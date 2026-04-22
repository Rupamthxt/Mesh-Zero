package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
)

type senderNotifee struct {
	h       host.Host
	ctx     context.Context
	done    chan bool
	mu      sync.Mutex
	hasSent bool
}

func (n *senderNotifee) HandlePeerFound(pi peer.AddrInfo) {
	n.mu.Lock()
	if n.hasSent {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	// Skip ourselves
	if pi.ID == n.h.ID() {
		return
	}

	// Wait for the OS to attach IP addresses to the mDNS broadcast
	if len(pi.Addrs) == 0 {
		return
	}

	fmt.Printf("\n[mDNS] Found Worker: %s\n", pi.ID)
	fmt.Printf(" -> Available IPs: %v\n", pi.Addrs)
	fmt.Printf("\n[mDNS] Attempting Worker: %s\n", pi.ID)

	// Attempt Connection (NO PANICS ALLOWED)
	err := n.h.Connect(n.ctx, pi)
	if err != nil {
		fmt.Printf(" -> Connection skipped (likely unroutable interface): %v\n", err)
		return
	}

	fmt.Println(" -> Connection established! Opening stream...")

	s, err := n.h.NewStream(n.ctx, pi.ID, "/mesh-zero/task/1.0.0")
	if err != nil {
		fmt.Printf(" -> Failed to open stream: %v\n", err)
		return
	}
	defer s.Close()
	n.mu.Lock()
	if n.hasSent {
		// We were beaten by another thread just milliseconds ago.
		s.Close()
		n.mu.Unlock()
		return
	}
	n.hasSent = true // WE CLAIM THE TASK!
	n.mu.Unlock()

	// Ensure the file exists
	wasmBytes, err := os.ReadFile("task.wasm")
	if err != nil {
		fmt.Printf(" -> FATAL: Could not read task.wasm: %v\n", err)
		n.done <- true
		return
	}

	paramBytes := []byte(`{"vector": [0.1, 0.5, 0.9], "k": 5}`)

	taskId := uint64(time.Now().UnixNano())

	// Construct and send payload
	header := make([]byte, 20)
	copy(header[:4], "MZ02")
	binary.BigEndian.PutUint64(header[4:12], taskId)
	binary.BigEndian.PutUint32(header[12:16], uint32(len(wasmBytes)))
	binary.BigEndian.PutUint32(header[16:20], uint32(len(paramBytes)))

	s.Write(header)
	s.Write(wasmBytes)
	s.Write(paramBytes)

	fmt.Println(" -> Payload sent! Waiting for execution results...")
	io.Copy(os.Stdout, s)

	n.done <- true // Signal main loop to exit
}

func main() {
	// Force the sender to use IPv4 localhost as well
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		fmt.Printf("Fatal host error: %v\n", err)
		return
	}
	defer h.Close()

	ctx := context.Background()
	done := make(chan bool)

	// Start mDNS
	rendezvous := "mesh-zero-local-v1"
	notifee := &senderNotifee{h: h, ctx: ctx, done: done}

	mdnsService := mdns.NewMdnsService(h, rendezvous, notifee)
	if err := mdnsService.Start(); err != nil {
		fmt.Printf("Fatal mDNS error: %v\n", err)
		return
	}

	fmt.Println("Scanning local network for Mesh-Zero workers...")
	<-done // Wait until payload executes successfully
	fmt.Println("Task complete. Shutting down sender.")
}
