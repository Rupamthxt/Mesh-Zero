package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
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

	brokerAddr := os.Getenv("MESH_BROKER_ADDR")
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

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("ERROR: Task execution rejected by broker: %s\n", string(respBytes))
		return
	}

	responseBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("ERROR: Failed to read response: %v\n", err)
		return
	}

	separator := []byte("\n---MZ-RECEIPT---\n")
	sepIdx := bytes.Index(responseBytes, separator)

	var taskStdout []byte
	var receiptJSON []byte

	if sepIdx != -1 {
		taskStdout = responseBytes[:sepIdx]
		receiptJSON = responseBytes[sepIdx+len(separator):]
	} else {
		taskStdout = responseBytes
	}

	// Print the task output
	fmt.Println(" -> Task output received:")
	os.Stdout.Write(taskStdout)

	if len(receiptJSON) > 0 {
		var receipt ComputeReceipt
		// Strip trailing newlines/whitespace
		receiptJSON = bytes.TrimSpace(receiptJSON)
		if err := json.Unmarshal(receiptJSON, &receipt); err == nil {
			fmt.Println("\n\n========================================")
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
				if ledger.Balances[senderID] == 0 {
					ledger.Balances[senderID] = 100.0 // Default starting balance
				}
				ledger.Balances[senderID] -= receipt.TotalPrice
				ledger.Balances[receipt.WorkerID] += receipt.TotalPrice
				ledger.Save()
				fmt.Printf("[BILLING] Local account balance updated. Current: %.6f credits\n", ledger.Balances[senderID])
			}
		} else {
			fmt.Printf("\n[BILLING] Received invalid receipt JSON: %v\n", err)
		}
	}
}
