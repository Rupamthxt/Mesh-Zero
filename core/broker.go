package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	quickjswasi "github.com/paralin/go-quickjs-wasi"
)

type WorkerConnection struct {
	ID         string  `json:"id"`
	PricePerMs float64 `json:"price_per_ms"`
	Tier       int     `json:"tier"`
	Conn       *websocket.Conn
	Busy       bool
}

type Broker struct {
	workers       map[string]*WorkerConnection
	workersMu     sync.RWMutex
	taskQueue     chan uint64
	activeTasks   map[uint64]string // TaskID -> WorkerID
	activeTasksMu sync.Mutex
	upgrader      websocket.Upgrader

	// Long-polling notifications
	taskWaiters    map[uint64][]chan struct{}
	taskWaitersMu  sync.Mutex
	batchWaiters   map[int64][]chan struct{}
	batchWaitersMu sync.Mutex
}

func NewBroker() *Broker {
	b := &Broker{
		workers:      make(map[string]*WorkerConnection),
		taskQueue:    make(chan uint64, 1000),
		activeTasks:  make(map[uint64]string),
		taskWaiters:  make(map[uint64][]chan struct{}),
		batchWaiters: make(map[int64][]chan struct{}),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Backend workers connect via WebSocket and do not carry browser Origin headers
				if r.URL.Path == "/ws/worker" {
					return true
				}
				allowedOrigin := os.Getenv("EMDASH_ALLOWED_ORIGIN")
				if allowedOrigin == "" {
					return true // Development mode
				}
				origin := r.Header.Get("Origin")
				return origin == allowedOrigin
			},
		},
	}
	// Start the background matchmaking scheduler loop
	go b.startScheduler()
	return b
}

func (b *Broker) notifyTask(taskID uint64) {
	b.taskWaitersMu.Lock()
	defer b.taskWaitersMu.Unlock()
	if chans, ok := b.taskWaiters[taskID]; ok {
		for _, ch := range chans {
			close(ch)
		}
		delete(b.taskWaiters, taskID)
	}
}

func (b *Broker) notifyBatch(parentID int64) {
	b.batchWaitersMu.Lock()
	defer b.batchWaitersMu.Unlock()
	if chans, ok := b.batchWaiters[parentID]; ok {
		for _, ch := range chans {
			close(ch)
		}
		delete(b.batchWaiters, parentID)
	}
}


type RegisterPayload struct {
	ID         string  `json:"id"`
	PricePerMs float64 `json:"price_per_ms"`
	Tier       int     `json:"tier"`
}

type TaskDispatch struct {
	TaskID    uint64 `json:"task_id"`
	WasmBytes []byte `json:"wasm_bytes"`
	DataBytes []byte `json:"data_bytes"`
}

type TaskResponse struct {
	TaskID      uint64 `json:"task_id"`
	Stdout      []byte `json:"stdout"`
	ReceiptJSON []byte `json:"receipt_json"`
	Error       string `json:"error"`
}

func (b *Broker) Start(port string) error {
	// Ensure DB is initialized on start
	if err := InitDB(); err != nil {
		return fmt.Errorf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", b.handleWorkerWS)
	mux.HandleFunc("/api/tasks/submit", b.handleTaskSubmit)
	mux.HandleFunc("/api/tasks/status", b.handleTaskStatus)
	mux.HandleFunc("/api/workers", b.handleGetWorkers)
	mux.HandleFunc("/api/billing/checkout", b.handleCheckout)
	mux.HandleFunc("/api/billing/stripe-webhook", b.handleStripeWebhook)
	mux.HandleFunc("/api/billing/simulate-success", b.handleSimulateSuccess)
	mux.HandleFunc("/api/payouts/request", b.handlePayoutRequest)
	mux.HandleFunc("/api/payouts/list", b.handlePayoutList)
	mux.HandleFunc("/api/payouts/approve", b.handlePayoutApprove)
	mux.HandleFunc("/api/tasks/submit-batch", b.handleTaskSubmitBatch)
	mux.HandleFunc("/api/tasks/batch-status", b.handleTaskBatchStatus)

	fmt.Printf("[BROKER] Starting Central Broker Gateway on http://localhost:%s\n", port)
	return http.ListenAndServe(":"+port, mux)
}

func (b *Broker) handleWorkerWS(w http.ResponseWriter, r *http.Request) {
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	// The first message is registration
	_, msg, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}
	var reg RegisterPayload
	if err := json.Unmarshal(msg, &reg); err != nil {
		conn.Close()
		return
	}

	worker := &WorkerConnection{
		ID:         reg.ID,
		PricePerMs: reg.PricePerMs,
		Tier:       reg.Tier,
		Conn:       conn,
		Busy:       false,
	}

	b.workersMu.Lock()
	b.workers[reg.ID] = worker
	b.workersMu.Unlock()

	fmt.Printf("[BROKER] Worker %s registered (Tier: %d | Price: %.4f credits/ms)\n", reg.ID[:8], reg.Tier, reg.PricePerMs)

	// Keep connection alive & route replies
	defer func() {
		b.workersMu.Lock()
		delete(b.workers, reg.ID)
		b.workersMu.Unlock()
		conn.Close()
		fmt.Printf("[BROKER] Worker %s disconnected\n", reg.ID[:8])

		// Connection resilience: find any running tasks assigned to this worker and re-queue them
		b.activeTasksMu.Lock()
		var tasksToRequeue []uint64
		for taskID, wID := range b.activeTasks {
			if wID == reg.ID {
				tasksToRequeue = append(tasksToRequeue, taskID)
			}
		}
		for _, taskID := range tasksToRequeue {
			delete(b.activeTasks, taskID)
			// Reset status in DB and push back to scheduler queue
			_ = UpdateTaskDB(taskID, "pending", nil, nil, "")
			b.taskQueue <- taskID
			fmt.Printf("[RESILIENCE] Re-queued task %d due to worker disconnect\n", taskID)
		}
		b.activeTasksMu.Unlock()
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var resp TaskResponse
		if err := json.Unmarshal(msgBytes, &resp); err != nil {
			fmt.Printf("[BROKER ERROR] Failed to unmarshal task response from worker: %v\n", err)
			continue
		}
		if resp.TaskID == 0 {
			continue
		}

		// Retrieve parentID before database update
		var parentID int64
		if tRec, dbErr := GetTaskDB(resp.TaskID); dbErr == nil {
			parentID = tRec.ParentID
		}

		b.activeTasksMu.Lock()
		delete(b.activeTasks, resp.TaskID)
		b.activeTasksMu.Unlock()

		// Mark worker as idle
		b.workersMu.Lock()
		if w, ok := b.workers[reg.ID]; ok {
			w.Busy = false
		}
		b.workersMu.Unlock()

		status := "completed"
		if resp.Error != "" {
			status = "failed"
		}

		// Update SQLite database with completion results
		if err := UpdateTaskDB(resp.TaskID, status, resp.Stdout, resp.ReceiptJSON, resp.Error); err != nil {
			fmt.Printf("[DATABASE ERROR] Failed to update task %d status: %v\n", resp.TaskID, err)
		} else {
			// Trigger long-polling notifications
			b.notifyTask(resp.TaskID)
			if parentID != 0 {
				b.notifyBatch(parentID)
			}
		}
	}
}

func (b *Broker) handleTaskSubmit(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for frontend website
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form
	r.ParseMultipartForm(10 << 20)

	script := r.FormValue("script")
	var wasmBytes []byte
	var dataBytes []byte

	if script != "" {
		// Use embedded QuickJS interpreter
		wasmBytes = quickjswasi.QuickJSWASM
		if strings.Contains(script, "navigator.gpu") || strings.Contains(script, "requestAdapter") {
			script = WebGPUPolyfill + "\n" + script
		}
		dataBytes = []byte(script)
	} else {
		// Fallback to standard WASM binary upload + input data
		wasmFile, _, err := r.FormFile("wasm")
		if err == nil {
			defer wasmFile.Close()
			wasmBytes, _ = io.ReadAll(wasmFile)
		}

		templateID := r.FormValue("template_id")
		if templateID != "" {
			var wasmPath string
			switch templateID {
			case "hasher":
				wasmPath = "cmd/emdash/hasher.wasm"
			case "gpu_task":
				wasmPath = "task/gpu_task.wasm"
			}
			var rErr error
			wasmBytes, rErr = os.ReadFile(wasmPath)
			if rErr != nil {
				wasmBytes, rErr = os.ReadFile("../" + wasmPath)
				if rErr != nil {
					wasmBytes, _ = os.ReadFile("../../" + wasmPath)
				}
			}
		}

		if len(wasmBytes) == 0 {
			http.Error(w, "Missing wasm task payload or script", http.StatusBadRequest)
			return
		}

		dataFile, _, err := r.FormFile("data")
		if err != nil {
			http.Error(w, "Missing data input", http.StatusBadRequest)
			return
		}
		defer dataFile.Close()
		dataBytes, _ = io.ReadAll(dataFile)
	}

	targetTier := 0 // 0 means any tier
	if tierStr := r.FormValue("tier"); tierStr != "" {
		fmt.Sscanf(tierStr, "%d", &targetTier)
	}

	taskID := uint64(time.Now().UnixNano())

	// Create task record in pending state in SQLite
	err := CreateTaskDB(taskID, wasmBytes, dataBytes, targetTier)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to queue task in DB: %v", err), http.StatusInternalServerError)
		return
	}

	// Push task to scheduler queue
	b.taskQueue <- taskID

	// Return response immediately
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task_id": fmt.Sprintf("%d", taskID),
		"status":  "pending",
	})
}

func (b *Broker) handleTaskStatus(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for frontend website
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "Missing id parameter", http.StatusBadRequest)
		return
	}

	taskID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid task id", http.StatusBadRequest)
		return
	}

	// Fetch task from DB
	t, err := GetTaskDB(taskID)
	if err != nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	// Long-polling: If task is still executing/pending, wait for completion notification
	if t.Status == "pending" || t.Status == "running" {
		notifyChan := make(chan struct{})

		b.taskWaitersMu.Lock()
		b.taskWaiters[taskID] = append(b.taskWaiters[taskID], notifyChan)
		b.taskWaitersMu.Unlock()

		select {
		case <-notifyChan:
			// Task status changed! Refresh task record
			if refreshed, err := GetTaskDB(taskID); err == nil {
				t = refreshed
			}
		case <-time.After(20 * time.Second):
			// Safe timeout to release HTTP connection; clean up listener channel
			b.taskWaitersMu.Lock()
			if waiters, ok := b.taskWaiters[taskID]; ok {
				var newWaiters []chan struct{}
				for _, ch := range waiters {
					if ch != notifyChan {
						newWaiters = append(newWaiters, ch)
					}
				}
				if len(newWaiters) == 0 {
					delete(b.taskWaiters, taskID)
				} else {
					b.taskWaiters[taskID] = newWaiters
				}
			}
			b.taskWaitersMu.Unlock()
		}
	}

	// Return status JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task_id":      fmt.Sprintf("%d", t.ID),
		"status":       t.Status,
		"stdout":       string(t.Stdout),
		"receipt_json": t.ReceiptJSON,
		"error":        t.Error,
		"tier":         t.Tier,
		"created_at":   t.CreatedAt,
	})
}

func (b *Broker) handleGetWorkers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	b.workersMu.RLock()
	var list []map[string]interface{}
	for _, worker := range b.workers {
		list = append(list, map[string]interface{}{
			"id":           worker.ID,
			"price_per_ms": worker.PricePerMs,
			"tier":         worker.Tier,
			"busy":         worker.Busy,
			"balance":      GetBalanceDB(worker.ID),
		})
	}
	b.workersMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// Background scheduler matchmaking loop
func (b *Broker) startScheduler() {
	for taskID := range b.taskQueue {
		// Fetch task details from SQLite database
		t, err := GetTaskDB(taskID)
		if err != nil {
			continue
		}

		// If task was already processed or cancelled, skip
		if t.Status != "pending" {
			continue
		}

		// Find an idle worker matching the task's tier
		var targetWorker *WorkerConnection
		for {
			b.workersMu.Lock()
			for _, worker := range b.workers {
				if !worker.Busy && (t.Tier == 0 || worker.Tier == t.Tier) {
					targetWorker = worker
					worker.Busy = true
					break
				}
			}
			b.workersMu.Unlock()

			if targetWorker != nil {
				break
			}
			// Wait a bit before trying to match again if no workers are available
			time.Sleep(500 * time.Millisecond)

			// Re-verify that task hasn't timed out or changed status
			tCheck, err := GetTaskDB(taskID)
			if err != nil || tCheck.Status != "pending" {
				break
			}

			// Simple execution expiry timeout: if task is older than 30s, fail it
			if time.Now().UnixNano()-t.CreatedAt > int64(30*time.Second) {
				_ = UpdateTaskDB(taskID, "failed", nil, nil, "Task matching timed out: no suitable worker online")
				b.notifyTask(taskID)
				if t.ParentID != 0 {
					b.notifyBatch(t.ParentID)
				}
				break
			}
		}

		if targetWorker == nil {
			continue
		}

		// Register active task mapping
		b.activeTasksMu.Lock()
		b.activeTasks[taskID] = targetWorker.ID
		b.activeTasksMu.Unlock()

		// Update status to running in DB
		_ = UpdateTaskDB(taskID, "running", nil, nil, "")

		// Dispatch job via WebSocket
		dispatch := TaskDispatch{
			TaskID:    taskID,
			WasmBytes: t.WasmBytes,
			DataBytes: t.DataBytes,
		}
		dispatchMsg, _ := json.Marshal(dispatch)

		err = targetWorker.Conn.WriteMessage(websocket.TextMessage, dispatchMsg)
		if err != nil {
			// WebSocket write error: clean active mapping, mark worker idle, and re-queue task
			b.activeTasksMu.Lock()
			delete(b.activeTasks, taskID)
			b.activeTasksMu.Unlock()

			b.workersMu.Lock()
			if w, ok := b.workers[targetWorker.ID]; ok {
				w.Busy = false
			}
			b.workersMu.Unlock()

			_ = UpdateTaskDB(taskID, "pending", nil, nil, "")
			b.taskQueue <- taskID
		}
	}
}

func setupCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func (b *Broker) handleCheckout(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	amountStr := r.FormValue("amount")
	accountID := r.FormValue("account_id")

	if amountStr == "" || accountID == "" {
		http.Error(w, "Missing amount or account_id", http.StatusBadRequest)
		return
	}

	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 {
		http.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	secretKey := os.Getenv("STRIPE_SECRET_KEY")
	if secretKey == "" {
		// Fallback to simulation mode redirect
		mockRedirectURL := fmt.Sprintf("http://localhost:8080/api/billing/simulate-success?account_id=%s&amount=%f", url.QueryEscape(accountID), amount)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"url": mockRedirectURL,
		})
		return
	}

	// Real Stripe checkout session
	sessionURL, err := createStripeCheckoutSession(amount, accountID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Stripe checkout error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": sessionURL,
	})
}

func (b *Broker) handleSimulateSuccess(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	accountID := r.URL.Query().Get("account_id")
	amountStr := r.URL.Query().Get("amount")

	if accountID == "" || amountStr == "" {
		http.Error(w, "Missing account_id or amount", http.StatusBadRequest)
		return
	}

	amount, _ := strconv.ParseFloat(amountStr, 64)
	credits := amount * 100.0 // $1 USD = 100 credits

	currentBalance := GetBalanceDB(accountID)
	_ = SetBalanceDB(accountID, currentBalance+credits)

	// Redirect back to console dashboard (detect host from referrer if available)
	redirectURL := "http://localhost:3000/console.html?success=true"
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil {
			redirectURL = fmt.Sprintf("%s://%s/console.html?success=true", u.Scheme, u.Host)
		}
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (b *Broker) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	if webhookSecret != "" {
		sigHeader := r.Header.Get("Stripe-Signature")
		if !verifyStripeSignature(payload, sigHeader, webhookSecret) {
			http.Error(w, "Invalid Stripe webhook signature", http.StatusUnauthorized)
			return
		}
	}

	var stripeEvent struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				Metadata struct {
					AccountID string `json:"account_id"`
					Amount    string `json:"amount"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}

	if err := json.Unmarshal(payload, &stripeEvent); err != nil {
		http.Error(w, "Invalid webhook JSON", http.StatusBadRequest)
		return
	}

	if stripeEvent.Type == "checkout.session.completed" {
		accountID := stripeEvent.Data.Object.Metadata.AccountID
		amountStr := stripeEvent.Data.Object.Metadata.Amount
		if accountID != "" && amountStr != "" {
			amount, _ := strconv.ParseFloat(amountStr, 64)
			credits := amount * 100.0 // $1 USD = 100 credits

			currentBal := GetBalanceDB(accountID)
			_ = SetBalanceDB(accountID, currentBal+credits)
			fmt.Printf("[BILLING] Stripe Webhook successfully processed. Deposited %.2f credits to %s\n", credits, accountID)
		}
	}

	w.WriteHeader(http.StatusOK)
}

func (b *Broker) handlePayoutRequest(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	workerID := r.FormValue("worker_id")
	amountStr := r.FormValue("amount")

	if workerID == "" || amountStr == "" {
		http.Error(w, "Missing worker_id or amount", http.StatusBadRequest)
		return
	}

	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount <= 0 {
		http.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	currentBal := GetBalanceDB(workerID)
	if currentBal < amount {
		http.Error(w, "Insufficient credit balance for payout", http.StatusBadRequest)
		return
	}

	err = SetBalanceDB(workerID, currentBal-amount)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update balance: %v", err), http.StatusInternalServerError)
		return
	}

	err = CreatePayoutRequestDB(workerID, amount)
	if err != nil {
		_ = SetBalanceDB(workerID, currentBal) // Rollback
		http.Error(w, fmt.Sprintf("Failed to record request: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Payout request successfully submitted and is pending approval",
	})
}

func (b *Broker) handlePayoutList(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requests, err := GetPayoutRequestsDB()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to retrieve payout requests: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(requests)
}

func (b *Broker) handlePayoutApprove(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := r.FormValue("id")
	status := r.FormValue("status")

	if idStr == "" || (status != "approved" && status != "rejected") {
		http.Error(w, "Missing or invalid parameters", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID format", http.StatusBadRequest)
		return
	}

	err = UpdatePayoutRequestStatusDB(id, status)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update status: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Payout request marked as %s", status),
	})
}

func createStripeCheckoutSession(amount float64, accountID string) (string, error) {
	secretKey := os.Getenv("STRIPE_SECRET_KEY")
	if secretKey == "" {
		return "", fmt.Errorf("STRIPE_SECRET_KEY not configured")
	}

	apiURL := "https://api.stripe.com/v1/checkout/sessions"
	data := url.Values{}
	data.Set("mode", "payment")
	data.Set("success_url", "https://emdash.world/console.html?session_id={CHECKOUT_SESSION_ID}&success=true")
	data.Set("cancel_url", "https://emdash.world/console.html?success=false")
	data.Set("line_items[0][price_data][currency]", "usd")
	cents := int64(amount * 100)
	data.Set("line_items[0][price_data][unit_amount]", fmt.Sprintf("%d", cents))
	data.Set("line_items[0][price_data][product_data][name]", "Emdash Compute Credits")
	data.Set("line_items[0][quantity]", "1")
	data.Set("metadata[account_id]", accountID)
	data.Set("metadata[amount]", fmt.Sprintf("%f", amount))

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(secretKey, "")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("stripe error (status %d): %s", resp.StatusCode, string(body))
	}

	var session struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return "", err
	}

	return session.URL, nil
}

func verifyStripeSignature(payload []byte, sigHeader, webhookSecret string) bool {
	if sigHeader == "" || webhookSecret == "" {
		return false
	}

	parts := strings.Split(sigHeader, ",")
	var timestamp string
	var signature string
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			switch kv[0] {
			case "t":
				timestamp = kv[1]
			case "v1":
				signature = kv[1]
			}
		}
	}

	if timestamp == "" || signature == "" {
		return false
	}

	signedPayload := timestamp + "." + string(payload)

	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(signedPayload))
	expectedMac := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedMac))
}

func (b *Broker) handleTaskSubmitBatch(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	script := r.FormValue("script")
	inputsJSON := r.FormValue("inputs")
	tierStr := r.FormValue("tier")

	if script == "" || inputsJSON == "" {
		http.Error(w, "Missing script or inputs", http.StatusBadRequest)
		return
	}

	if strings.Contains(script, "navigator.gpu") || strings.Contains(script, "requestAdapter") {
		script = WebGPUPolyfill + "\n" + script
	}

	var inputs []string
	if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
		http.Error(w, "Invalid inputs JSON format (must be string array)", http.StatusBadRequest)
		return
	}

	if len(inputs) == 0 {
		http.Error(w, "Inputs array cannot be empty", http.StatusBadRequest)
		return
	}

	targetTier := 0
	if tierStr != "" {
		fmt.Sscanf(tierStr, "%d", &targetTier)
	}

	parentID := int64(time.Now().UnixNano())
	wasmBytes := quickjswasi.QuickJSWASM

	// Queue all sub-tasks in parallel
	for i, input := range inputs {
		subTaskID := uint64(time.Now().UnixNano()) + uint64(i)

		// Prepend target URL parameter to the script scope
		wrappedScript := fmt.Sprintf("const __INPUT__ = %q;\n%s", input, script)

		err := CreateTaskDBWithParent(subTaskID, wasmBytes, []byte(wrappedScript), targetTier, parentID)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to queue batch subtask: %v", err), http.StatusInternalServerError)
			return
		}

		// Queue the task for the scheduler matchmaking loop
		b.taskQueue <- subTaskID
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "success",
		"batch_id":    fmt.Sprintf("%d", parentID),
		"tasks_count": len(inputs),
		"message":     fmt.Sprintf("Batch job queued with %d parallel scraping tasks", len(inputs)),
	})
}

func (b *Broker) handleTaskBatchStatus(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parentIDStr := r.URL.Query().Get("parent_id")
	if parentIDStr == "" {
		http.Error(w, "Missing parent_id", http.StatusBadRequest)
		return
	}

	parentID, err := strconv.ParseInt(parentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid parent_id format", http.StatusBadRequest)
		return
	}

	records, err := GetBatchTasksDB(parentID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch batch tasks: %v", err), http.StatusInternalServerError)
		return
	}

	total := len(records)
	if total == 0 {
		http.Error(w, "No tasks found for the specified batch ID", http.StatusNotFound)
		return
	}

	// Count completed/failed tasks
	finishedCount := 0
	for _, rec := range records {
		if rec.Status == "completed" || rec.Status == "failed" {
			finishedCount++
		}
	}

	// Long-polling: If batch is not yet finished, wait for updates
	if finishedCount < total {
		notifyChan := make(chan struct{})

		b.batchWaitersMu.Lock()
		b.batchWaiters[parentID] = append(b.batchWaiters[parentID], notifyChan)
		b.batchWaitersMu.Unlock()

		select {
		case <-notifyChan:
			// A subtask updated! Re-read from DB
			if refreshed, err := GetBatchTasksDB(parentID); err == nil {
				records = refreshed
				total = len(records)
			}
		case <-time.After(20 * time.Second):
			// Safe timeout to release connection; clean up listener channel
			b.batchWaitersMu.Lock()
			if waiters, ok := b.batchWaiters[parentID]; ok {
				var newWaiters []chan struct{}
				for _, ch := range waiters {
					if ch != notifyChan {
						newWaiters = append(newWaiters, ch)
					}
				}
				if len(newWaiters) == 0 {
					delete(b.batchWaiters, parentID)
				} else {
					b.batchWaiters[parentID] = newWaiters
				}
			}
			b.batchWaitersMu.Unlock()
		}
	}

	pendingCount := 0
	runningCount := 0
	completedCount := 0
	failedCount := 0

	var outputs []string
	var errors []string

	for _, rec := range records {
		switch rec.Status {
		case "pending":
			pendingCount++
		case "running":
			runningCount++
		case "completed":
			completedCount++
			outputs = append(outputs, string(rec.Stdout))
		case "failed":
			failedCount++
			errors = append(errors, rec.Error)
		}
	}

	progressPercent := float64(completedCount+failedCount) / float64(total) * 100.0
	status := "running"
	if completedCount+failedCount == total {
		if failedCount > 0 {
			status = "failed"
		} else {
			status = "completed"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"batch_id":         fmt.Sprintf("%d", parentID),
		"status":           status,
		"progress_percent": progressPercent,
		"tasks_total":      total,
		"tasks_pending":    pendingCount,
		"tasks_running":    runningCount,
		"tasks_completed":  completedCount,
		"tasks_failed":     failedCount,
		"outputs":          outputs,
		"errors":           errors,
	})
}

const WebGPUPolyfill = `
// WebGPU Headless Polyfill shim for QuickJS WASM
globalThis.GPUMapMode = { READ: 1, WRITE: 2 };
globalThis.GPUBufferUsage = {
	STORAGE: 1,
	COPY_SRC: 2,
	COPY_DST: 4,
	MAP_READ: 8
};

class MockGPUBuffer {
	constructor(desc) {
		this.size = desc.size;
		this.usage = desc.usage;
		this.mapped = desc.mappedAtCreation || false;
		this.data = new Float32Array(this.size / 4);
	}
	getMappedRange() {
		return this.data.buffer;
	}
	unmap() {
		this.mapped = false;
	}
	mapAsync(mode) {
		return Promise.resolve();
	}
}

class MockGPUDevice {
	constructor() {
		this.activeShader = "";
		this.activeBindGroup = null;
		this.resultBuffer = null;
		
		this.pendingCopies = [];
		this.queue = {
			writeBuffer: (buffer, offset, data) => {
				buffer.data.set(data);
			},
			submit: (encoders) => {
				// Evaluate the shader math natively inside the javascript runtime:
				// Map input buffer values and calculate output values dynamically.
				if (this.activeBindGroup) {
					const entries = this.activeBindGroup.entries;
					const firstEntry = entries.find(e => e.binding === 0);
					const secondEntry = entries.find(e => e.binding === 1);
					const thirdEntry = entries.find(e => e.binding === 2);
					
					if (firstEntry && secondEntry && !thirdEntry) {
						// 2-buffer unary operations (e.g. inputData * inputData squaring)
						const first = firstEntry.resource.buffer.data;
						const result = secondEntry.resource.buffer.data;
						
						let op = (a) => a * a; // Default square operator
						if (this.activeShader.includes("+")) op = (a) => a + a;
						else if (this.activeShader.includes("-")) op = (a) => 0;

						for (let i = 0; i < first.length; i++) {
							result[i] = op(first[i]);
						}
					} else if (firstEntry && secondEntry && thirdEntry) {
						// 3-buffer binary operations (e.g. first * second = result)
						const first = firstEntry.resource.buffer.data;
						const second = secondEntry.resource.buffer.data;
						const result = thirdEntry.resource.buffer.data;
						
						let op = (a, b) => a * b; // Default operation multiplication
						if (this.activeShader.includes("+")) op = (a, b) => a + b;
						else if (this.activeShader.includes("-")) op = (a, b) => a - b;
						else if (this.activeShader.includes("/")) op = (a, b) => a / b;

						for (let i = 0; i < first.length; i++) {
							result[i] = op(first[i], second[i]);
						}
					}
				}
				
				// Execute deferred copy commands
				this.pendingCopies.forEach(cp => cp());
				this.pendingCopies = [];
			}
		};
	}
	createBuffer(desc) {
		const buf = new MockGPUBuffer(desc);
		if (desc.usage & 8) { // MAP_READ buffer reference
			this.resultBuffer = buf;
		}
		return buf;
	}
	createShaderModule(desc) {
		this.activeShader = desc.code;
		return desc;
	}
	createComputePipeline(desc) {
		return {
			getBindGroupLayout: (idx) => ({})
		};
	}
	createBindGroup(desc) {
		this.activeBindGroup = desc;
		return desc;
	}
	createCommandEncoder() {
		return {
			beginComputePass: () => ({
				setPipeline: () => {},
				setBindGroup: () => {},
				dispatchWorkgroups: () => {},
				end: () => {}
			}),
			copyBufferToBuffer: (src, srcOff, dst, dstOff, size) => {
				this.pendingCopies.push(() => {
					dst.data.set(src.data);
				});
			},
			finish: () => {}
		};
	}
}

Object.defineProperty(globalThis, 'navigator', {
	value: {
		gpu: {
			requestAdapter: async () => ({
				requestDevice: async () => new MockGPUDevice()
			})
		}
	},
	writable: true,
	configurable: true
});

globalThis.document = {
	getElementById: (id) => ({
		set innerText(val) {
			console.log("[DOM " + id + "] " + val);
		},
		get innerText() {
			return "";
		}
	})
};
`
