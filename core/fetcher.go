package core

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type FetchResult struct {
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	LatencyMs  int64             `json:"latency_ms"`
	Size       int               `json:"size"`
}

// FetchResidentialURL performs an HTTP request using real browser headers and connection settings
func FetchResidentialURL(ctx context.Context, targetURL string, timeoutSec int) (*FetchResult, error) {
	if err := ValidateTargetURL(targetURL); err != nil {
		return nil, err
	}

	if timeoutSec <= 0 || timeoutSec > 30 {
		timeoutSec = 15
	}

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to construct request: %w", err)
	}

	// Modern Chrome / Safari headers to mimic genuine desktop residential browsers
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Sec-Ch-Ua", `"Google Chrome";v="123", "Not:A-Brand";v="8", "Chromium";v="123"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Cache-Control", "max-age=0")

	client := &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	startTime := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(startTime).Milliseconds()

	if err != nil {
		return nil, fmt.Errorf("network request failed: %w", err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr == nil {
			defer gzReader.Close()
			reader = gzReader
		}
	}

	// Cap read buffer at 15MB to prevent memory exhaustion
	limitedReader := io.LimitReader(reader, 15*1024*1024)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	headersMap := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headersMap[k] = v[0]
		}
	}

	return &FetchResult{
		StatusCode: resp.StatusCode,
		Headers:    headersMap,
		Body:       string(bodyBytes),
		LatencyMs:  latency,
		Size:       len(bodyBytes),
	}, nil
}
