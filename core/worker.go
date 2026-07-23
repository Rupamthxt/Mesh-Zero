package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
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
		brokerAddr = "localhost:8080" // default fallback
	}

	u := url.URL{Scheme: "ws", Host: brokerAddr, Path: "/ws/worker"}
	fmt.Printf("[WORKER] Connecting to Broker Gateway at %s...\n", u.String())

	// Establish connection loop with retry backoff
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
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
				if bytes.Equal(dispatch.WasmBytes, quickjswasi.QuickJSWASM) {
					wasmArgs = []string{"qjs", "-e", string(dispatch.DataBytes)}
				}
				duration, execErr := executeWasm(ctx, dispatch.WasmBytes, dispatch.DataBytes, w.Hooks, &outBuf, wasmArgs)

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
