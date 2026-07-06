package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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
}

func NewBroker() *Broker {
	b := &Broker{
		workers:   make(map[string]*WorkerConnection),
		taskQueue: make(chan uint64, 1000),
		activeTasks: make(map[uint64]string),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				allowedOrigin := os.Getenv("MESH_ALLOWED_ORIGIN")
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
		if err := json.Unmarshal(msgBytes, &resp); err == nil && resp.TaskID != 0 {
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
			_ = UpdateTaskDB(resp.TaskID, status, resp.Stdout, resp.ReceiptJSON, resp.Error)
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

	wasmFile, _, err := r.FormFile("wasm")
	var wasmBytes []byte
	if err == nil {
		defer wasmFile.Close()
		wasmBytes, _ = io.ReadAll(wasmFile)
	}

	templateID := r.FormValue("template_id")
	if templateID != "" {
		var wasmPath string
		if templateID == "hasher" {
			wasmPath = "cmd/mesh-zero/hasher.wasm"
		} else if templateID == "gpu_task" {
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
		http.Error(w, "Missing wasm task payload", http.StatusBadRequest)
		return
	}

	dataFile, _, err := r.FormFile("data")
	if err != nil {
		http.Error(w, "Missing data input", http.StatusBadRequest)
		return
	}
	defer dataFile.Close()
	dataBytes, _ := io.ReadAll(dataFile)

	targetTier := 0 // 0 means any tier
	if tierStr := r.FormValue("tier"); tierStr != "" {
		fmt.Sscanf(tierStr, "%d", &targetTier)
	}

	taskID := uint64(time.Now().UnixNano())

	// Create task record in pending state in SQLite
	err = CreateTaskDB(taskID, wasmBytes, dataBytes, targetTier)
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
		"task_id": taskID,
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

	// Return status JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task_id":      t.ID,
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
