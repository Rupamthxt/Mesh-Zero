package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WorkerConnection struct {
	ID            string  `json:"id"`
	IP            string  `json:"ip"`
	Country       string  `json:"country"`
	CountryCode   string  `json:"country_code"`
	City          string  `json:"city"`
	ISP           string  `json:"isp"`
	PricePerTask  float64 `json:"price_per_task"`
	Conn          *websocket.Conn
	Busy          bool
	CompletedJobs int `json:"completed_jobs"`
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
		taskQueue:    make(chan uint64, 5000),
		activeTasks:  make(map[uint64]string),
		taskWaiters:  make(map[uint64][]chan struct{}),
		batchWaiters: make(map[int64][]chan struct{}),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
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

func (b *Broker) Start(port string) error {
	if err := InitDB(); err != nil {
		return fmt.Errorf("failed to init db: %w", err)
	}

	mux := http.NewServeMux()

	// Worker WebSocket gateway
	mux.HandleFunc("/ws/worker", b.handleWorkerWS)

	// AI Agent & Developer Extraction REST Endpoints
	mux.HandleFunc("/api/extract", b.handleExtract)
	mux.HandleFunc("/api/extract/status", b.handleExtractStatus)
	mux.HandleFunc("/api/extract/batch", b.handleExtractBatch)
	mux.HandleFunc("/api/extract/batch-status", b.handleExtractBatchStatus)

	// Residential Nodes Fleet status
	mux.HandleFunc("/api/nodes", b.handleGetNodes)
	mux.HandleFunc("/api/workers", b.handleGetNodes) // Alias

	// Billing & Payouts
	mux.HandleFunc("/api/billing/checkout", b.handleCheckout)
	mux.HandleFunc("/api/billing/stripe-webhook", b.handleStripeWebhook)
	mux.HandleFunc("/api/billing/simulate-success", b.handleSimulateSuccess)
	mux.HandleFunc("/api/payouts/request", b.handlePayoutRequest)
	mux.HandleFunc("/api/payouts/list", b.handlePayoutList)
	mux.HandleFunc("/api/payouts/approve", b.handlePayoutApprove)

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b.workersMu.RLock()
		onlineNodes := len(b.workers)
		b.workersMu.RUnlock()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "healthy",
			"online_nodes": onlineNodes,
			"timestamp":    time.Now().Unix(),
		})
	})

	fmt.Printf("[BROKER] Residential Web Extraction Gateway running on http://localhost:%s\n", port)
	return http.ListenAndServe(":"+port, mux)
}

func (b *Broker) handleWorkerWS(w http.ResponseWriter, r *http.Request) {
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	// 1. First message must be Node Registration payload
	_, msg, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}

	var reg struct {
		ID           string  `json:"id"`
		IP           string  `json:"ip"`
		Country      string  `json:"country"`
		CountryCode  string  `json:"country_code"`
		City         string  `json:"city"`
		ISP          string  `json:"isp"`
		PricePerTask float64 `json:"price_per_task"`
	}

	if err := json.Unmarshal(msg, &reg); err != nil {
		conn.Close()
		return
	}

	if reg.PricePerTask <= 0 {
		reg.PricePerTask = 1.0
	}

	worker := &WorkerConnection{
		ID:            reg.ID,
		IP:            reg.IP,
		Country:       reg.Country,
		CountryCode:   strings.ToUpper(reg.CountryCode),
		City:          reg.City,
		ISP:           reg.ISP,
		PricePerTask:  reg.PricePerTask,
		Conn:          conn,
		Busy:          false,
		CompletedJobs: 0,
	}

	b.workersMu.Lock()
	b.workers[reg.ID] = worker
	b.workersMu.Unlock()

	fmt.Printf("[BROKER] Residential Node Registered: %s | %s, %s (%s) | ISP: %s\n",
		reg.ID[:8], reg.City, reg.Country, reg.CountryCode, reg.ISP)

	// Keep connection alive & handle job completions
	defer func() {
		b.workersMu.Lock()
		delete(b.workers, reg.ID)
		b.workersMu.Unlock()
		conn.Close()
		fmt.Printf("[BROKER] Residential Node %s disconnected\n", reg.ID[:8])

		// Fault Tolerance: Re-queue in-flight tasks assigned to this disconnected worker
		b.activeTasksMu.Lock()
		var toRequeue []uint64
		for taskID, wID := range b.activeTasks {
			if wID == reg.ID {
				toRequeue = append(toRequeue, taskID)
			}
		}
		for _, taskID := range toRequeue {
			delete(b.activeTasks, taskID)
			_ = UpdateTaskDB(taskID, "pending", 0, 0, nil, nil, "")
			b.taskQueue <- taskID
			fmt.Printf("[RESILIENCE] Re-queued extraction job #%d due to node disconnect\n", taskID)
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
			continue
		}
		if resp.TaskID == 0 {
			continue
		}

		// Retrieve parentID before DB update
		var parentID int64
		if tRec, dbErr := GetTaskDB(resp.TaskID); dbErr == nil {
			parentID = tRec.ParentID
		}

		b.activeTasksMu.Lock()
		delete(b.activeTasks, resp.TaskID)
		b.activeTasksMu.Unlock()

		b.workersMu.Lock()
		if wk, ok := b.workers[reg.ID]; ok {
			wk.Busy = false
			if resp.Error == "" {
				wk.CompletedJobs++
			}
		}
		b.workersMu.Unlock()

		status := "completed"
		if resp.Error != "" {
			status = "failed"
		}

		// Update database with extraction result
		_ = UpdateTaskDB(resp.TaskID, status, resp.StatusCode, resp.LatencyMs, resp.ResultJSON, resp.ReceiptJSON, resp.Error)

		// Credit worker node if extraction was successful
		if status == "completed" {
			currentBal := GetBalanceDB(reg.ID)
			_ = SetBalanceDB(reg.ID, currentBal+worker.PricePerTask)
		}

		b.notifyTask(resp.TaskID)
		if parentID != 0 {
			b.notifyBatch(parentID)
		}
	}
}

// REST: Submit single URL extraction job
func (b *Broker) handleExtract(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ExtractionRequest
	contentType := r.Header.Get("Content-Type")

	if strings.Contains(contentType, "application/json") {
		bodyBytes, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	} else {
		req.URL = r.FormValue("url")
		req.Country = r.FormValue("country")
		req.ExtractScript = r.FormValue("extract_script")
		if tStr := r.FormValue("timeout_sec"); tStr != "" {
			req.TimeoutSec, _ = strconv.Atoi(tStr)
		}
	}

	if req.URL == "" {
		http.Error(w, `Missing "url" parameter`, http.StatusBadRequest)
		return
	}

	// Validate target URL for safety compliance
	if err := ValidateTargetURL(req.URL); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("Target URL violates safety policy: %v", err),
		})
		return
	}

	taskID := uint64(time.Now().UnixNano())
	if err := CreateTaskDB(taskID, req.URL, strings.ToUpper(req.Country), req.ExtractScript, 0); err != nil {
		http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	// Push task to scheduler
	b.taskQueue <- taskID

	// Check if caller wants synchronous response (e.g. ?sync=true)
	isSync := r.URL.Query().Get("sync") == "true" || r.URL.Query().Get("wait") == "true"
	if isSync {
		t := b.waitForTask(taskID, 25*time.Second)
		w.Header().Set("Content-Type", "application/json")
		if t == nil {
			w.WriteHeader(http.StatusGatewayTimeout)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Extraction timed out waiting for node execution",
			})
			return
		}

		var parsedData interface{}
		if t.ResultJSON != "" {
			_ = json.Unmarshal([]byte(t.ResultJSON), &parsedData)
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"task_id":      fmt.Sprintf("%d", t.ID),
			"url":          t.URL,
			"status":       t.Status,
			"status_code":  t.StatusCode,
			"latency_ms":   t.LatencyMs,
			"data":         parsedData,
			"receipt_json": t.ReceiptJSON,
			"error":        t.Error,
		})
		return
	}

	// Default: Asynchronous submission
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "pending",
		"task_id":    fmt.Sprintf("%d", taskID),
		"status_url": fmt.Sprintf("/api/extract/status?id=%d", taskID),
	})
}

func (b *Broker) waitForTask(taskID uint64, timeout time.Duration) *TaskRecord {
	t, err := GetTaskDB(taskID)
	if err != nil {
		return nil
	}
	if t.Status == "completed" || t.Status == "failed" {
		return t
	}

	notifyChan := make(chan struct{})
	b.taskWaitersMu.Lock()
	b.taskWaiters[taskID] = append(b.taskWaiters[taskID], notifyChan)
	b.taskWaitersMu.Unlock()

	select {
	case <-notifyChan:
		refreshed, _ := GetTaskDB(taskID)
		return refreshed
	case <-time.After(timeout):
		b.taskWaitersMu.Lock()
		delete(b.taskWaiters, taskID)
		b.taskWaitersMu.Unlock()
		refreshed, _ := GetTaskDB(taskID)
		return refreshed
	}
}

// REST: Check status of single extraction job (supports long-polling)
func (b *Broker) handleExtractStatus(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}

	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "Missing id parameter", http.StatusBadRequest)
		return
	}

	taskID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid task ID", http.StatusBadRequest)
		return
	}

	t := b.waitForTask(taskID, 15*time.Second)
	if t == nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}

	var parsedData interface{}
	if t.ResultJSON != "" {
		_ = json.Unmarshal([]byte(t.ResultJSON), &parsedData)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task_id":      fmt.Sprintf("%d", t.ID),
		"url":          t.URL,
		"status":       t.Status,
		"status_code":  t.StatusCode,
		"latency_ms":   t.LatencyMs,
		"data":         parsedData,
		"receipt_json": t.ReceiptJSON,
		"error":        t.Error,
		"created_at":   t.CreatedAt,
	})
}

// REST: Batch URL extraction
func (b *Broker) handleExtractBatch(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URLs          []string `json:"urls"`
		Country       string   `json:"country,omitempty"`
		ExtractScript string   `json:"extract_script,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	if len(req.URLs) == 0 {
		http.Error(w, "URLs array cannot be empty", http.StatusBadRequest)
		return
	}

	parentID := int64(time.Now().UnixNano())
	country := strings.ToUpper(req.Country)

	for i, u := range req.URLs {
		if err := ValidateTargetURL(u); err != nil {
			continue // Skip URLs violating safety policy
		}

		subTaskID := uint64(time.Now().UnixNano()) + uint64(i)
		_ = CreateTaskDB(subTaskID, u, country, req.ExtractScript, parentID)
		b.taskQueue <- subTaskID
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "success",
		"batch_id":    fmt.Sprintf("%d", parentID),
		"tasks_count": len(req.URLs),
		"status_url":  fmt.Sprintf("/api/extract/batch-status?batch_id=%d", parentID),
	})
}

// REST: Check status of batch extraction
func (b *Broker) handleExtractBatchStatus(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}

	parentIDStr := r.URL.Query().Get("batch_id")
	if parentIDStr == "" {
		http.Error(w, "Missing batch_id", http.StatusBadRequest)
		return
	}

	parentID, err := strconv.ParseInt(parentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid batch_id", http.StatusBadRequest)
		return
	}

	records, err := GetBatchTasksDB(parentID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Database error: %v", err), http.StatusInternalServerError)
		return
	}

	completedCount := 0
	failedCount := 0
	pendingCount := 0

	var results []map[string]interface{}
	for _, rec := range records {
		if rec.Status == "completed" {
			completedCount++
		} else if rec.Status == "failed" {
			failedCount++
		} else {
			pendingCount++
		}

		var parsedData interface{}
		if rec.ResultJSON != "" {
			_ = json.Unmarshal([]byte(rec.ResultJSON), &parsedData)
		}

		results = append(results, map[string]interface{}{
			"id":          fmt.Sprintf("%d", rec.ID),
			"url":         rec.URL,
			"status":      rec.Status,
			"status_code": rec.StatusCode,
			"latency_ms":  rec.LatencyMs,
			"data":        parsedData,
			"error":       rec.Error,
		})
	}

	overallStatus := "in_progress"
	if pendingCount == 0 {
		overallStatus = "completed"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"batch_id":    fmt.Sprintf("%d", parentID),
		"status":      overallStatus,
		"total":       len(records),
		"completed":   completedCount,
		"failed":      failedCount,
		"pending":     pendingCount,
		"extractions": results,
	})
}

// REST: List all registered residential nodes
func (b *Broker) handleGetNodes(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}

	b.workersMu.RLock()
	var list []map[string]interface{}
	for _, wk := range b.workers {
		list = append(list, map[string]interface{}{
			"id":             wk.ID[:16] + "...",
			"ip":             wk.IP,
			"country":        wk.Country,
			"country_code":   wk.CountryCode,
			"city":           wk.City,
			"isp":            wk.ISP,
			"price_per_task": wk.PricePerTask,
			"busy":           wk.Busy,
			"completed_jobs": wk.CompletedJobs,
			"balance":        GetBalanceDB(wk.ID),
		})
	}
	b.workersMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// Background scheduler matchmaking loop
func (b *Broker) startScheduler() {
	for taskID := range b.taskQueue {
		t, err := GetTaskDB(taskID)
		if err != nil || t.Status != "pending" {
			continue
		}

		var targetWorker *WorkerConnection

		// Wait and match an idle worker (matching target country if specified)
		for {
			b.workersMu.Lock()
			for _, worker := range b.workers {
				if !worker.Busy {
					if t.Country == "" || worker.CountryCode == t.Country {
						targetWorker = worker
						worker.Busy = true
						break
					}
				}
			}
			b.workersMu.Unlock()

			if targetWorker != nil {
				break
			}

			time.Sleep(500 * time.Millisecond)

			// Timeout check: if no suitable residential node is online after 45s, fail task
			if time.Now().UnixNano()-t.CreatedAt > int64(45*time.Second) {
				_ = UpdateTaskDB(taskID, "failed", 0, 0, nil, nil, "No suitable residential node online for specified country")
				b.notifyTask(taskID)
				if t.ParentID != 0 {
					b.notifyBatch(t.ParentID)
				}
				break
			}

			// Verify task status hasn't changed
			tCheck, err := GetTaskDB(taskID)
			if err != nil || tCheck.Status != "pending" {
				break
			}
		}

		if targetWorker == nil {
			continue
		}

		// Map active task
		b.activeTasksMu.Lock()
		b.activeTasks[taskID] = targetWorker.ID
		b.activeTasksMu.Unlock()

		_ = UpdateTaskDB(taskID, "running", 0, 0, nil, nil, "")

		// Dispatch to worker over WebSocket
		dispatch := TaskDispatch{
			TaskID:        taskID,
			URL:           t.URL,
			ExtractScript: t.ExtractScript,
			TimeoutSec:    15,
		}
		msg, _ := json.Marshal(dispatch)

		err = targetWorker.Conn.WriteMessage(websocket.TextMessage, msg)
		if err != nil {
			// Write failed: re-queue task and mark worker idle
			b.activeTasksMu.Lock()
			delete(b.activeTasks, taskID)
			b.activeTasksMu.Unlock()

			b.workersMu.Lock()
			if wk, ok := b.workers[targetWorker.ID]; ok {
				wk.Busy = false
			}
			b.workersMu.Unlock()

			_ = UpdateTaskDB(taskID, "pending", 0, 0, nil, nil, "")
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

	mockRedirectURL := fmt.Sprintf("http://localhost:8080/api/billing/simulate-success?account_id=%s&amount=%f", url.QueryEscape(accountID), amount)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": mockRedirectURL,
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
	credits := amount * 100.0 // $1 = 100 extraction credits

	currentBalance := GetBalanceDB(accountID)
	_ = SetBalanceDB(accountID, currentBalance+credits)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "success",
		"account_id":  accountID,
		"new_balance": currentBalance + credits,
	})
}

func (b *Broker) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (b *Broker) handlePayoutRequest(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	workerID := r.FormValue("worker_id")
	amountStr := r.FormValue("amount")

	amount, _ := strconv.ParseFloat(amountStr, 64)
	if workerID == "" || amount <= 0 {
		http.Error(w, "Invalid payout parameters", http.StatusBadRequest)
		return
	}

	currentBal := GetBalanceDB(workerID)
	if currentBal < amount {
		http.Error(w, "Insufficient balance", http.StatusBadRequest)
		return
	}

	_ = SetBalanceDB(workerID, currentBal-amount)
	_ = CreatePayoutRequestDB(workerID, amount)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Payout requested successfully",
	})
}

func (b *Broker) handlePayoutList(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	requests, _ := GetPayoutRequestsDB()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(requests)
}

func (b *Broker) handlePayoutApprove(w http.ResponseWriter, r *http.Request) {
	if setupCORS(w, r) {
		return
	}
	idStr := r.FormValue("id")
	status := r.FormValue("status")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	_ = UpdatePayoutRequestStatusDB(id, status)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}
