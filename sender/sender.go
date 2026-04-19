// sender.go
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run sender.go <worker-multiaddr>")
		return
	}

	h, _ := libp2p.New(libp2p.NoListenAddrs)
	defer h.Close()

	kDHT, _ := dht.New(context.Background(), h)
	kDHT.Bootstrap(context.Background())

	routingDiscovery := routing.NewRoutingDiscovery(kDHT)
	rendezvousString := "mesh-zero-compute-pool-v1"

	fmt.Println("Searching for Mesh-Zero workers...")

	peerChan, _ := routingDiscovery.FindPeers(context.Background(), rendezvousString)

	for peer := range peerChan {
		if peer.ID == h.ID() {
			continue
		}

		fmt.Printf("Found Worker Node: %s! Connecting...\n", peer.ID)

		h.Connect(context.Background(), peer)

		s, _ := h.NewStream(context.Background(), peer.ID, "/mesh-zero/task/1.0.0")
		defer s.Close()

		wasmBytes, err := os.ReadFile("task.wasm")
		if err != nil {
			fmt.Printf("FATAL: Could not read task.wasm: %v\n", err)
			return
		}
		if len(wasmBytes) < 8 {
			fmt.Printf("FATAL: task.wasm is too small (%d bytes). Not a valid WASM.\n", len(wasmBytes))
			return
		}

		paramBytes := []byte(`{"vector": [0.1, 0.5, 0.9], "k": 5}`)

		fmt.Printf("[SENDER] WASM Size: %d bytes\n", len(wasmBytes))
		fmt.Printf("[SENDER] Param Size: %d bytes\n", len(paramBytes))

		header := make([]byte, 12)
		copy(header[:4], "MZ01") // Magic Number
		binary.BigEndian.PutUint32(header[4:8], uint32(len(wasmBytes)))
		binary.BigEndian.PutUint32(header[8:12], uint32(len(paramBytes)))

		// 8. Blast the Payload over the stream
		s.Write(header)
		s.Write(wasmBytes)
		s.Write(paramBytes)

		// 9. Read the stream to see the WASM execution output!
		fmt.Println("Payload sent. Waiting for execution results...")
		io.Copy(os.Stdout, s)

		break
	}

}
