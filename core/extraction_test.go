package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSafetyValidator(t *testing.T) {
	// Prohibited URLs
	invalidURLs := []string{
		"http://localhost:8080/secret",
		"http://127.0.0.1/admin",
		"http://192.168.1.1/setup",
		"http://10.0.0.5/api",
		"ftp://example.com/file",
		"http://paypal.com/login",
		"https://chase.com/auth",
	}

	for _, u := range invalidURLs {
		if err := ValidateTargetURL(u); err == nil {
			t.Errorf("Expected URL %q to be blocked by safety policy, but it passed", u)
		}
	}

	// Permitted public URLs
	validURLs := []string{
		"https://news.ycombinator.com",
		"https://example.com",
		"https://en.wikipedia.org/wiki/WebAssembly",
	}

	for _, u := range validURLs {
		if err := ValidateTargetURL(u); err != nil {
			t.Errorf("Expected URL %q to be allowed, but got error: %v", u, err)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(3) // Max 3 per minute

	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("Request %d should have been permitted", i+1)
		}
	}

	// 4th request must be rejected
	if rl.Allow() {
		t.Errorf("4th request should have been rejected by rate limiter")
	}
}

func TestExtractionSandbox(t *testing.T) {
	sampleHTML := `<!DOCTYPE html>
<html>
<head>
    <title>Show HN: My New Startup</title>
    <meta name="description" content="A distributed residential extraction network.">
    <meta property="og:title" content="Show HN: My New Startup">
</head>
<body>
    <h1>Show HN: My New Startup</h1>
    <p>We extract structured data for AI agents safely.</p>
    <a href="https://example.com/docs">Documentation</a>
    <a href="https://example.com/pricing">Pricing</a>
</body>
</html>`

	// 1. Test Default Intelligent Extractor
	jsCode := PrepareExtractionScript("https://example.com/startup", sampleHTML, 200, "default")
	outBytes, err := ExecuteExtractionSandbox(context.Background(), jsCode)
	if err != nil {
		t.Fatalf("Sandbox execution error: %v", err)
	}

	var res map[string]interface{}
	if err := json.Unmarshal(outBytes, &res); err != nil {
		t.Fatalf("Failed to parse sandbox JSON output: %v. Raw output: %s", err, string(outBytes))
	}

	if title, ok := res["title"].(string); !ok || title != "Show HN: My New Startup" {
		t.Errorf("Expected title 'Show HN: My New Startup', got: %v", res["title"])
	}

	if desc, ok := res["description"].(string); !ok || desc != "A distributed residential extraction network." {
		t.Errorf("Expected description to match, got: %v", res["description"])
	}

	// 2. Test Custom User Script
	customScript := `function(ctx) {
		return {
			custom_title: ctx.matchTag(ctx.html, 'title').toUpperCase(),
			total_links: ctx.extractLinks(ctx.html).length
		};
	}`

	jsCodeCustom := PrepareExtractionScript("https://example.com/startup", sampleHTML, 200, customScript)
	customOut, err := ExecuteExtractionSandbox(context.Background(), jsCodeCustom)
	if err != nil {
		t.Fatalf("Custom script sandbox execution error: %v", err)
	}

	var customRes map[string]interface{}
	if err := json.Unmarshal(customOut, &customRes); err != nil {
		t.Fatalf("Failed to parse custom script output: %v", err)
	}

	if cTitle, ok := customRes["custom_title"].(string); !ok || cTitle != "SHOW HN: MY NEW STARTUP" {
		t.Errorf("Expected uppercase custom title, got: %v", customRes["custom_title"])
	}
	if linksCount, ok := customRes["total_links"].(float64); !ok || linksCount != 2 {
		t.Errorf("Expected 2 links, got: %v", customRes["total_links"])
	}
}

func TestEndToEndExtraction(t *testing.T) {
	// Mock target website
	mockTargetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<html><head><title>Test Target Website</title></head><body><h1>Welcome to AI Data Extraction</h1></body></html>`))
	}))
	defer mockTargetServer.Close()

	// 1. Initialize Broker
	broker := NewBroker()
	brokerPort := "8999"
	go func() {
		_ = broker.Start(brokerPort)
	}()

	// Wait for broker to initialize
	time.Sleep(500 * time.Millisecond)

	// 2. Initialize Worker connected to Broker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker := &Worker{
		PricePerTask: 1.0,
	}

	t.Setenv("EMDASH_BROKER_ADDR", "localhost:"+brokerPort)
	go func() {
		_ = worker.Start(ctx, false, "8998")
	}()

	// Wait for worker to connect and register
	time.Sleep(1 * time.Second)

	// 3. Directly test residential fetcher on real public URL (e.g. example.com)
	fetchRes, err := FetchResidentialURL(context.Background(), "https://example.com", 10)
	if err != nil {
		t.Fatalf("FetchResidentialURL failed: %v", err)
	}
	if fetchRes.StatusCode != 200 {
		t.Errorf("Expected status 200, got: %d", fetchRes.StatusCode)
	}
	if len(fetchRes.Body) == 0 {
		t.Errorf("Expected non-empty HTML body")
	}

	// 4. Test Extraction Receipt Generation and Verification
	receipt, err := GenerateExtractionReceipt(999, worker.ID, "https://example.com", 200, 1024, 1.0, worker.PrivateKeyHex)
	if err != nil {
		t.Fatalf("GenerateExtractionReceipt failed: %v", err)
	}

	valid, err := VerifyExtractionReceipt(receipt, worker.ID)
	if err != nil || !valid {
		t.Errorf("Receipt cryptographic verification failed: valid=%v, err=%v", valid, err)
	}
}

func TestAPIKeyManagement(t *testing.T) {
	_ = InitDB()

	// 1. Check Default Demo Key
	demoKey, err := GetAPIKeyDB("emdash_trial_demo")
	if err != nil {
		t.Fatalf("Default trial demo key not found: %v", err)
	}
	if demoKey.Credits < 100 {
		t.Errorf("Expected at least 100 credits on demo key, got %.2f", demoKey.Credits)
	}

	// 2. Create custom trial key
	customKey := GenerateAPIKey("emdash_trial")
	err = CreateAPIKeyDB(customKey, "Test User Key", 50.0)
	if err != nil {
		t.Fatalf("Failed to create custom key: %v", err)
	}

	fetched, err := GetAPIKeyDB(customKey)
	if err != nil || fetched.Credits != 50.0 {
		t.Errorf("Failed to retrieve custom key or mismatched credits: %v", fetched)
	}

	// 3. Deduct credit
	err = DeductAPIKeyCreditDB(customKey, 5.0)
	if err != nil {
		t.Fatalf("Failed to deduct credit: %v", err)
	}

	afterDeduct, _ := GetAPIKeyDB(customKey)
	if afterDeduct.Credits != 45.0 {
		t.Errorf("Expected 45.0 credits after deduction, got %.2f", afterDeduct.Credits)
	}
}
