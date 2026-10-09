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
		fmt.Println("[FFI] Emdash residential node daemon is already running.")
		return
	}
	fmt.Println("[FFI] Booting Emdash residential node engine...")
	ctx, cancel := context.WithCancel(context.Background())
	ffiCancel = cancel
	emdashWorker = &core.Worker{
		PricePerTask: 1.0,
	}

	go func() {
		_ = emdashWorker.Start(ctx, true, "8085")
	}()
}

//export StopEmdashDaemon
func StopEmdashDaemon() {
	if ffiCancel != nil {
		fmt.Println("[FFI] Shutting down Emdash residential node engine...")
		ffiCancel()
		emdashWorker = nil
	}
}
