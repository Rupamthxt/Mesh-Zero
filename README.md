# Emdash
 
 ![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)
 ![License](https://img.shields.io/badge/License-MIT-blue.svg)
 ![Status](https://img.shields.io/badge/Status-Beta-orange)
 
 **Emdash** is a CGO-free, lightweight, and verifiable WebAssembly (WASM) compute pool. It turns standard consumer hardware—from gaming rigs to Apple Silicon MacBooks—into a secure, globally distributed serverless execution network.
 
 Unlike heavy container orchestration networks, Emdash runs workloads inside a secure **Wazero WebAssembly sandbox** or an **embedded JavaScript/WebGPU interpreter**, compiling down to a single static binary.
 
 ---
 
 ## 🏗️ Core Architecture
 
 Emdash uses a high-performance **Broker & Worker WebSocket architecture**:
 
 ```
  [ Compute Buyers ] ──────►  [ Central Broker ]  ◄────── [ Worker Nodes ]
   (CLI, curl, Web Console)    - Matchmaking Scheduler    - Wazero WASM Sandbox
                               - SQLite Job Logs          - JS / WebGPU Polyfill
                               - REST Submission APIs     - Cryptographic Receipts
 ```
 
 *   **Central Broker:** Acts as the network gateway, receiving tasks via REST APIs, managing scheduling state in a WAL-mode SQLite database, and matchmaking workloads to registered nodes.
 *   **Lightweight Workers:** Connect to the Broker via WebSockets, pull queued tasks, execute them inside a secure CPU/GPU sandbox, and sign a cryptographic proof-of-work receipt.
 *   **WASM Sandbox (Wazero):** Runs untrusted pre-compiled WebAssembly binaries with strict memory limits (`WithMemoryLimitPages`) and execution timeouts.
 *   **Headless WebGPU JavaScript Sandbox:** Allows developers to submit standard, browser-compatible WebGPU JavaScript code. An embedded polyfill inside QuickJS intercepts the calls and runs compute shaders natively on the worker's hardware (Metal on macOS, CUDA on Windows/Linux).
 
 ---
 
 ## ⚡ Quickstart
 
 ### Prerequisites
 *   **Go** (v1.22+)
 *   **NVIDIA Drivers** (Optional, for CUDA GPU acceleration on Linux/Windows)
 
 ### 1. Compile the Project
 Clone the repository and compile the static binary:
 ```bash
 git clone https://github.com/Rupamthxt/mesh-zero.git emdash
 cd emdash
 go build -o emdash cmd/emdash/main.go
 ```
 
 ### 2. Generate Security Keys
 Create your worker authentication key pair. The private key secures proof-of-work receipts:
 ```bash
 ./emdash keygen
 ```
 Export the keys to your environment:
 ```bash
 export EMDASH_PUB_KEY=<your_public_key>
 export EMDASH_PRIV_KEY=<your_private_key>
 ```
 
 ### 3. Launch the Central Broker
 Start the coordinator server on port `8080`:
 ```bash
 ./emdash broker start 8080
 ```
 
 ### 4. Start a Worker Node
 Launch a compute worker in another terminal, pointing to the broker. You can specify the worker price per millisecond of calculation (e.g., `0.01` credits/ms):
 ```bash
 ./emdash worker start 8085 0.01
 ```
 
 ---
 
 ## 💻 Submitting Workloads
 
 ### Option A: Standard JavaScript / WebGPU Compute
 Emdash supports running browser-compatible WebGPU scripts directly on the network with **zero WASM compilation**.
 
 Create a WebGPU script (`multiply.js`):
 ```javascript
 async function runGPU() {
     const adapter = await navigator.gpu.requestAdapter();
     const device = await adapter.requestDevice();
     
     // ... setup storage buffers and WGSL compute shader code ...
     
     device.queue.submit([commandEncoder.finish()]);
     
     await readBuffer.mapAsync(GPUMapMode.READ);
     const result = new Float32Array(readBuffer.getMappedRange());
     console.log("GPU Computation Result:", result);
 }
 runGPU();
 ```
 
 Submit it directly via the REST endpoint:
 ```bash
 curl -X POST -F "script=<multiply.js" http://localhost:8080/api/tasks/submit
 ```
 
 ### Option B: Compiling & Submitting WebAssembly (WASM)
 Write a stateless program in Go (or Rust) that reads input from `os.Stdin` and writes output to `os.Stdout`:
 ```go
 // main.go
 package main
 import (
 	"crypto/sha256"
 	"encoding/hex"
 	"io"
 	"os"
 )
 func main() {
 	input, _ := io.ReadAll(os.Stdin)
 	hash := sha256.Sum256(input)
 	os.Stdout.Write([]byte(hex.EncodeToString(hash[:])))
 }
 ```
 
 Compile to WebAssembly WASI:
 ```bash
 tinygo build -o hasher.wasm -target=wasi main.go
 ```
 
 Submit the binary with an input parameter file:
 ```bash
 echo "Hello Emdash!" > input.txt
 ./emdash run hasher.wasm input.txt 0.05
 ```
 
 ---
 
 ## 🛡️ Sandbox Resource Boundaries
 
 Workers configure strict sandbox execution limits on startup via environment variables:
 *   `EMDASH_MAX_RAM`: Memory limits in bytes mapped to Wazero pages (e.g. `536870912` for 512MB limit).
 *   `EMDASH_TASK_TIMEOUT`: Maximum execution limit in seconds before the task is forcefully killed (Default: `5` seconds).
 
 ---
 
 ## 📄 License
 This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.