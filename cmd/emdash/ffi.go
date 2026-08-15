//go:build cgo

package main

import (
	"C"
	"context"
	"fmt"

	"github.com/rupamthxt/emdash/core"
)

var emdashWorker *core.Worker
var ffiCancel context.CancelFunc

//export StartEmdashDaemon
func StartEmdashDaemon() {
	if emdashWorker != nil {
		fmt.Println("[FFI] Emdash daemon is already running.")
		return
	}
	fmt.Println("[FFI] Booting Emdash engine from UI...")
	ctx, cancel := context.WithCancel(context.Background())
	ffiCancel = cancel
	emdashWorker = &core.Worker{
		Hooks: core.DefaultHooks,
	}

	go func() {
		emdashWorker.Start(ctx, true, "8080")
	}()
}

//export StopEmdashDaemon
func StopEmdashDaemon() {
	if ffiCancel != nil {
		fmt.Println("[FFI] Shutting down Emdash engine...")
		ffiCancel()
		emdashWorker = nil
	}
}
