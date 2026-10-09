package core

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Blocked domain patterns for host protection and compliance
var blockedDomainKeywords = []string{
	"bank", "paypal", "wellsfargo", "chase.com", "citi.com", "bankofamerica",
	"capitalone", "fidelity", "vanguard", "coinbase", "binance", "kraken",
	".gov", ".mil", "irs.gov", "fbi.gov", "cia.gov",
	"router", "tplink", "netgear", "asus.com",
	"porn", "xxx", "xvideos", "pornhub", "onlyfans",
	"torrent", "piratebay", "1337x", "darkweb",
}

// RateLimiter protects the worker node from overwhelming the host residential connection
type RateLimiter struct {
	maxPerMinute int
	requests     []time.Time
	mu           sync.Mutex
}

func NewRateLimiter(maxPerMinute int) *RateLimiter {
	if maxPerMinute <= 0 {
		maxPerMinute = 12 // Default: max 12 requests per minute (one every 5 seconds)
	}
	return &RateLimiter{
		maxPerMinute: maxPerMinute,
		requests:     make([]time.Time, 0),
	}
}

func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)

	// Filter timestamps within the last minute
	valid := make([]time.Time, 0, len(rl.requests))
	for _, t := range rl.requests {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.requests = valid

	if len(rl.requests) >= rl.maxPerMinute {
		return false
	}

	rl.requests = append(rl.requests, now)
	return true
}

// ValidateTargetURL inspects a URL to guarantee it does not target private IPs,
// internal networks, localhost, or sensitive/restricted domains.
func ValidateTargetURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL structure: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported protocol %q: only http and https are permitted", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("missing hostname in target URL")
	}

	// 1. Localhost and loopback checks
	if host == "localhost" || host == "localhost.localdomain" || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("security violation: localhost and local domains are prohibited")
	}

	// 2. Direct IP address checks
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("security violation: private/loopback IP address %s is prohibited", ip.String())
		}
	}

	// 3. Sensitive domain keyword blacklist
	for _, kw := range blockedDomainKeywords {
		if strings.Contains(host, kw) {
			return fmt.Errorf("security compliance: domain %q matches restricted category (%s)", host, kw)
		}
	}

	return nil
}
