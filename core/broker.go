package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	workers   map[string]*WorkerConnection
	workersMu sync.RWMutex
	pending   map[uint64]chan *TaskResponse
	pendingMu sync.Mutex
	upgrader  websocket.Upgrader
}

func NewBroker() *Broker {
	return &Broker{
		workers: make(map[string]*WorkerConnection),
		pending: make(map[uint64]chan *TaskResponse),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
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
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", b.handleWorkerWS)
	mux.HandleFunc("/api/tasks/submit", b.handleTaskSubmit)
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
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var resp TaskResponse
		if err := json.Unmarshal(msgBytes, &resp); err == nil && resp.TaskID != 0 {
			b.pendingMu.Lock()
			ch, ok := b.pending[resp.TaskID]
			b.pendingMu.Unlock()
			if ok {
				ch <- &resp
			}
		}
	}
}

func (b *Broker) handleTaskSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Parse multipart
	r.ParseMultipartForm(10 << 20)

	wasmFile, _, err := r.FormFile("wasm")
	var wasmBytes []byte
	if err == nil {
		defer wasmFile.Close()
		wasmBytes, _ = io.ReadAll(wasmFile)
	}

	templateID := r.FormValue("template_id")
	if templateID != "" {
		// For prototype, we'll try load locally in broker
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

	maxPrice := 0.0
	if mpStr := r.FormValue("max_price"); mpStr != "" {
		fmt.Sscanf(mpStr, "%f", &maxPrice)
	}

	// 1. Find an idle worker
	b.workersMu.Lock()
	var targetWorker *WorkerConnection
	targetTier := 0 // 0 means any tier
	if tierStr := r.FormValue("tier"); tierStr != "" {
		fmt.Sscanf(tierStr, "%d", &targetTier)
	}

	for _, worker := range b.workers {
		if !worker.Busy && (targetTier == 0 || worker.Tier == targetTier) && (maxPrice == 0 || worker.PricePerMs <= maxPrice) {
			targetWorker = worker
			worker.Busy = true
			break
		}
	}
	b.workersMu.Unlock()

	if targetWorker == nil {
		http.Error(w, "No suitable idle workers available matching budget", http.StatusServiceUnavailable)
		return
	}

	defer func() {
		b.workersMu.Lock()
		if w, ok := b.workers[targetWorker.ID]; ok {
			w.Busy = false
		}
		b.workersMu.Unlock()
	}()

	taskID := uint64(time.Now().UnixNano())
	
	// Register pending task channel before dispatching
	ch := make(chan *TaskResponse, 1)
	b.pendingMu.Lock()
	b.pending[taskID] = ch
	b.pendingMu.Unlock()

	defer func() {
		b.pendingMu.Lock()
		delete(b.pending, taskID)
		b.pendingMu.Unlock()
	}()

	// 2. Dispatch job via WebSocket
	dispatch := TaskDispatch{
		TaskID:    taskID,
		WasmBytes: wasmBytes,
		DataBytes: dataBytes,
	}

	dispatchMsg, _ := json.Marshal(dispatch)
	err = targetWorker.Conn.WriteMessage(websocket.TextMessage, dispatchMsg)
	if err != nil {
		http.Error(w, "Failed to dispatch task to worker", http.StatusInternalServerError)
		return
	}

	// 3. Wait for the worker response via channel with timeout
	var resp *TaskResponse
	select {
	case resp = <-ch:
	case <-time.After(15 * time.Second):
		http.Error(w, "Task execution timed out", http.StatusGatewayTimeout)
		return
	}

	if resp.Error != "" {
		http.Error(w, fmt.Sprintf("Execution error: %s", resp.Error), http.StatusInternalServerError)
		return
	}

	// Return output + trailer receipt
	w.Header().Set("Content-Type", "text/plain")
	w.Write(resp.Stdout)
	if len(resp.ReceiptJSON) > 0 {
		fmt.Fprintf(w, "\n---MZ-RECEIPT---\n%s\n", string(resp.ReceiptJSON))
	}
}

func (b *Broker) handleGetWorkers(w http.ResponseWriter, r *http.Request) {
	b.workersMu.RLock()
	var list []map[string]interface{}
	for _, worker := range b.workers {
		list = append(list, map[string]interface{}{
			"id":           worker.ID,
			"price_per_ms": worker.PricePerMs,
			"busy":         worker.Busy,
		})
	}
	b.workersMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}
