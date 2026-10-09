# Emdash: Sandboxed Residential Web Extraction Network for AI Agents

![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)
![License](https://img.shields.io/badge/License-MIT-blue.svg)
![Status](https://img.shields.io/badge/Status-Beta-green)

**Emdash** is an edge-native, sandboxed residential web extraction network built specifically for **AI agents, autonomous web crawlers, and LLM data pipelines**.

Traditional data center IPs (AWS, GCP, DigitalOcean) are instantly blocked by modern anti-bot shields (Cloudflare, Datadome, Akamai). Meanwhile, legacy residential proxy providers (Bright Data, Oxylabs) charge $10+/GB, waste bandwidth transferring megabytes of unneeded images and styling, and expose node hosts to legal liability via open SOCKS5/HTTP tunnels.

Emdash replaces open forward proxies with **sandboxed edge extraction**:
1. **Zero Open Proxy Abuse:** Residential nodes never act as open TCP tunnels. They only fetch target URLs with genuine desktop browser TLS fingerprints and execute sandboxed DOM extraction routines.
2. **95% Bandwidth Savings:** Instead of transferring 5MB of bloated assets across the network, the residential node parses the page locally and returns only the cleaned, structured JSON output (a few kilobytes).
3. **Built-in Host Protection:** Automatic local rate-limiting, strict private IP/localhost blocking, and a sensitive domain blacklist protect the host's internet connection.

---

## 🏗️ Architecture

```
  [ AI Agent / Crawler / Developer ]
                 │
           POST /api/extract
     { url: "...", country: "US" }
                 │
      [ Central Broker Gateway ]
   (Geo-routing, Scheduler, Ledger)
                 │
     WebSocket ──┴── WebSocket
                 │
        ┌────────┴────────┐
        ▼                 ▼
  [ Node: US Comcast ]  [ Node: UK Virgin ]
  - Auto Geo-Discovery  - Auto Geo-Discovery
  - Browser Header HTTP - Browser Header HTTP
  - QuickJS WASM Sandbox- QuickJS WASM Sandbox
  - Returns Clean JSON  - Returns Clean JSON
```

---

## ⚡ Quickstart

### 1. Build Binaries
```bash
# Build the unified CLI & Broker
go build -o emdash cmd/emdash/main.go

# Build the standalone one-click residential worker node
go build -o emdash-worker cmd/emdash-worker/main.go
```

### 2. Start the Central Broker
Start the coordinator gateway on port `8080`:
```bash
./emdash broker start 8080
```

### 3. Start a Residential Node
In another terminal (or on another machine), start a residential worker:
```bash
./emdash worker start 8085 1.0
```
*On launch, the node automatically detects its public IP, Country, City, and ISP, and registers with the broker.*

---

## 💻 Extracting Data for AI Agents

### Option A: Synchronous REST API (Instant Extraction)
Submit a URL and receive the parsed, structured JSON directly:
```bash
curl -X POST "http://localhost:8080/api/extract?sync=true" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://news.ycombinator.com",
    "country": "US"
  }'
```

**Response:**
```json
{
  "task_id": "1791486706709780000",
  "url": "https://news.ycombinator.com",
  "status": "completed",
  "status_code": 200,
  "latency_ms": 142,
  "data": {
    "title": "Hacker News",
    "clean_text_preview": "Hacker News new | past | comments | ask | show | jobs | submit...",
    "headings": {
      "h1": [],
      "h2": []
    },
    "links_count": 30,
    "sample_links": [
      { "href": "https://news.ycombinator.com/item?id=...", "text": "Show HN: ..." }
    ]
  },
  "receipt_json": "{...}"
}
```

### Option B: Custom Extraction Script
Submit a custom sandboxed JavaScript function to extract specific fields:
```bash
curl -X POST "http://localhost:8080/api/extract?sync=true" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://example.com",
    "extract_script": "function(ctx) { return { title: ctx.matchTag(ctx.html, \"title\"), length: ctx.html.length }; }"
  }'
```

### Option C: CLI Tool
Extract directly from your command line:
```bash
./emdash extract https://example.com US
```

### Option D: Batch Extraction
Submit multiple URLs for parallel scraping across the residential fleet:
```bash
curl -X POST "http://localhost:8080/api/extract/batch" \
  -H "Content-Type: application/json" \
  -d '{
    "urls": [
      "https://example.com/page1",
      "https://example.com/page2",
      "https://example.com/page3"
    ]
  }'
```

---

## 🛡️ Host Safety & Node Compliance

Emdash is engineered so home users and server owners can contribute bandwidth with complete peace of mind:
* **No Open SOCKS/HTTP Proxy:** Incoming requests can only be HTTP/HTTPS GET queries targeting legitimate public web domains.
* **Private Network & Localhost Isolation:** Any attempt to scrape `127.0.0.1`, `localhost`, `10.0.0.0/8`, `192.168.0.0/16`, or internal subnets is rejected before leaving the machine.
* **Sensitive Domain Blacklist:** Automatic blocking of banking, financial portals, adult material, and government domains.
* **Host Rate Limiting:** Built-in rate limiting (max 12 requests/min by default) ensures the host's internet connection is never degraded.
* **WASM Sandboxing:** Extraction scripts execute inside a strictly isolated QuickJS WASI sandbox (`wazero`) with enforced memory limits and timeouts.

---

## 📄 License
This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.