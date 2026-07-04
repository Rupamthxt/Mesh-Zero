package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func main() {
	// 1. Read whatever arbitrary data the Sender provided
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Printf("Error reading input: %v\n", err)
		return
	}

	// 2. Do heavy CPU work (Cryptographic Hashing)
	hash := sha256.Sum256(input)
	hashString := hex.EncodeToString(hash[:])

	// 3. Return the result to the mesh
	fmt.Printf("[WASM HASHER] Successfully processed %d bytes.\n", len(input))
	fmt.Printf("[WASM HASHER] SHA-256 Result: %s\n", hashString)
}
