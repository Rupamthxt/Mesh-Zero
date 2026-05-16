//go:build !gpu
// +build !gpu

package core

import "fmt"

func init() {
	fmt.Println("GPU support is not enabled. Tasks requiring GPU will not be executed.")
}

func executeWasm(wasmBytes []byte, paramBytes []byte) {
	
}