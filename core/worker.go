package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Worker struct {
	ID            string
	PricePerTask  float64
	PrivateKeyHex string
	Geo           NodeGeoInfo
	RateLimiter   *RateLimiter
}

type NodeCapabilities struct {
	MaxRAm uint64
}

var currentNodeCapabilities = NodeCapabilities{
	MaxRAm: 512 * 1024 * 1024,
}

type TaskDispatch struct {
	TaskID        uint64 `json:"task_id"`
	URL           string `json:"url"`
	ExtractScript string `json:"extract_script,omitempty"`
	TimeoutSec    int    `json:"timeout_sec,omitempty"`
}

type TaskResponse struct {
	TaskID      uint64 `json:"task_id"`
	StatusCode  int    `json:"status_code"`
	LatencyMs   int64  `json:"latency_ms"`
	ResultJSON  []byte `json:"result_json"`
	ReceiptJSON []byte `json:"receipt_json"`
	Error       string `json:"error,omitempty"`
}

func (w *Worker) Start(ctx context.Context, enableApi bool, apiPort string) error {
	privKeyHex, err := ensureLocalKeyPair()
	if err != nil {
		return fmt.Errorf("failed to load/generate worker key: %v", err)
	}
	w.PrivateKeyHex = privKeyHex

	privBytes, _ := hex.DecodeString(w.PrivateKeyHex)
	privKey := ed25519.PrivateKey(privBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)
	w.ID = hex.EncodeToString(pubKey)

	// Discover host public network characteristics and geolocation
	fmt.Println("[NETWORK] Discovering residential host identity & geolocation...")
	w.Geo = DiscoverNodeGeo()
	w.RateLimiter = NewRateLimiter(12) // Default max 12 requests/min to protect host bandwidth

	if w.PricePerTask <= 0 {
		w.PricePerTask = 1.0 // 1 credit per extraction by default
	}

	fmt.Println("==================================================")
	fmt.Println("     EMDASH RESIDENTIAL EXTRACTION NODE")
	fmt.Printf("  Node ID:     %s\n", w.ID[:16]+"...")
	fmt.Printf("  Location:    %s, %s (%s)\n", w.Geo.City, w.Geo.Country, w.Geo.CountryCode)
	fmt.Printf("  Public IP:   %s via %s\n", w.Geo.IP, w.Geo.ISP)
	fmt.Printf("  Rate Limit:  Max 12 requests/min (Host Protection Active)\n")
	fmt.Printf("  Pricing:     %.2f credits/page\n", w.PricePerTask)
	fmt.Println("==================================================")

	brokerAddr := os.Getenv("EMDASH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "ws://localhost:8080"
	}

	if strings.HasPrefix(brokerAddr, "http://") {
		brokerAddr = "ws://" + strings.TrimPrefix(brokerAddr, "http://")
	} else if strings.HasPrefix(brokerAddr, "https://") {
		brokerAddr = "wss://" + strings.TrimPrefix(brokerAddr, "https://")
	}
	if !strings.HasPrefix(brokerAddr, "ws://") && !strings.HasPrefix(brokerAddr, "wss://") {
		if strings.HasPrefix(brokerAddr, "localhost") || strings.Contains(brokerAddr, "127.0.0.1") || strings.Contains(brokerAddr, ":") {
			brokerAddr = "ws://" + brokerAddr
		} else {
			brokerAddr = "wss://" + brokerAddr
		}
	}

	u, err := url.Parse(brokerAddr)
	if err != nil {
		u = &url.URL{Scheme: "ws", Host: brokerAddr}
	}
	u.Path = "/ws/worker"
	fmt.Printf("[WORKER] Connecting to Central Gateway at %s...\n", u.String())

	// Establish connection loop with retry backoff
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			header := make(http.Header)
			header.Set("ngrok-skip-browser-warning", "true")

			conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
			if err != nil {
				fmt.Printf("[WORKER] Connection failed: %v. Retrying in 5 seconds...\n", err)
				time.Sleep(5 * time.Second)
				continue
			}

			// 1. Register with broker with geolocation and capabilities
			reg := map[string]interface{}{
				"id":             w.ID,
				"ip":             w.Geo.IP,
				"country":        w.Geo.Country,
				"country_code":   w.Geo.CountryCode,
				"city":           w.Geo.City,
				"isp":            w.Geo.ISP,
				"price_per_task": w.PricePerTask,
			}
			regBytes, _ := json.Marshal(reg)
			_ = conn.WriteMessage(websocket.TextMessage, regBytes)

			fmt.Println("[WORKER] Successfully connected and registered with Central Broker!")

			// 2. Loop and process incoming extraction tasks
			for {
				_, message, err := conn.ReadMessage()
				if err != nil {
					fmt.Printf("[WORKER] Broker connection lost: %v\n", err)
					break
				}

				var dispatch TaskDispatch
				if err := json.Unmarshal(message, &dispatch); err != nil {
					continue
				}

				fmt.Printf("[WORKER] Processing Extraction Job #%d -> %s\n", dispatch.TaskID, dispatch.URL)

				// Rate limiting host check
				if !w.RateLimiter.Allow() {
					fmt.Println("[WORKER] Rate limit exceeded; rejecting task to protect host")
					resp := TaskResponse{
						TaskID: dispatch.TaskID,
						Error:  "Worker host rate limit reached; job throttled for safety",
					}
					respMsg, _ := json.Marshal(resp)
					_ = conn.WriteMessage(websocket.TextMessage, respMsg)
					continue
				}

				// Fetch URL natively with residential browser headers
				fetchRes, fetchErr := FetchResidentialURL(ctx, dispatch.URL, dispatch.TimeoutSec)
				if fetchErr != nil {
					fmt.Printf("[WORKER] Fetch failed: %v\n", fetchErr)
					resp := TaskResponse{
						TaskID: dispatch.TaskID,
						Error:  fmt.Sprintf("Residential fetch failed: %v", fetchErr),
					}
					respMsg, _ := json.Marshal(resp)
					_ = conn.WriteMessage(websocket.TextMessage, respMsg)
					continue
				}

				// Execute sandboxed extraction script inside QuickJS WASI
				preparedJS := PrepareExtractionScript(dispatch.URL, fetchRes.Body, fetchRes.StatusCode, dispatch.ExtractScript)
				extractedJSON, execErr := ExecuteExtractionSandbox(ctx, preparedJS)

				var errStr string
				if execErr != nil {
					errStr = execErr.Error()
					fmt.Printf("[WORKER] Extraction error: %v\n", execErr)
				} else {
					fmt.Printf("[WORKER] Extraction successful (%d bytes JSON extracted in %dms)\n", len(extractedJSON), fetchRes.LatencyMs)
				}

				// Generate signed receipt
				var receiptJSON []byte
				receipt, err := GenerateExtractionReceipt(dispatch.TaskID, w.ID, dispatch.URL, fetchRes.StatusCode, len(extractedJSON), w.PricePerTask, w.PrivateKeyHex)
				if err == nil {
					receiptJSON, _ = json.Marshal(receipt)
				}

				// Send result back to Broker
				resp := TaskResponse{
					TaskID:      dispatch.TaskID,
					StatusCode:  fetchRes.StatusCode,
					LatencyMs:   fetchRes.LatencyMs,
					ResultJSON:  extractedJSON,
					ReceiptJSON: receiptJSON,
					Error:       errStr,
				}

				respMsg, _ := json.Marshal(resp)
				_ = conn.WriteMessage(websocket.TextMessage, respMsg)
			}

			time.Sleep(2 * time.Second)
		}
	}()

	if enableApi {
		go w.StartAPIServer(apiPort)
	}

	<-ctx.Done()
	return nil
}

func ensureLocalKeyPair() (string, error) {
	keyFile := "emdash_worker.key"

	data, err := os.ReadFile(keyFile)
	if err == nil {
		privKeyHex := string(bytes.TrimSpace(data))
		if len(privKeyHex) == 128 {
			return privKeyHex, nil
		}
	}

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", err
	}
	_ = pub

	privHex := hex.EncodeToString(priv)
	err = os.WriteFile(keyFile, []byte(privHex), 0600)
	if err != nil {
		return "", err
	}

	fmt.Println("[SECURITY] New worker cryptographic keypair auto-generated and saved locally.")
	return privHex, nil
}
