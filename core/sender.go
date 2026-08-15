package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"
)

func RunSender(wasmPath, inputPath string, maxPrice float64) {
	// Read input files
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		fmt.Printf("FATAL: Could not read WASM file: %v\n", err)
		return
	}

	dataBytes, err := os.ReadFile(inputPath)
	if err != nil {
		fmt.Printf("FATAL: Could not read data file: %v\n", err)
		return
	}

	brokerAddr := os.Getenv("EMDASH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "localhost:8080"
	}

	fmt.Printf("[CLIENT] Sending task payload to Central Broker at %s...\n", brokerAddr)

	// Create multipart request
	bodyBuf := &bytes.Buffer{}
	mw := multipart.NewWriter(bodyBuf)

	// Attach data
	dataPart, err := mw.CreateFormFile("data", "data.txt")
	if err != nil {
		fmt.Printf("FATAL: Failed to package data: %v\n", err)
		return
	}
	dataPart.Write(dataBytes)

	// Attach wasm
	wasmPart, err := mw.CreateFormFile("wasm", "task.wasm")
	if err != nil {
		fmt.Printf("FATAL: Failed to package WASM: %v\n", err)
		return
	}
	wasmPart.Write(wasmBytes)

	// Attach budget constraint
	mw.WriteField("max_price", fmt.Sprintf("%f", maxPrice))

	mw.Close()

	brokerURL := fmt.Sprintf("http://%s/api/tasks/submit", brokerAddr)
	req, err := http.NewRequest("POST", brokerURL, bodyBuf)
	if err != nil {
		fmt.Printf("FATAL: Failed to build request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("FATAL: Failed to connect to Broker: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		respBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("ERROR: Task submission rejected by broker: %s\n", string(respBytes))
		return
	}

	var submitResp struct {
		TaskID uint64 `json:"task_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		fmt.Printf("ERROR: Failed to parse submission response: %v\n", err)
		return
	}

	fmt.Printf("[CLIENT] Task queued. TaskID: %d. Waiting for execution...\n", submitResp.TaskID)

	// Start polling the status endpoint
	statusURL := fmt.Sprintf("http://%s/api/tasks/status?id=%d", brokerAddr, submitResp.TaskID)
	var taskStdout string
	var receiptJSON string
	var taskErr string

	for {
		time.Sleep(500 * time.Millisecond)

		statusResp, err := http.Get(statusURL)
		if err != nil {
			fmt.Printf("ERROR: Failed to poll task status: %v\n", err)
			return
		}

		var s struct {
			Status      string `json:"status"`
			Stdout      string `json:"stdout"`
			ReceiptJSON string `json:"receipt_json"`
			Error       string `json:"error"`
		}

		if err := json.NewDecoder(statusResp.Body).Decode(&s); err != nil {
			statusResp.Body.Close()
			continue
		}
		statusResp.Body.Close()

		if s.Status == "completed" || s.Status == "failed" {
			taskStdout = s.Stdout
			receiptJSON = s.ReceiptJSON
			taskErr = s.Error
			break
		}
	}

	if taskErr != "" {
		fmt.Printf("ERROR: Task execution failed: %s\n", taskErr)
		return
	}

	// Print the task output
	fmt.Println(" -> Task output received:")
	fmt.Print(taskStdout)

	if len(receiptJSON) > 0 {
		var receipt ComputeReceipt
		err := json.Unmarshal([]byte(receiptJSON), &receipt)
		if err == nil {
			// Verify signature locally
			isValid, verifyErr := VerifyReceipt(&receipt, receipt.WorkerID)
			if verifyErr == nil && isValid {
				fmt.Println("\n========================================")
				fmt.Println(" COMPUTE RECEIPT RECEIVED & VERIFIED")
				fmt.Printf("  Task ID:         %d\n", receipt.TaskID)
				fmt.Printf("  Worker Peer ID:  %s\n", receipt.WorkerID)
				fmt.Printf("  Execution Time:  %.2f ms\n", receipt.ExecutionTimeMs)
				fmt.Printf("  Rate:            %.4f credits/ms\n", receipt.PricePerMs)
				fmt.Printf("  Cost Charged:    %.6f credits\n", receipt.TotalPrice)
				fmt.Printf("  Signature:       %s...\n", receipt.Signature[:16])
				fmt.Println("========================================")

				// Process billing locally (simulated)
				ledger, err := LoadLedger()
				if err == nil {
					// Deduct from sender, add to worker
					senderID := "local-client"
					senderBal := ledger.GetBalance(senderID)
					if senderBal == 0 {
						senderBal = 100.0 // Default starting balance
					}
					ledger.SetBalance(senderID, senderBal-receipt.TotalPrice)
					
					workerBal := ledger.GetBalance(receipt.WorkerID)
					ledger.SetBalance(receipt.WorkerID, workerBal+receipt.TotalPrice)
					
					ledger.Save()
					fmt.Printf("[BILLING] Local account balance updated. Current: %.6f credits\n", ledger.GetBalance(senderID))
				}
			} else {
				fmt.Printf("\n[BILLING] Cryptographic Verification Failure: Mismatched worker signature.\n")
			}
		} else {
			fmt.Printf("\n[BILLING] Received invalid receipt JSON: %v\n", err)
		}
	}
}
