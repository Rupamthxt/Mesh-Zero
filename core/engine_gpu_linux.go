//go:build gpu && linux

package core

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/tetratelabs/wazero/api"
)

func detectCudaPlatform() bool {
	cudaMutex.Lock()
	defer cudaMutex.Unlock()

	// Check for libcuda.so in standard Linux locations (including WSL2 and Arch/Fedora)
	libPaths := []string{
		"/usr/lib/x86_64-linux-gnu/libcuda.so.1",
		"/usr/lib/x86_64-linux-gnu/libcuda.so",
		"/usr/local/cuda/lib64/libcuda.so",
		"/usr/lib/libcuda.so.1",
		"/usr/lib/libcuda.so",
		"/usr/lib/wsl/lib/libcuda.so.1",
		"/usr/lib/wsl/lib/libcuda.so",
	}
	found := false
	var checkedPaths []string
	for _, path := range libPaths {
		if _, err := os.Stat(path); err == nil {
			found = true
			fmt.Printf("[GPU DETECT] Found CUDA library at: %s\n", path)
			break
		}
		checkedPaths = append(checkedPaths, path)
	}

	// Fallback check: try running nvidia-smi to confirm active drivers
	if !found {
		smiPaths := []string{"nvidia-smi", "/usr/bin/nvidia-smi", "/usr/sbin/nvidia-smi"}
		var lastSmiErr error
		for _, smiPath := range smiPaths {
			cmd := exec.Command(smiPath)
			if err := cmd.Run(); err == nil {
				found = true
				fmt.Printf("[GPU DETECT] Found active GPU driver via %s\n", smiPath)
				break
			} else {
				lastSmiErr = err
			}
		}
		if !found {
			fmt.Printf("[GPU DETECT] CUDA libraries not found in standard paths: %v\n", checkedPaths)
			fmt.Printf("[GPU DETECT] nvidia-smi execution failed: %v\n", lastSmiErr)
		}
	}

	cudaInitialized = found
	return found
}

func runGPUCompute(shaderSource, entryPoint string, dataBytes []byte, threadsPerGrid uint32, mod api.Module, dataPtr uint32) {
	// Fallback to high-performance parallel CPU multi-threading simulator on Linux
	simulateParallelCPU(dataBytes, threadsPerGrid, mod, dataPtr)
}
