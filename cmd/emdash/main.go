package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/rupamthxt/emdash/core"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "broker":
		handleBrokerCommand()
	case "worker":
		handleWorkerCommand()
	case "run":
		if len(os.Args) < 4 {
			fmt.Println("Usage: emdash run <task.wasm> <input.data> [max-price-per-ms]")
			os.Exit(1)
		}
		wasmPath := os.Args[2]
		inputPath := os.Args[3]
		maxPrice := 0.0
		if len(os.Args) >= 5 {
			if mp, err := strconv.ParseFloat(os.Args[4], 64); err == nil {
				maxPrice = mp
			}
		}
		fmt.Printf("Submitting %s to the emdash pool (Max Price: %.4f)...\n", wasmPath, maxPrice)
		core.RunSender(wasmPath, inputPath, maxPrice)
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
		fmt.Println("export EMDASH_PUB_KEY=<your_public_key>")
		fmt.Println("export EMDASH_PRIV_KEY=<your_private_key>")

	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
	}
}

func printUsage() {
	fmt.Printf(`Emdash: Centralized & Verifiable Compute Pool
		Usage:
		emdash broker start [port]           - Start the central broker scheduler
		emdash worker start [port] [price]   - Run node in foreground with optional port & pricing rate
		emdash worker daemon [port] [price]  - Run node in background with optional port & pricing rate
		emdash worker stop	                - Stop the background daemon
		emdash run <file> <input> [budget]   - Submit a WASM task with maximum pricing budget
		\n`)
}

func handleWorkerCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: emdash worker [start|daemon]")
		os.Exit(1)
	}

	subCommand := os.Args[2]

	apiPort := "8080"
	if len(os.Args) >= 4 {
		apiPort = os.Args[3]
	}

	price := 0.01 // default
	if len(os.Args) >= 5 {
		if p, err := strconv.ParseFloat(os.Args[4], 64); err == nil {
			price = p
		}
	}

	if subCommand == "start" {
		fmt.Printf("Starting Emdash Node in foreground on port %s (Price: %.4f)...\n", apiPort, price)
		worker := &core.Worker{
			Hooks:      core.DefaultHooks,
			PricePerMs: price,
		}
		worker.Start(context.Background(), true, apiPort)
		return
	}

	if subCommand == "daemon" {
		fmt.Println("Spawning Emdash daemon...")

		binaryPath, _ := os.Executable()
		args := []string{"worker", "start", apiPort}
		if len(os.Args) >= 5 {
			args = append(args, os.Args[4])
		}
		cmd := exec.Command(binaryPath, args...)

		logFile, err := os.OpenFile("emdash.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
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
		os.WriteFile("emdash.pid", []byte(pidStr), 0644)

		fmt.Printf("Daemon running in background. (PID: %d)\n", cmd.Process.Pid)
		fmt.Println("Check emdash.log for node output.")
		os.Exit(0)
	}

	if subCommand == "stop" {
		pidBytes, err := os.ReadFile("emdash.pid")
		if err != nil {
			fmt.Println("Could not find emdash.pid. Is the daemon running?")
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

		os.Remove("emdash.pid")
		fmt.Printf("Successfully stopped Emdash daemon (PID: %d)\n", pid)
		return
	}
}

func handleBrokerCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: emdash broker [start] [port]")
		os.Exit(1)
	}

	subCommand := os.Args[2]
	port := "8080"
	if len(os.Args) >= 4 {
		port = os.Args[3]
	}

	if subCommand == "start" {
		broker := core.NewBroker()
		defer core.CloseDB()
		err := broker.Start(port)
		if err != nil {
			fmt.Printf("Broker server error: %v\n", err)
		}
	} else {
		fmt.Println("Unknown broker subcommand")
	}
}
