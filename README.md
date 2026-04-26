# Mesh-Zero

> **A Decentralized, Peer-to-Peer WebAssembly Execution Engine & Distributed Storage Fabric.**

Mesh-Zero is a lightweight compute substrate built entirely in Go. It allows any device—from a MacBook to a Raspberry Pi—to dynamically discover peers, negotiate a connection, and securely execute sandboxed WebAssembly (`.wasm`) payloads across a decentralized network. 

No AWS. No Docker. No central orchestration. Just pure peer-to-peer compute.

---

## Key Capabilities

* **Zero-Configuration Discovery:** Uses **mDNS** for local network swarming and **Kademlia DHT** for global internet routing. Nodes find each other automatically.
* **Universal Execution Sandbox:** Powered by **wazero** and **WASI**. Write your tasks in Go, Rust, or C/C++, compile to `.wasm`, and execute them safely anywhere in the mesh.
* **Idempotent & Atomic:** Implements a strict `MZ02` wire protocol with unique Task IDs and mutex-locked network streams to prevent the "thundering herd" problem and guarantee exact-once execution.
* **Zero-Copy Data Bridge:** Exposes custom Host Functions linking the WASM sandbox directly to local OS memory mapped files (`mmap`), allowing gigabytes of data (like VectraDB shards) to be queried with sub-millisecond overhead.

---

## The `MZ02` Wire Protocol

To eliminate HTTP overhead, Mesh-Zero nodes communicate via multiplexed `libp2p` streams using a highly optimized 20-byte binary header.

| Field | Type | Size | Description |
| :--- | :--- | :--- | :--- |
| **Magic** | `[4]byte` | 4 Bytes | Protocol Identifier (`MZ02`). |
| **TaskID** | `uint64` | 8 Bytes | Unique ID to prevent double-execution across the mesh. |
| **WasmLen** | `uint32` | 4 Bytes | Byte size of the executable payload. |
| **ParamLen**| `uint32` | 4 Bytes | Byte size of the Stdin parameter data. |
| **Payload** | `[]byte` | Variable | The `.wasm` binary followed immediately by the input data. |

---

## Quickstart

### Prerequisites
* **Go** (v1.21+)
* **TinyGo** (For compiling WASI-compatible payloads)

### 1. Start a Worker Node
The Worker acts as the compute engine. It binds to all interfaces, advertises itself via mDNS, and listens for WASM payloads.

```bash
# Clone the repository and run the worker
go run mesh.go
```
Output:
```bash
Worker Node 12D3KooW... listening on Localhost. Waiting for tasks...
```
### 2. Compile a WASM task
Write a generic task in Go. Mesh-Zero uses `os.Stdin` to inject parameters and `os.Stdout` to stream results.
```bash
# hasher.go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func main() {
	input, _ := io.ReadAll(os.Stdin)
	hash := sha256.Sum256(input)
	fmt.Printf("[WASM] SHA-256: %s\n", hex.EncodeToString(hash[:]))
}
```
Compile it to a lightweight WASI binary:
```bash
tinygo build -o hasher.wasm -target=wasi hasher.go
```
### 3. Submit the task via the sender
The Sender acts as a load balancer. It discovers available Workers, atomically locks a stream, and blasts the payload.

Create a data file to process:
```bash
echo "Hello from the decentralized edge!" > input.txt
```
Send it to the mesh:
```bash
go run sender.go hasher.wasm input.txt
```
Output:
```bash
[mDNS] Found Worker: 12D3KooW...
 -> Stream locked to 12D3KooW...! Blasting payload...
 -> Payload sent! Waiting for results...
[WASM] SHA-256: 7f0a...
```
## Roadmap
Mesh-Zero is an evolving systems architecture. Next phases include:
* **[ ] VectraDB `mmap` Integration:** Connect the WASM sandbox directly to on-disk vector databases.
* **[ ] Compute Fuel/Gas Limits:** Implement deterministic execution limits via cooperative heartbeats to prevent infinite loops.
* **[✅] Daemonization CLI:** Wrap the node in a robust CLI (`mesh-zero daemon start`) for silent background operation.
* **[ ] IoT Gateway Pattern:** Implement a BLE scanner to allow Micro-IoT devices (like smart thermometers) to act as peripheral sensors for the mesh.