package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// RunExtract submits an extraction job from the CLI to the Central Broker
func RunExtract(targetURL, country, scriptPath string) {
	brokerAddr := os.Getenv("EMDASH_BROKER_ADDR")
	if brokerAddr == "" {
		brokerAddr = "localhost:8080"
	}

	var customScript string
	if scriptPath != "" {
		data, err := os.ReadFile(scriptPath)
		if err != nil {
			fmt.Printf("ERROR: Could not read script file: %v\n", err)
			return
		}
		customScript = string(data)
	}

	fmt.Printf("[CLIENT] Requesting residential extraction for: %s\n", targetURL)
	if country != "" {
		fmt.Printf("[CLIENT] Target Country Preference: %s\n", country)
	}

	payload := map[string]interface{}{
		"url":            targetURL,
		"country":        country,
		"extract_script": customScript,
	}
	bodyBytes, _ := json.Marshal(payload)

	reqURL := fmt.Sprintf("http://%s/api/extract", brokerAddr)
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		fmt.Printf("FATAL: Failed to build request: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 35 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("FATAL: Failed to contact Central Broker: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		errBytes, _ := io.ReadAll(resp.Body)
		fmt.Printf("ERROR: Submission rejected: %s\n", string(errBytes))
		return
	}

	var submitResp struct {
		TaskID string `json:"task_id"`
		Status string `json:"status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&submitResp)

	fmt.Printf("[CLIENT] Job queued (Task ID: %s). Waiting for residential node execution...\n", submitResp.TaskID)

	// Poll status
	statusURL := fmt.Sprintf("http://%s/api/extract/status?id=%s", brokerAddr, submitResp.TaskID)
	for {
		time.Sleep(1 * time.Second)

		sResp, err := http.Get(statusURL)
		if err != nil {
			continue
		}

		var s struct {
			Status      string          `json:"status"`
			StatusCode  int             `json:"status_code"`
			LatencyMs   int64           `json:"latency_ms"`
			Data        json.RawMessage `json:"data"`
			ReceiptJSON string          `json:"receipt_json"`
			Error       string          `json:"error"`
		}

		if err := json.NewDecoder(sResp.Body).Decode(&s); err != nil {
			sResp.Body.Close()
			continue
		}
		sResp.Body.Close()

		if s.Status == "completed" {
			fmt.Println("\n==================================================")
			fmt.Println("       EXTRACTION SUCCESSFUL (RESIDENTIAL NODE)")
			fmt.Printf("  HTTP Status: %d\n", s.StatusCode)
			fmt.Printf("  Latency:     %d ms\n", s.LatencyMs)
			fmt.Println("==================================================")

			var prettyJSON bytes.Buffer
			if err := json.Indent(&prettyJSON, s.Data, "", "  "); err == nil {
				fmt.Println(prettyJSON.String())
			} else {
				fmt.Println(string(s.Data))
			}

			if s.ReceiptJSON != "" {
				var r ExtractionReceipt
				if err := json.Unmarshal([]byte(s.ReceiptJSON), &r); err == nil {
					fmt.Println("\n[RECEIPT] Cryptographically Signed Extraction Receipt:")
					fmt.Printf("  Worker Peer: %s...\n", r.WorkerID[:16])
					fmt.Printf("  Signature:   %s...\n", r.Signature[:24])
				}
			}
			return
		} else if s.Status == "failed" {
			fmt.Printf("\nERROR: Extraction failed: %s\n", s.Error)
			return
		}
	}
}
