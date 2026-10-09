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
	case "extract":
		if len(os.Args) < 3 {
			fmt.Println("Usage: emdash extract <url> [country] [script.js]")
			fmt.Println("Example: emdash extract https://news.ycombinator.com US")
			os.Exit(1)
		}
		targetURL := os.Args[2]
		country := ""
		if len(os.Args) >= 4 {
			country = os.Args[3]
		}
		scriptPath := ""
		if len(os.Args) >= 5 {
			scriptPath = os.Args[4]
		}
		core.RunExtract(targetURL, country, scriptPath)
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			fmt.Printf("Failed to generate key: %v\n", err)
			return
		}
		fmt.Printf("PUBLIC KEY (Node Identity)  : %s\n", hex.EncodeToString(pub))
		fmt.Printf("PRIVATE KEY (Signing Secret): %s\n", hex.EncodeToString(priv))
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
	fmt.Printf(`Emdash: Sandboxed Residential Web Extraction for AI Agents
Usage:
  emdash broker start [port]                    - Start the central matchmaking gateway
  emdash worker start [port] [price_per_page]   - Run residential node in foreground
  emdash worker daemon [port] [price_per_page]  - Run residential node in background
  emdash worker stop                            - Stop the background node daemon
  emdash extract <url> [country] [script.js]    - Submit extraction task and print extracted JSON
  emdash keygen                                 - Generate new node cryptographic keypair
`)
}

func handleWorkerCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: emdash worker [start|daemon|stop]")
		os.Exit(1)
	}

	subCommand := os.Args[2]

	apiPort := "8085"
	if len(os.Args) >= 4 {
		apiPort = os.Args[3]
	}

	price := 1.0 // default 1 credit per extraction
	if len(os.Args) >= 5 {
		if p, err := strconv.ParseFloat(os.Args[4], 64); err == nil {
			price = p
		}
	}

	if subCommand == "start" {
		fmt.Printf("Starting Emdash Residential Node in foreground (Rate: %.2f credits/page)...\n", price)
		worker := &core.Worker{
			PricePerTask: price,
		}
		_ = worker.Start(context.Background(), true, apiPort)
		return
	}

	if subCommand == "daemon" {
		fmt.Println("Spawning Emdash residential node daemon...")

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
		_ = os.WriteFile("emdash.pid", []byte(pidStr), 0644)

		fmt.Printf("Daemon running in background (PID: %d).\n", cmd.Process.Pid)
		fmt.Println("Check emdash.log for node logs.")
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
			fmt.Printf("Invalid PID in file\n")
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

		_ = os.Remove("emdash.pid")
		fmt.Printf("Successfully stopped Emdash node daemon (PID: %d)\n", pid)
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
