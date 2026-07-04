package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tetratelabs/wazero"
)

type ExtensionHook func(context.Context, wazero.Runtime) error

var DefaultHooks []ExtensionHook

type Worker struct {
	ID         string
	PricePerMs float64
	Hooks      []ExtensionHook
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
	w.ID = "worker-" + strconv.FormatInt(time.Now().UnixNano(), 36)

	fmt.Println("========================================")
	fmt.Println(" MESH-ZERO LIGHTWEIGHT WORKER INITIALIZED")
	fmt.Printf("  Worker ID: %s\n", w.ID)
	fmt.Printf("  GPU Accel: %v | Max RAM: %d MB\n", currentNodeCapabilities.HasGPU, currentNodeCapabilities.MaxRAm)
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

			// 1. Register with broker
			reg := map[string]interface{}{
				"id":           w.ID,
				"price_per_ms": w.PricePerMs,
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
				duration, execErr := executeWasm(ctx, dispatch.WasmBytes, dispatch.DataBytes, w.Hooks, &outBuf)

				var errStr string
				if execErr != nil {
					errStr = execErr.Error()
				}

				// Generate receipt
				privKeyHex := os.Getenv("MESH_PRIV_KEY")
				if privKeyHex == "" {
					privKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
				}

				execTimeMs := float64(duration.Nanoseconds()) / 1e6
				var receiptJSON []byte
				receipt, err := GenerateReceipt(dispatch.TaskID, w.ID, execTimeMs, w.PricePerMs, privKeyHex)
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
