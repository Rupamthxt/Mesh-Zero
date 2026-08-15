# Emdash: Running & Deployment Operations Guide

This guide details all compiling, local execution, frontend console, desktop wrapping, and cloud deployment instructions for the Emdash network.

---

## 1. Compilation Commands (Go Backend)

Build the `emdash` executable from the project root directory.

### A. Standard Build
Compiles the central Broker, CLI runner client, and standard CPU worker daemon:
```bash
go build -o emdash cmd/emdash/main.go
```

### B. GPU-Enabled Build (Windows / Linux)
Compiles the worker node with active NVIDIA CUDA driver runtime probes (without CGO):
```bash
go build -tags gpu -o emdash cmd/emdash/main.go
```

### C. GPU-Enabled Build (macOS)
Compiles the worker node with Apple Metal execution framework hooks:
```bash
go build -tags gpu -o emdash cmd/emdash/main.go
```

---

## 2. Local Service Execution

### A. Start the Central Broker
Start the matchmaking gateway and REST APIs. Defaults to SQLite WAL database tracking.
```bash
# Start Broker on port 8080
./emdash broker start 8080
```
*   **Stripe billing variables (Optional):**
    ```bash
    export STRIPE_SECRET_KEY="sk_test_..."
    export STRIPE_WEBHOOK_SECRET="whsec_..."
    ./emdash broker start 8080
    ```
    *If missing, the Broker automatically runs in simulated offline billing mode.*

### B. Start a Worker Node (Host Panel)
Boot up a local node to join the pool:
```bash
# Arguments: worker start <port> <price_per_ms>
# Runs worker API on port 8085, charging 0.035 credits/ms
./emdash worker start 8085 0.035
```
*   **Targeting a remote broker (Optional):**
    ```bash
    export EMDASH_BROKER_ADDR="192.168.1.15:8080"
    ./emdash worker start 8085 0.035
    ```

---

## 3. Web Console & Desktop Client

### A. Run Web Frontend (Local Developer Mode)
The visual dashboard panel lives in the `mesh-zero-web` directory:
```bash
cd ../mesh-zero-web
npm install
npm run dev
```
*Accessible locally at `http://localhost:3000`.*

### B. Run Tauri Desktop App (Development)
Pipes web pages into a native desktop container and spawns the Go worker as a sidecar process:
```bash
# 1. Compile worker with triple suffix and place in tauri/bin
# (Replace 'aarch64-apple-darwin' with your system's target triple)
go build -tags gpu -o tauri/bin/emdash-aarch64-apple-darwin cmd/emdash/main.go

# 2. Launch Tauri developer window
cd tauri
cargo tauri dev
```

### C. Package Tauri Production Installers
```bash
cd tauri
cargo tauri build
```
*Output installer formats (.dmg, .msi, .deb) are written to `tauri/target/release/bundle/`.*

---

## 4. Submitting Compute Jobs (CLI Client)

Verify execution by submitting workloads directly from your MacBook command line:

### A. Submit a JavaScript Script
Submit a raw JavaScript text file to be evaluated inside the QuickJS WASI sandbox:
```bash
# 1. Create a script file
echo 'console.log("Hello from Emdash! Result:", 50 * 20);' > task.js

# 2. Run the script on the network (Max budget: 0.05 credits/ms)
./emdash run task.js "" 0.05
```

### B. Submit a WebAssembly Binary
```bash
# Arguments: run <wasm_path> <input_data_path> <max_price>
./emdash run task/gpu_task.wasm task/data.txt 0.05
```

---

## 5. DigitalOcean Cloud Deployment (Broker)

Host the broker gateway on a public Ubuntu 22.04 LTS Droplet.

### A. Automated Deployment (One-Click)
Run our automated script from your MacBook to compile, upload, and configure the server:
```bash
./deploy.sh
```

### B. Manual Server Configuration
If executing steps manually on the Droplet:

1.  **Move Compiled Binary to Executables path:**
    ```bash
    mv /tmp/emdash /usr/local/bin/emdash
    chmod +x /usr/local/bin/emdash
    mkdir -p /var/lib/emdash
    ```
2.  **Setup Systemd Daemon File (`/etc/systemd/system/emdash.service`):**
    ```ini
    [Unit]
    Description=Emdash Central Broker
    After=network.target

    [Service]
    Type=simple
    User=root
    WorkingDirectory=/var/lib/emdash
    ExecStart=/usr/local/bin/emdash broker start 8080
    Restart=always
    RestartSec=5
    Environment=PORT=8080
    Environment=STRIPE_SECRET_KEY=sk_test_...
    Environment=STRIPE_WEBHOOK_SECRET=whsec_...

    [Install]
    WantedBy=multi-user.target
    ```
    *Enable and start: `systemctl daemon-reload && systemctl enable --now emdash`.*

3.  **Setup Caddy Reverse Proxy (`/etc/caddy/Caddyfile`):**
    ```caddy
    emdash.world {
        reverse_proxy localhost:8080
    }
    ```
    *Reload Caddy: `systemctl reload caddy`.*

---

## 6. Standalone One-Click PC Client (Emdash-Worker)

For normal PC users to join the pool with a single click, compile the standalone `emdash-worker` console app. It is pre-routed to your production global server and automatically handles key generation and registration without any terminal commands.

### A. Configure Your Global Broker Address
Open [main.go](file:///Users/rupamthxt/Projects/mesh-zero/cmd/emdash-worker/main.go) and update the `GlobalBrokerAddr` constant with your production droplet's public IP/Domain:
```go
const GlobalBrokerAddr = "YOUR_GLOBAL_SERVER_IP:8080"
```

### B. Cross-Compile for Windows PC (From macOS/Linux)
Generate a CGO-free, zero-dependency Windows executable:
```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/emdash-worker.exe cmd/emdash-worker/main.go
```
*Simply send the compiled `emdash-worker.exe` to Windows users. When they double-click the file, it opens a formatted console panel and registers their PC to your global server automatically.*

### C. Compile for macOS and Linux PCs
*   **macOS:**
    ```bash
    go build -o dist/emdash-worker-mac cmd/emdash-worker/main.go
    ```
*   **Linux:**
    ```bash
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/emdash-worker-linux cmd/emdash-worker/main.go
    ```
