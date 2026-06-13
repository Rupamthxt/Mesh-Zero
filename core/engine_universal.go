//go:build !gpu
// +build !gpu

package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func init() {
	fmt.Println("GPU support is not enabled. Tasks requiring GPU will not be executed.")
}

func executeWasm(ctx context.Context, wasmBytes []byte, paramBytes []byte, hooks []ExtensionHook, out io.Writer) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(100)
	r := wazero.NewRuntimeWithConfig(ctx, config)
	defer r.Close(timeoutCtx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	for _, hook := range hooks {
		if err := hook(ctx, r); err != nil {
			fmt.Fprintf(out, "Failed to load extension hook: %v\n", err)
			return
		}
	}

	compiledMod, err := r.CompileModule(timeoutCtx, wasmBytes)
	if err != nil {
		fmt.Fprintf(out, "Compilation error: %v\n", err)
		return
	}

	mod, err := r.InstantiateModule(timeoutCtx, compiledMod, wazero.NewModuleConfig().
		WithStdout(out).
		WithStderr(out).
		WithStdin(bytes.NewReader(paramBytes)))

	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(out, "[SYSTEM KILL] Task exceeded 5-second execution limit.\n")
		} else {
			fmt.Fprintf(out, "Execution failed: %v\n", err)
		}
		return
	}
	mod.Close(timeoutCtx)
}
