package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// ComputeReceipt represents a cryptographically verifiable proof of work/compute.
// Workers generate this receipt after executing a task, signing it with their private key.
type ComputeReceipt struct {
	TaskID          uint64  `json:"task_id"`
	WorkerID        string  `json:"worker_id"`
	ExecutionTimeMs float64 `json:"execution_time_ms"`
	PricePerMs      float64 `json:"price_per_ms"`
	TotalPrice      float64 `json:"total_price"`
	Signature       string  `json:"signature"`
}

// GenerateReceipt creates a new signed compute receipt for a given task execution.
func GenerateReceipt(taskID uint64, workerID string, executionTimeMs float64, pricePerMs float64, privKeyHex string) (*ComputeReceipt, error) {
	if privKeyHex == "" {
		return nil, fmt.Errorf("missing private key for receipt signing")
	}

	privKeyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil || len(privKeyBytes) != 64 {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}
	privKey := ed25519.PrivateKey(privKeyBytes)

	totalPrice := executionTimeMs * pricePerMs
	receipt := &ComputeReceipt{
		TaskID:          taskID,
		WorkerID:        workerID,
		ExecutionTimeMs: executionTimeMs,
		PricePerMs:      pricePerMs,
		TotalPrice:      totalPrice,
	}

	// Serialize fields to sign
	dataToSign := []byte(fmt.Sprintf("%d:%s:%f:%f:%f", 
		receipt.TaskID, receipt.WorkerID, receipt.ExecutionTimeMs, receipt.PricePerMs, receipt.TotalPrice))

	sig := ed25519.Sign(privKey, dataToSign)
	receipt.Signature = hex.EncodeToString(sig)

	return receipt, nil
}

// VerifyReceipt checks that a receipt was signed by the worker associated with the public key.
func VerifyReceipt(receipt *ComputeReceipt, pubKeyHex string) (bool, error) {
	if pubKeyHex == "" {
		return false, fmt.Errorf("missing public key for verification")
	}

	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKeyBytes) != 32 {
		return false, fmt.Errorf("invalid public key: %v", err)
	}
	pubKey := ed25519.PublicKey(pubKeyBytes)

	dataToSign := []byte(fmt.Sprintf("%d:%s:%f:%f:%f", 
		receipt.TaskID, receipt.WorkerID, receipt.ExecutionTimeMs, receipt.PricePerMs, receipt.TotalPrice))

	sigBytes, err := hex.DecodeString(receipt.Signature)
	if err != nil {
		return false, fmt.Errorf("invalid signature hex: %v", err)
	}

	isValid := ed25519.Verify(pubKey, dataToSign, sigBytes)
	return isValid, nil
}

// SimulatedBillingLedger tracks the credit balances of different senders/workers in the local node.
type SimulatedBillingLedger struct {
	Balances map[string]float64 `json:"balances"`
}

const LedgerFile = "mesh_ledger.json"

func LoadLedger() (*SimulatedBillingLedger, error) {
	data, err := os.ReadFile(LedgerFile)
	if err != nil {
		// Return empty ledger
		return &SimulatedBillingLedger{Balances: make(map[string]float64)}, nil
	}
	var ledger SimulatedBillingLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return nil, err
	}
	return &ledger, nil
}

func (l *SimulatedBillingLedger) Save() error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(LedgerFile, data, 0644)
}
