package core

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
	h         host.Host
	ctx       context.Context
	wasmPath  string
	inputPath string
	done      chan bool
	mu        sync.Mutex
	hasSent   bool
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

	if len(pi.Addrs) == 0 {
		return
	}

	fmt.Printf("\n[mDNS] Found Worker: %s\n", pi.ID)
	fmt.Printf(" -> Available IPs: %v\n", pi.Addrs)
	fmt.Printf("\n[mDNS] Attempting Worker: %s\n", pi.ID)

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
		s.Close()
		n.mu.Unlock()
		return
	}
	n.hasSent = true
	n.mu.Unlock()

	wasmFile := n.wasmPath
	wasmBytes, err := os.ReadFile(wasmFile)
	if err != nil {
		fmt.Printf(" -> FATAL: Could not read task.wasm: %v\n", err)
		n.done <- true
		return
	}

	inputFile := n.inputPath
	paramBytes, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Printf(" FATAL: Could not read input file %s\n", inputFile)
		n.done <- true
		return
	}

	taskId := uint64(time.Now().UnixNano())

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

	n.done <- true
}

func RunSender(wasmPath, inputPath string) {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		fmt.Printf("Fatal host error: %v\n", err)
		return
	}
	defer h.Close()

	ctx := context.Background()
	done := make(chan bool)

	rendezvous := "mesh-zero-local-v1"
	notifee := &senderNotifee{h: h, ctx: ctx, done: done, wasmPath: wasmPath, inputPath: inputPath}

	mdnsService := mdns.NewMdnsService(h, rendezvous, notifee)
	if err := mdnsService.Start(); err != nil {
		fmt.Printf("Fatal mDNS error: %v\n", err)
		return
	}

	fmt.Println("Scanning local network for Mesh-Zero workers...")
	<-done
	fmt.Println("Task complete. Shutting down sender.")
}
