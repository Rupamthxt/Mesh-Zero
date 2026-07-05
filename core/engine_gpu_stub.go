//go:build gpu && !darwin && !linux && !windows

package core

import "fmt"

func init() {
	fmt.Println("GPU acceleration is only supported on macOS (Darwin), linux, and Windows in this version.")
}
