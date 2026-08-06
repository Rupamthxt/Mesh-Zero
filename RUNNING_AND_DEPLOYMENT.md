# MeshØ: Running & Deployment Operations Guide

This guide details all compiling, local execution, frontend console, desktop wrapping, and cloud deployment instructions for the Mesh-Zero network.

---

## 1. Compilation Commands (Go Backend)

Build the `mesh-zero` executable from the project root directory.

### A. Standard Build
Compiles the central Broker, CLI runner client, and standard CPU worker daemon:
```bash
go build -o mesh-zero cmd/mesh-zero/main.go
```

### B. GPU-Enabled Build (Windows / Linux)
Compiles the worker node with active NVIDIA CUDA driver runtime probes (without CGO):
```bash
go build -tags gpu -o mesh-zero cmd/mesh-zero/main.go
```

### C. GPU-Enabled Build (macOS)
Compiles the worker node with Apple Metal execution framework hooks:
```bash
go build -tags gpu -o mesh-zero cmd/mesh-zero/main.go
```

---

## 2. Local Service Execution

### A. Start the Central Broker
Start the matchmaking gateway and REST APIs. Defaults to SQLite WAL database tracking.
```bash
# Start Broker on port 8080
./mesh-zero broker start 8080
```
*   **Stripe billing variables (Optional):**
    ```bash
    export STRIPE_SECRET_KEY="sk_test_..."
    export STRIPE_WEBHOOK_SECRET="whsec_..."
    ./mesh-zero broker start 8080
    ```
    *If missing, the Broker automatically runs in simulated offline billing mode.*

### B. Start a Worker Node (Host Panel)
Boot up a local node to join the pool:
```bash
# Arguments: worker start <port> <price_per_ms>
# Runs worker API on port 8085, charging 0.035 credits/ms
./mesh-zero worker start 8085 0.035
```
*   **Targeting a remote broker (Optional):**
    ```bash
    export MESH_BROKER_ADDR="192.168.1.15:8080"
    ./mesh-zero worker start 8085 0.035
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
go build -tags gpu -o tauri/bin/mesh-zero-aarch64-apple-darwin cmd/mesh-zero/main.go

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
echo 'console.log("Hello from MeshØ! Result:", 50 * 20);' > task.js

# 2. Run the script on the network (Max budget: 0.05 credits/ms)
./mesh-zero run task.js "" 0.05
```

### B. Submit a WebAssembly Binary
```bash
# Arguments: run <wasm_path> <input_data_path> <max_price>
./mesh-zero run task/gpu_task.wasm task/data.txt 0.05
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
    mv /tmp/mesh-zero /usr/local/bin/mesh-zero
    chmod +x /usr/local/bin/mesh-zero
    mkdir -p /var/lib/mesh-zero
    ```
2.  **Setup Systemd Daemon File (`/etc/systemd/system/mesh-zero.service`):**
    ```ini
    [Unit]
    Description=Mesh-Zero Central Broker
    After=network.target

    [Service]
    Type=simple
    User=root
    WorkingDirectory=/var/lib/mesh-zero
    ExecStart=/usr/local/bin/mesh-zero broker start 8080
    Restart=always
    RestartSec=5
    Environment=PORT=8080
    Environment=STRIPE_SECRET_KEY=sk_test_...
    Environment=STRIPE_WEBHOOK_SECRET=whsec_...

    [Install]
    WantedBy=multi-user.target
    ```
    *Enable and start: `systemctl daemon-reload && systemctl enable --now mesh-zero`.*

3.  **Setup Caddy Reverse Proxy (`/etc/caddy/Caddyfile`):**
    ```caddy
    api.meshzero.network {
        reverse_proxy localhost:8080
    }
    ```
    *Reload Caddy: `systemctl reload caddy`.*

---

## 6. Standalone One-Click PC Client (Attick-Worker)

For normal PC users to join the mesh with a single click, compile the standalone `attick-worker` console app. It is pre-routed to your production global server and automatically handles key generation and registration without any terminal commands.

### A. Configure Your Global Broker Address
Open [main.go](file:///Users/rupamthxt/Projects/mesh-zero/cmd/attick-worker/main.go) and update the `GlobalBrokerAddr` constant with your production droplet's public IP/Domain:
```go
const GlobalBrokerAddr = "YOUR_GLOBAL_SERVER_IP:8080"
```

### B. Cross-Compile for Windows PC (From macOS/Linux)
Generate a CGO-free, zero-dependency Windows executable:
```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/attick-worker.exe cmd/attick-worker/main.go
```
*Simply send the compiled `attick-worker.exe` to Windows users. When they double-click the file, it opens a formatted console panel and registers their PC to your global server automatically.*

### C. Compile for macOS and Linux PCs
*   **macOS:**
    ```bash
    go build -o dist/attick-worker-mac cmd/attick-worker/main.go
    ```
*   **Linux:**
    ```bash
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/attick-worker-linux cmd/attick-worker/main.go
    ```
