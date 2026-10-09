package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// GenerateAPIKey creates a cryptographically secure random API key with a given prefix
func GenerateAPIKey(prefix string) string {
	if prefix == "" {
		prefix = "emdash_live"
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

// ExtractAPIKey extracts the API key from Authorization header, X-API-Key, or query param
func ExtractAPIKey(r *http.Request) string {
	// 1. Authorization: Bearer <key>
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}

	// 2. X-API-Key header
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		return strings.TrimSpace(apiKey)
	}

	// 3. Query parameter ?api_key=<key>
	if apiKey := r.URL.Query().Get("api_key"); apiKey != "" {
		return strings.TrimSpace(apiKey)
	}

	// Fallback to default demo key for playground/trial requests
	return "emdash_trial_demo"
}

// AuthenticateAPIKey validates that the key exists, is active, and has sufficient credits
func AuthenticateAPIKey(r *http.Request) (*APIKeyRecord, error) {
	keyStr := ExtractAPIKey(r)
	if keyStr == "" {
		return nil, fmt.Errorf("missing API key. Provide via 'Authorization: Bearer <key>' or 'X-API-Key'")
	}

	keyRec, err := GetAPIKeyDB(keyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid or unrecognized API key: %s", keyStr)
	}

	if !keyRec.IsActive {
		return nil, fmt.Errorf("API key %s has been deactivated", keyStr)
	}

	if keyRec.Credits < 1.0 {
		return nil, fmt.Errorf("insufficient credits on API key %s (balance: %.2f credits). Please top up", keyStr, keyRec.Credits)
	}

	return keyRec, nil
}
