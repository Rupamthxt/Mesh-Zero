// sender.go
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run sender.go <worker-multiaddr>")
		return
	}

	h, _ := libp2p.New(libp2p.NoListenAddrs)
	defer h.Close()

	maddr, _ := multiaddr.NewMultiaddr(os.Args[1])
	info, _ := peer.AddrInfoFromP2pAddr(maddr)

	h.Connect(context.Background(), *info)

	s, _ := h.NewStream(context.Background(), info.ID, "/mesh-zero/task/1.0.0")
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
}
