package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"

	// "github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

type ExtensionHook func(context.Context, wazero.Runtime) error

type Worker struct {
	Host  host.Host
	Hooks []ExtensionHook
}

// type discoveryNotifee struct{}

// func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
// 	fmt.Printf("[mDNS] Worker saw peer on network: %s\n", pi.ID)
// }

var completedTasks = make(map[uint64]bool)
var taskMu sync.Mutex

func (w *Worker) Start(ctx context.Context, enableApi bool, apiPort string) error {

	var relays []peer.AddrInfo
	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerInfo, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err == nil {
			relays = append(relays, *peerInfo)
		}
	}

	var kdht *dht.IpfsDHT
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
		libp2p.NATPortMap(),
		libp2p.EnableAutoRelayWithStaticRelays(relays),
		libp2p.Routing(func(n host.Host) (routing.PeerRouting, error) {
			var dhtErr error
			kdht, dhtErr = dht.New(ctx, n, dht.Mode(dht.ModeAuto))
			return kdht, dhtErr
		}),
	)
	if err != nil {
		fmt.Printf("Fatal host error: %v\n", err)
		return nil
	}
	defer h.Close()
	w.Host = h

	fmt.Println("Connecting to public bootstrap nodes....")
	var wg sync.WaitGroup
	for _, peerAddr := range dht.DefaultBootstrapPeers {
		peerInfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			if err := h.Connect(ctx, pi); err == nil {
				fmt.Printf("[DHT] Connected to bootstrap node: %s\n", pi.ID.String()[:10])
			}
		}(*peerInfo)
	}
	wg.Wait()

	if err = kdht.Bootstrap(ctx); err != nil {
		fmt.Printf("Fatal DHT bootstrap error: %v\n", err)
		return nil
	}
	fmt.Println("[NETWORK] Global DHT Bootstrapped successfully!")

	rendezvous := "mesh-zero-local-v1"
	fmt.Printf("[DHT] Announcing presence to namespace: %s\n", rendezvous)

	routingDiscovery := drouting.NewRoutingDiscovery(kdht)
	dutil.Advertise(ctx, routingDiscovery, rendezvous)

	go func() {
		for {
			peerChan, err := routingDiscovery.FindPeers(ctx, rendezvous)
			if err != nil {
				time.Sleep(time.Second * 10)
				continue
			}

			for pi := range peerChan {
				if pi.ID == h.ID() || len(pi.Addrs) == 0 {
					continue
				}
				if h.Network().Connectedness(pi.ID) != network.Connected {
					// The Tie-Breaker: Prevent simultaneous open collisions
					if h.ID().String() < pi.ID.String() {
						err := h.Connect(ctx, pi)
						if err == nil {
							fmt.Printf("[DHT] Discovered Mesh-Zero node: %s\n", pi.ID.String()[:10])
							h.Peerstore().Put(pi.ID, "rendezvous", rendezvous)
						}
					}
				}
			}
			time.Sleep(time.Second * 30)
		}
	}()

	h.SetStreamHandler("/mesh-zero/task/1.0.0", func(s network.Stream) {
		w.handleTaskStream(ctx, s)
	})

	if enableApi {
		if apiPort == "" {
			apiPort = "8080" // Fallback default
		}
		go w.StartAPIServer(apiPort)
	}

	fmt.Printf("Worker Node %s listening. Waiting for tasks...\n", h.ID())
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
