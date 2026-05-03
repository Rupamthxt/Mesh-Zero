package main

import (
	"C"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/rupamthxt/mesh-zero/core"
)

var meshWorker *core.Worker
var ffiCancel context.CancelFunc

//export StartMeshDaemon
func StartMeshDaemon() {
	if meshWorker != nil {
		fmt.Println("[FFI] Mesh daemon is already running.")
		return
	}
	fmt.Println("[FFI] Booting Mesh-Zero engine from Flutter UI...")
	ctx, cancel := context.WithCancel(context.Background())
	ffiCancel = cancel
	meshWorker = &core.Worker{}

	go func() {
		meshWorker.Start(ctx, true, "8080")
	}()
}

//export StopMeshDaemon
func StopMeshDaemon() {
	if ffiCancel != nil {
		fmt.Println("[FFI] Shutting down Mesh-Zero engine...")
		ffiCancel()
		meshWorker = nil
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "worker":
		handleWorkerCommand()
	case "run":
		if len(os.Args) < 4 {
			fmt.Println("Usage: mesh-zero run <task.wasm> <input.data>")
			os.Exit(1)
		}
		wasmPath := os.Args[2]
		inputPath := os.Args[3]
		fmt.Printf("Submitting %s to the mesh...\n", wasmPath)
		core.RunSender(wasmPath, inputPath)
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fmt.Printf("Failed to generate key : %d\n", err)
			return
		}
		fmt.Printf("PUBLIC KEY (Give to Workers): %s\n", hex.EncodeToString(pub))
		fmt.Printf("PRIVATE KEY (Keep Secret)   : %s\n", hex.EncodeToString(priv))
		fmt.Println("--------------------------------------------------")
		fmt.Println("Export these as environment variables before starting nodes:")
		fmt.Println("export MESH_PUB_KEY=<your_public_key>")
		fmt.Println("export MESH_PRIV_KEY=<your_private_key>")

	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
	}
}

func printUsage() {
	fmt.Printf(`Mesh-Zero: Decentralized Compute Node
		Usage:
		mesh-zero worker start    - Run the node in the foreground
		mesh-zero worker daemon   - Run the node silently in the background
		mesh-zero worker stop	  - Stop the background daemon
		mesh-zero run <file>      - Submit a WASM task to the mesh
		\n`)
}

func handleWorkerCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: mesh-zero worker [start|daemon]")
		os.Exit(1)
	}

	subCommand := os.Args[2]

	apiPort := "8080"
	if len(os.Args) >= 4 {
		apiPort = os.Args[3]
	}

	if subCommand == "start" {
		fmt.Println("Starting Mesh-Zero Node in foreground...")
		worker := &core.Worker{
			Hooks: nil,
		}
		worker.Start(context.Background(), true, apiPort)
		return
	}

	if subCommand == "daemon" {
		fmt.Println("Spawning Mesh-Zero daemon...")

		binaryPath, _ := os.Executable()
		cmd := exec.Command(binaryPath, "worker", "start")

		logFile, err := os.OpenFile("mesh-zero.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			fmt.Printf("Failed to open log file: %v\n", err)
			return
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile

		err = cmd.Start()
		if err != nil {
			fmt.Printf("Failed to start daemon: %v\n", err)
			return
		}

		pidStr := fmt.Sprintf("%d", cmd.Process.Pid)
		os.WriteFile("mesh-zero.pid", []byte(pidStr), 0644)

		fmt.Printf("Daemon running in background. (PID: %d)\n", cmd.Process.Pid)
		fmt.Println("Check mesh-zero.log for node output.")
		os.Exit(0)
	}

	if subCommand == "stop" {
		pidBytes, err := os.ReadFile("mesh-zero.pid")
		if err != nil {
			fmt.Println("Could not find mesh-zero.pid. Is the daemon running?")
			return
		}
		pid, err := strconv.Atoi(string(pidBytes))
		if err != nil {
			fmt.Printf("Invalid PID in file")
			return
		}

		process, err := os.FindProcess(pid)
		if err != nil {
			fmt.Printf("Failed to find process: %v\n", err)
			return
		}

		err = process.Signal(syscall.SIGTERM)
		if err != nil {
			fmt.Printf("Failed to stop daemon: %v\n", err)
			return
		}

		os.Remove("mesh-zero.pid")
		fmt.Printf("Successfully stopped Mesh-zero daemon (PID: %d)\n", pid)
		return
	}
}
