package core

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
)

//go:embed dashboard.html
var dashboardHTML []byte

func (w *Worker) StartAPIServer(port string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/peers", w.handleGetPeers)
	mux.HandleFunc("/api/execute", w.handleExecuteTask)

	mux.HandleFunc("/", func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html")
		res.Write(dashboardHTML)
	})

	handler := corsMiddleware(mux)

	fmt.Printf("[API] Local Gateway listening on http://localhost:%s\n", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		fmt.Printf("[API] Fatal server error: %v\n", err)
	}
}

func (w *Worker) handleGetPeers(res http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(res, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	brokerAddr := os.Getenv("MESH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "localhost:8080"
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/api/workers", brokerAddr))
	if err != nil {
		http.Error(res, "Failed to contact broker server", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var workers []map[string]interface{}
	if err := json.Unmarshal(body, &workers); err != nil {
		http.Error(res, "Invalid broker response", http.StatusInternalServerError)
		return
	}

	var peerIDs []string
	for _, wk := range workers {
		if id, ok := wk["id"].(string); ok && id != w.ID {
			peerIDs = append(peerIDs, id)
		}
	}

	res.Header().Set("Content-Type", "application/json")
	json.NewEncoder(res).Encode(map[string]interface{}{
		"node_id": w.ID,
		"peers":   peerIDs,
		"count":   len(peerIDs),
	})
}

func (w *Worker) handleExecuteTask(res http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(res, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req.ParseMultipartForm(10 << 20)

	var wasmBytes []byte
	templateID := req.FormValue("template_id")
	if templateID != "" {
		var wasmPath string
		if templateID == "hasher" {
			wasmPath = "cmd/mesh-zero/hasher.wasm"
		} else if templateID == "gpu_task" {
			wasmPath = "task/gpu_task.wasm"
		} else {
			http.Error(res, "Unknown template ID", http.StatusBadRequest)
			return
		}
		var err error
		wasmBytes, err = os.ReadFile(wasmPath)
		if err != nil {
			wasmBytes, err = os.ReadFile("../" + wasmPath)
			if err != nil {
				wasmBytes, err = os.ReadFile("../../" + wasmPath)
				if err != nil {
					http.Error(res, fmt.Sprintf("Template file not found: %v", err), http.StatusInternalServerError)
					return
				}
			}
		}
	} else {
		wasmFile, _, err := req.FormFile("wasm")
		if err != nil {
			http.Error(res, "Missing 'wasm' file", http.StatusBadRequest)
			return
		}
		defer wasmFile.Close()
		wasmBytes, _ = io.ReadAll(wasmFile)
	}

	dataFile, _, err := req.FormFile("data")
	if err != nil {
		http.Error(res, "Missing 'data' file", http.StatusBadRequest)
		return
	}
	defer dataFile.Close()
	dataBytes, _ := io.ReadAll(dataFile)

	brokerAddr := os.Getenv("MESH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "localhost:8080"
	}

	bodyBuf := &bytes.Buffer{}
	mw := multipart.NewWriter(bodyBuf)

	// Add data field
	dataPart, _ := mw.CreateFormFile("data", "data.txt")
	dataPart.Write(dataBytes)

	// Add wasm or template ID
	if templateID != "" {
		mw.WriteField("template_id", templateID)
	} else {
		wasmPart, _ := mw.CreateFormFile("wasm", "task.wasm")
		wasmPart.Write(wasmBytes)
	}

	mw.Close()

	brokerURL := fmt.Sprintf("http://%s/api/tasks/submit", brokerAddr)
	brokerReq, err := http.NewRequest("POST", brokerURL, bodyBuf)
	if err != nil {
		http.Error(res, "Failed to build broker request", http.StatusInternalServerError)
		return
	}
	brokerReq.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{}
	brokerResp, err := client.Do(brokerReq)
	if err != nil {
		http.Error(res, "Broker connection error", http.StatusInternalServerError)
		return
	}
	defer brokerResp.Body.Close()

	res.WriteHeader(brokerResp.StatusCode)
	io.Copy(res, brokerResp.Body)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
