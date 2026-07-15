package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

func executeWasm(ctx context.Context, wasmBytes []byte, paramBytes []byte, hooks []ExtensionHook, out io.Writer) (time.Duration, error) {
	startTime := time.Now()

	timeoutSeconds := 5
	if customTimeoutStr := os.Getenv("MESH_TASK_TIMEOUT"); customTimeoutStr != "" {
		if t, err := strconv.Atoi(customTimeoutStr); err == nil && t > 0 {
			timeoutSeconds = t
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	pages := uint32(currentNodeCapabilities.MaxRAm / 65536)
	if pages == 0 {
		pages = 8192 // Fallback to 512MB (8192 pages)
	}
	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(pages)
	r := wazero.NewRuntimeWithConfig(ctx, config)
	defer r.Close(timeoutCtx)

	wasi_snapshot_preview1.MustInstantiate(ctx, r)

	for _, hook := range hooks {
		if err := hook(ctx, r); err != nil {
			fmt.Fprintf(out, "Failed to load extension hook: %v\n", err)
			return time.Since(startTime), err
		}
	}

	compiledMod, err := r.CompileModule(timeoutCtx, wasmBytes)
	if err != nil {
		fmt.Fprintf(out, "Compilation error: %v\n", err)
		return time.Since(startTime), err
	}

	mod, err := r.InstantiateModule(timeoutCtx, compiledMod, wazero.NewModuleConfig().
		WithStdout(out).
		WithStderr(out).
		WithStdin(bytes.NewReader(paramBytes)))

	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			fmt.Fprintf(out, "[SYSTEM KILL] Task exceeded %d-second execution limit.\n", timeoutSeconds)
		} else {
			fmt.Fprintf(out, "Execution failed: %v\n", err)
		}
		return time.Since(startTime), err
	}
	mod.Close(timeoutCtx)
	return time.Since(startTime), nil
}
