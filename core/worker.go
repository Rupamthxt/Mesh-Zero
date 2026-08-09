package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	quickjswasi "github.com/paralin/go-quickjs-wasi"
	"github.com/tetratelabs/wazero"
)

type ExtensionHook func(context.Context, wazero.Runtime) error

var DefaultHooks []ExtensionHook

type Worker struct {
	ID            string
	PricePerMs    float64
	PrivateKeyHex string
	Hooks         []ExtensionHook
}

type NodeCapabilities struct {
	HasGPU       bool
	MaxRAm       uint64
	IsMobile     bool
	SupportsWASI bool
}

var currentNodeCapabilities = NodeCapabilities{
	HasGPU:       false,
	MaxRAm:       512 * 1024 * 1024, // 512MB RAM limit for tasks
	IsMobile:     false,
	SupportsWASI: true,
}



func (w *Worker) Start(ctx context.Context, enableApi bool, apiPort string) error {
	// Parse dynamic memory limit from environment variable (in Megabytes)
	if ramStr := os.Getenv("MESH_MAX_RAM"); ramStr != "" {
		if ramMB, err := strconv.Atoi(ramStr); err == nil && ramMB > 0 {
			currentNodeCapabilities.MaxRAm = uint64(ramMB) * 1024 * 1024
		}
	}

	privKeyHex, err := ensureLocalKeyPair()
	if err != nil {
		return fmt.Errorf("failed to load/generate worker key: %v", err)
	}
	w.PrivateKeyHex = privKeyHex

	privBytes, _ := hex.DecodeString(w.PrivateKeyHex)
	privKey := ed25519.PrivateKey(privBytes)
	pubKey := privKey.Public().(ed25519.PublicKey)
	w.ID = hex.EncodeToString(pubKey)

	fmt.Println("========================================")
	fmt.Println(" MESH-ZERO LIGHTWEIGHT WORKER INITIALIZED")
	fmt.Printf("  Worker ID: %s\n", w.ID)
	fmt.Printf("  GPU Accel: %v | Max RAM: %d MB\n", currentNodeCapabilities.HasGPU, currentNodeCapabilities.MaxRAm / (1024 * 1024))
	fmt.Printf("  Pricing:   %.4f credits/ms\n", w.PricePerMs)
	fmt.Println("========================================")

	brokerAddr := os.Getenv("MESH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "ws://localhost:8080"
	}

	// Sanitize common http/https copy-paste prefixes to ws/wss equivalents
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
	fmt.Printf("[WORKER] Connecting to Broker Gateway at %s...\n", u.String())

	// Establish connection loop with retry backoff
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Inject ngrok bypass header to prevent browser warning redirects from blocking handshakes
			header := make(http.Header)
			header.Set("ngrok-skip-browser-warning", "true")

			conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
			if err != nil {
				fmt.Printf("[WORKER] Connection failed: %v. Retrying in 5 seconds...\n", err)
				time.Sleep(5 * time.Second)
				continue
			}

			// Determine hardware tier: 1 = GPU/High-end, 2 = Desktop (Standard), 3 = Mobile/IoT
			tier := 2
			if currentNodeCapabilities.HasGPU {
				tier = 1
			} else if currentNodeCapabilities.IsMobile {
				tier = 3
			}

			// 1. Register with broker
			reg := map[string]interface{}{
				"id":           w.ID,
				"price_per_ms": w.PricePerMs,
				"tier":         tier,
			}
			regBytes, _ := json.Marshal(reg)
			_ = conn.WriteMessage(websocket.TextMessage, regBytes)

			fmt.Println("[WORKER] Successfully connected and registered with Central Broker!")

			// 2. Loop and read tasks
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

				fmt.Printf("[WORKER] Executing Task %d via Broker...\n", dispatch.TaskID)
				
				// Execute WASM sandbox in memory
				var outBuf bytes.Buffer
				var wasmArgs []string
				var executionData = dispatch.DataBytes

				if bytes.Equal(dispatch.WasmBytes, quickjswasi.QuickJSWASM) {
					jsCode := string(dispatch.DataBytes)
					
					// Detect if this is a residential web scraping request
					reInput := regexp.MustCompile(`const\s+__INPUT__\s*=\s*["']([^"']+)["'];`)
					match := reInput.FindStringSubmatch(jsCode)
					if len(match) > 1 {
						targetURL := match[1]
						if strings.HasPrefix(targetURL, "http://") || strings.HasPrefix(targetURL, "https://") {
							fmt.Printf("[WORKER] Residential Scraper: Pre-fetching URL: %s\n", targetURL)
							
							// Fetch the content natively on the host using worker's residential IP
							fetchedBody, statusCode, isOk, fetchErr := fetchURLFromHost(targetURL)
							
							var errStr string
							if fetchErr != nil {
								errStr = fetchErr.Error()
								fmt.Printf("[WORKER] Pre-fetch failed: %v\n", fetchErr)
							} else {
								fmt.Printf("[WORKER] Pre-fetch complete. Size: %d bytes | Status: %d\n", len(fetchedBody), statusCode)
							}
							
							escapedBody, _ := json.Marshal(fetchedBody)
							escapedErr, _ := json.Marshal(errStr)
							
							// Prepend the fetch polyfill to override globalThis.fetch inside QuickJS
							polyfill := fmt.Sprintf(`
globalThis.fetch = function(url) {
	if (url === __INPUT__) {
		const errStr = %s;
		if (errStr) {
			return Promise.reject(new Error(errStr));
		}
		return Promise.resolve({
			ok: %t,
			status: %d,
			text: () => Promise.resolve(%s),
			json: () => {
				try {
					return Promise.resolve(JSON.parse(%s));
				} catch (e) {
					return Promise.reject(e);
				}
			}
		});
	}
	return Promise.reject(new Error("MeshØ Sandbox error: fetch is only allowed for the assigned __INPUT__ URL."));
};
`, escapedErr, isOk, statusCode, string(escapedBody), string(escapedBody))
							
							// Prepend polyfill after the __INPUT__ definition
							newLineIdx := strings.Index(jsCode, "\n")
							if newLineIdx != -1 {
								jsCode = jsCode[:newLineIdx+1] + polyfill + jsCode[newLineIdx+1:]
							} else {
								jsCode = polyfill + jsCode
							}
							
							executionData = []byte(jsCode)
						}
					}
					wasmArgs = []string{"qjs", "-e", string(executionData)}
				}
				duration, execErr := executeWasm(ctx, dispatch.WasmBytes, executionData, w.Hooks, &outBuf, wasmArgs)

				var errStr string
				if execErr != nil {
					errStr = execErr.Error()
				}

				// Generate receipt using auto-initialized worker key
				execTimeMs := float64(duration.Nanoseconds()) / 1e6
				var receiptJSON []byte
				receipt, err := GenerateReceipt(dispatch.TaskID, w.ID, execTimeMs, w.PricePerMs, w.PrivateKeyHex)
				if err == nil {
					receiptJSON, _ = json.Marshal(receipt)
				}

				// Send result back to Broker
				resp := TaskResponse{
					TaskID:      dispatch.TaskID,
					Stdout:      outBuf.Bytes(),
					ReceiptJSON: receiptJSON,
					Error:       errStr,
				}

				respMsg, _ := json.Marshal(resp)
				_ = conn.WriteMessage(websocket.TextMessage, respMsg)
				fmt.Printf("[WORKER] Finished Task %d. Duration: %.2fms | Cost: %.6f credits\n", dispatch.TaskID, execTimeMs, receipt.TotalPrice)
			}

			time.Sleep(2 * time.Second)
		}
	}()

	// Start local API server/dashboard if enabled
	if enableApi {
		go w.StartAPIServer(apiPort)
	}

	<-ctx.Done()
	return nil
}

func ensureLocalKeyPair() (string, error) {
	keyFile := "mesh_worker.key"

	// Try to read existing key
	data, err := os.ReadFile(keyFile)
	if err == nil {
		privKeyHex := string(bytes.TrimSpace(data))
		if len(privKeyHex) == 128 { // 64 bytes in hex is 128 characters
			return privKeyHex, nil
		}
	}

	// Generate new keypair
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

// fetchURLFromHost executes a standard HTTP GET request with customized headers
// to simulate a normal residential browser request.
func fetchURLFromHost(targetURL string) (string, int, bool, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return "", 0, false, err
	}
	
	// Add dynamic headers to look like a standard desktop browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Connection", "keep-alive")
	
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false, err
	}
	defer resp.Body.Close()
	
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, resp.StatusCode >= 200 && resp.StatusCode < 300, err
	}
	
	return string(bodyBytes), resp.StatusCode, resp.StatusCode >= 200 && resp.StatusCode < 300, nil
}
