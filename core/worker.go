package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type ExtensionHook func(context.Context, wazero.Runtime) error

type Worker struct {
	Host  host.Host
	Hooks []ExtensionHook
}

type discoveryNotifee struct{}

func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	fmt.Printf("[mDNS] Worker saw peer on network: %s\n", pi.ID)
}

var completedTasks = make(map[uint64]bool)
var taskMu sync.Mutex

func (w *Worker) Start(ctx context.Context) error {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"))
	if err != nil {
		fmt.Printf("Fatal host error: %v\n", err)
		return nil
	}
	defer h.Close()

	h.SetStreamHandler("/mesh-zero/task/1.0.0", func(s network.Stream) {
		w.handleTaskStream(ctx, s)
	})

	rendezvous := "mesh-zero-local-v1"
	mdnsService := mdns.NewMdnsService(h, rendezvous, &discoveryNotifee{})
	if err := mdnsService.Start(); err != nil {
		fmt.Printf("Fatal mDNS error: %v\n", err)
		return nil
	}

	fmt.Printf("Worker Node %s listening on Localhost. Waiting for tasks...\n", h.ID())
	select {}
}

func (w *Worker) handleTaskStream(ctx context.Context, s network.Stream) {
	defer s.Close()

	header := make([]byte, 20)
	if _, err := io.ReadFull(s, header); err != nil {
		return
	}
	if string(header[:4]) != "MZ02" {
		return
	}

	taskID := binary.BigEndian.Uint64(header[4:12])
	wasmLen := binary.BigEndian.Uint32(header[12:16])
	paramLen := binary.BigEndian.Uint32(header[16:20])

	taskMu.Lock()
	if completedTasks[taskID] {
		fmt.Printf("Worker already executed Task %d. Ignoring.\n", taskID)
		taskMu.Unlock()
		return
	}
	completedTasks[taskID] = true
	taskMu.Unlock()

	wasmBin := make([]byte, wasmLen)
	io.ReadFull(s, wasmBin)

	paramBin := make([]byte, paramLen)
	io.ReadFull(s, paramBin)

	fmt.Printf("\n[WORKER] Task Received! WASM: %dB, Params: %dB\n", wasmLen, paramLen)
	w.runWasmTask(ctx, wasmBin, paramBin, s)
}

func (w *Worker) runWasmTask(ctx context.Context, wasmBytes []byte, paramBytes []byte, out io.Writer) {
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	for _, hook := range w.Hooks {
		if err := hook(ctx, r); err != nil {
			fmt.Fprintf(out, "Failed to load extension hook: %v\n", err)
			return
		}
	}

	compiledMod, err := r.CompileModule(ctx, wasmBytes)
	if err != nil {
		fmt.Fprintf(out, "Compilation error: %v\n", err)
		return
	}

	config := wazero.NewModuleConfig().
		WithStdin(bytes.NewReader(paramBytes)).
		WithStdout(out).
		WithStderr(os.Stderr)

	_, err = r.InstantiateModule(ctx, compiledMod, config)
	if err != nil {
		fmt.Fprintf(out, "Execution error: %v\n", err)
	}

}
