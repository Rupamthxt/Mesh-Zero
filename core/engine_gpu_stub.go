//go:build gpu && !darwin
// +build gpu,!darwin

package core

import "fmt"

func init() {
	fmt.Println("GPU acceleration is only supported on macOS (Darwin) in this version.")
}
