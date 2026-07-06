//go:build gpu && (linux || windows)

package core

import (
	"context"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

var (
	cudaInitialized bool
	cudaMutex       sync.Mutex
)

func init() {
	currentNodeCapabilities.HasGPU = detectCudaPlatform()
	DefaultHooks = append(DefaultHooks, registerGPUHook)
}

func registerGPUHook(ctx context.Context, r wazero.Runtime) error {
	_, err := r.NewHostModuleBuilder("meshzero_gpu").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, shaderPtr, shaderLen, entryPtr, entryLen, dataPtr, dataLen, threadsPerGrid uint32) {
			if dataLen == 0 || threadsPerGrid == 0 {
				return
			}

			shaderBytes, ok := mod.Memory().Read(shaderPtr, shaderLen)
			if !ok {
				return
			}
			shaderSource := string(shaderBytes)

			entryBytes, ok := mod.Memory().Read(entryPtr, entryLen)
			if !ok {
				return
			}
			entryPoint := string(entryBytes)

			dataBytes, ok := mod.Memory().Read(dataPtr, dataLen)
			if !ok {
				return
			}

			runGPUCompute(shaderSource, entryPoint, dataBytes, threadsPerGrid, mod, dataPtr)
		}).
		Export("gpu_run_compute_shader").
		Instantiate(ctx)

	return err
}

// simulateParallelCPU acts as a high-performance multithreaded simulation when CUDA is not present
func simulateParallelCPU(dataBytes []byte, threadsPerGrid uint32, mod api.Module, dataPtr uint32) {
	var wg sync.WaitGroup
	chunkSize := (len(dataBytes) + int(threadsPerGrid) - 1) / int(threadsPerGrid)
	if chunkSize == 0 {
		chunkSize = 1
	}

	output := make([]byte, len(dataBytes))
	copy(output, dataBytes)

	for i := 0; i < int(threadsPerGrid); i++ {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()
			start := threadID * chunkSize
			end := start + chunkSize
			if end > len(output) {
				end = len(output)
			}
			for j := start; j < end; j++ {
				// Mock compute operation: simple parallel visual hash transformation
				output[j] = output[j] ^ byte((threadID*31)&0xFF)
			}
		}(i)
	}
	wg.Wait()
	mod.Memory().Write(dataPtr, output)
}
