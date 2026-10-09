package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rupamthxt/emdash/core"
)

// GlobalBrokerAddr is the default global server address for your production Broker
// Normal users double-clicking this app will be routed directly to this server.
const GlobalBrokerAddr = "157.245.107.252:8080"

func main() {
	// Set default broker destination if not overridden by env
	if os.Getenv("EMDASH_BROKER_ADDR") == "" {
		os.Setenv("EMDASH_BROKER_ADDR", GlobalBrokerAddr)
	}

	enableAPI := false
	apiPort := "8085"
	pricePerTask := 1.0

	fmt.Println(" _____ ___  ___ ______   ___   _____  _   _ ")
	fmt.Println("|  ___||  \\/  ||  _  \\  / _ \\ /  ___|| | | |")
	fmt.Println("| |__  | .  . || | | | / /_\\ \\\\ `--. | |_| |")
	fmt.Println("|  __| | |\\/| || | | | |  _  | `--. \\|  _  |")
	fmt.Println("| |___ | |  | || |/ /  | | | |/\\__/ /| | | |")
	fmt.Println("\\____/ \\_|  |_/|___/   \\_| |_/\\____/ \\_| |_/")
	fmt.Println("")
	fmt.Println("====================================================")
	fmt.Println("       EMDASH RESIDENTIAL EXTRACTION NODE")
	fmt.Println("====================================================")

	worker := &core.Worker{
		PricePerTask: pricePerTask,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Capture interrupt signals to shut down gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n[SYSTEM] Stopping worker node gracefully...")
		cancel()
	}()

	err := worker.Start(ctx, enableAPI, apiPort)
	if err != nil {
		fmt.Printf("Fatal worker execution error: %v\n", err)
		os.Exit(1)
	}
}
