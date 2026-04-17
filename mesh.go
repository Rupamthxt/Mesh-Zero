package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/libp2p/go-libp2p"
	// "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/udp/0/quic-v1"),
		libp2p.EnableRelay(),
	)

	if err != nil {
		panic(err)
	}

	addr := fmt.Sprintf("%s/p2p/%s", h.Addrs()[0].String(), h.ID().String())
	fmt.Printf("Worker Node listening on: %s\n", addr)

	h.SetStreamHandler("/mesh-zero/task/1.0.0", func(s network.Stream) {
		handleTaskStream(ctx, s)
	})

	fmt.Printf("Node started with ID: %s\n", h.ID())

	<-ctx.Done()
	fmt.Println("\nShutting down Mesh-Zero node...")
	h.Close()
}

// func handleTaskStream(ctx context.Context, s network.Stream) {
// 	defer s.Close()

// 	// Read Header : Verification(4) + Wasm Length(4) + Param length(4)
// 	header := make([]byte, 12)

// 	if _, err := io.ReadFull(s, header); err != nil {
// 		return
// 	}

// 	if string(header[:4]) != "MZ01" {
// 		return
// 	}

// 	wasmLen := binary.BigEndian.Uint32(header[4:8])
// 	paramLen := binary.BigEndian.Uint32(header[8:12])

// 	wasmBin := make([]byte, wasmLen)
// 	io.ReadFull(s, wasmBin)

// 	paramBin := make([]byte, paramLen)
// 	io.ReadFull(s, paramBin)

// 	runWasmTask(ctx, wasmBin, paramBin, s)
// }

// func runWasmTask(ctx context.Context, wasm []byte, param []byte, out io.Writer) {
// 	r := wazero.NewRuntime(ctx)
// 	defer r.Close(ctx)

// 	wasi_snapshot_preview1.MustInstantiate(ctx, r)

// 	compiledMod, err := r.CompileModule(ctx, wasm)
// 	if err != nil {
// 		fmt.Fprintf(out, "Compilation error: %v\n", err)
// 		return
// 	}

// 	config := wazero.NewModuleConfig().WithStdin(bytes.NewReader(param)).WithStdout(out).WithEnv("NODE_ID", "Local-01")

// 	_, err = r.InstantiateModule(ctx, compiledMod, config)
// 	if err != nil {
// 		fmt.Printf("Error instantiating Wasm module: %v\n", err)
// 		return
// 	}
// }

func handleTaskStream(ctx context.Context, s network.Stream) {
	defer s.Close()

	header := make([]byte, 12)
	if _, err := io.ReadFull(s, header); err != nil {
		fmt.Fprintf(s, "Stream error: Failed to read header: %v\n", err)
		return
	}

	if string(header[:4]) != "MZ01" {
		fmt.Fprintf(s, "Protocol error: Expected MZ01, got %s\n", string(header[:4]))
		return
	}

	wasmLen := binary.BigEndian.Uint32(header[4:8])
	paramLen := binary.BigEndian.Uint32(header[8:12])

	wasmBin := make([]byte, wasmLen)
	if _, err := io.ReadFull(s, wasmBin); err != nil {
		fmt.Fprintf(s, "Stream error: Failed to read WASM: %v\n", err)
		return
	}

	paramBin := make([]byte, paramLen)
	if _, err := io.ReadFull(s, paramBin); err != nil {
		fmt.Fprintf(s, "Stream error: Failed to read Params: %v\n", err)
		return
	}

	fmt.Printf("[WORKER] Received Header Magic: %s\n", string(header[:4]))
	fmt.Printf("[WORKER] Unpacked WASM Size: %d bytes\n", len(wasmBin))
	fmt.Printf("[WORKER] Unpacked Param Size: %d bytes\n", len(paramBin))

	if len(wasmBin) >= 4 {
		fmt.Printf("[WORKER] WASM Signature: %x\n", wasmBin[:4])
	}

	runWasmTask(ctx, wasmBin, paramBin, s)
}

func runWasmTask(ctx context.Context, wasmBytes []byte, params []byte, out io.Writer) {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	compiledMod, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		fmt.Fprintf(out, "Compilation error: %v\n", err)
		return
	}

	config := wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(params)).
		WithStdout(out).
		WithStderr(os.Stderr)

	_, err = r.InstantiateModule(ctx, compiledMod, config)
	if err != nil {
		fmt.Fprintf(out, "Execution error: %v\n", err)
	}
}
