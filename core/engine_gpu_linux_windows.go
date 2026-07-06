//go:build gpu && (linux || windows)

package core

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

var (
	cudaInitialized bool
	cudaMutex       sync.Mutex

	// Windows Lazy DLL Procedures
	nvcuda              *syscall.LazyDLL
	cuInit              *syscall.LazyProc
	cuDeviceGet         *syscall.LazyProc
	cuCtxCreate         *syscall.LazyProc
	cuModuleLoadData    *syscall.LazyProc
	cuModuleGetFunction *syscall.LazyProc
	cuMemAlloc          *syscall.LazyProc
	cuMemcpyHtoD        *syscall.LazyProc
	cuMemcpyDtoH        *syscall.LazyProc
	cuLaunchKernel      *syscall.LazyProc
	cuMemFree           *syscall.LazyProc
	cuCtxSynchronize    *syscall.LazyProc
	cuCtxDestroy        *syscall.LazyProc

	// CUDA Context references
	cudaDevice  uintptr
	cudaContext uintptr
)

func init() {
	currentNodeCapabilities.HasGPU = detectCuda()
	DefaultHooks = append(DefaultHooks, registerGPUHook)
}

func detectCuda() bool {
	cudaMutex.Lock()
	defer cudaMutex.Unlock()

	if cudaInitialized {
		return true
	}

	if runtime.GOOS == "windows" {
		// Load nvcuda.dll dynamically on Windows
		nvcuda = syscall.NewLazyDLL("nvcuda.dll")
		if nvcuda.Load() != nil {
			return false // CUDA driver not installed
		}

		cuInit = nvcuda.NewProc("cuInit")
		cuDeviceGet = nvcuda.NewProc("cuDeviceGet")
		cuCtxCreate = nvcuda.NewProc("cuCtxCreate_v2")
		cuModuleLoadData = nvcuda.NewProc("cuModuleLoadData")
		cuModuleGetFunction = nvcuda.NewProc("cuModuleGetFunction")
		cuMemAlloc = nvcuda.NewProc("cuMemAlloc_v2")
		cuMemcpyHtoD = nvcuda.NewProc("cuMemcpyHtoD_v2")
		cuMemcpyDtoH = nvcuda.NewProc("cuMemcpyDtoH_v2")
		cuLaunchKernel = nvcuda.NewProc("cuLaunchKernel")
		cuMemFree = nvcuda.NewProc("cuMemFree_v2")
		cuCtxSynchronize = nvcuda.NewProc("cuCtxSynchronize")
		cuCtxDestroy = nvcuda.NewProc("cuCtxDestroy")

		// Initialize CUDA Driver API
		r, _, _ := cuInit.Call(0)
		if r != 0 {
			return false
		}

		r, _, _ = cuDeviceGet.Call(uintptr(unsafe.Pointer(&cudaDevice)), 0)
		if r != 0 {
			return false
		}

		r, _, _ = cuCtxCreate.Call(uintptr(unsafe.Pointer(&cudaContext)), 0, cudaDevice)
		if r != 0 {
			return false
		}

		cudaInitialized = true
		fmt.Println("[GPU] NVIDIA CUDA initialized successfully via nvcuda.dll")
		return true
	} else if runtime.GOOS == "linux" {
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

		if found {
			return true
		}
	}

	return false
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

			if runtime.GOOS == "windows" && cudaInitialized {
				// Execute on NVIDIA GPU via CUDA API
				executeCUDA(shaderSource, entryPoint, dataBytes, threadsPerGrid, mod, dataPtr)
			} else {
				// Fallback to high-performance parallel CPU multi-threading simulator on Linux / CPU-only
				simulateParallelCPU(dataBytes, threadsPerGrid, mod, dataPtr)
			}
		}).
		Export("gpu_run_compute_shader").
		Instantiate(ctx)

	return err
}

func executeCUDA(ptxSource, entryPoint string, dataBytes []byte, threadsPerGrid uint32, mod api.Module, dataPtr uint32) {
	// Compile null-terminated strings
	ptxBytes := append([]byte(ptxSource), 0)
	cEntry := append([]byte(entryPoint), 0)

	var cudaModule uintptr
	var cudaFunction uintptr
	var deviceData uintptr

	// 1. Load PTX code into module
	r, _, _ := cuModuleLoadData.Call(uintptr(unsafe.Pointer(&cudaModule)), uintptr(unsafe.Pointer(&ptxBytes[0])))
	if r != 0 {
		fmt.Printf("[GPU ERROR] cuModuleLoadData failed: %d\n", r)
		return
	}
	defer cuCtxDestroy.Call(cudaModule)

	// 2. Get function reference
	r, _, _ = cuModuleGetFunction.Call(uintptr(unsafe.Pointer(&cudaFunction)), cudaModule, uintptr(unsafe.Pointer(&cEntry[0])))
	if r != 0 {
		fmt.Printf("[GPU ERROR] cuModuleGetFunction failed: %d\n", r)
		return
	}

	// 3. Allocate CUDA memory
	dataLen := len(dataBytes)
	r, _, _ = cuMemAlloc.Call(uintptr(unsafe.Pointer(&deviceData)), uintptr(dataLen))
	if r != 0 {
		fmt.Printf("[GPU ERROR] cuMemAlloc failed: %d\n", r)
		return
	}
	defer cuMemFree.Call(deviceData)

	// 4. Copy data to GPU
	r, _, _ = cuMemcpyHtoD.Call(deviceData, uintptr(unsafe.Pointer(&dataBytes[0])), uintptr(dataLen))
	if r != 0 {
		fmt.Printf("[GPU ERROR] cuMemcpyHtoD failed: %d\n", r)
		return
	}

	// 5. Launch kernel
	// Block sizes are standard 256 threads
	var blockDimX uintptr = 256
	gridDimX := uintptr((threadsPerGrid + 255) / 256)

	// Build arguments pointer structure
	args := []uintptr{
		uintptr(unsafe.Pointer(&deviceData)),
		uintptr(unsafe.Pointer(&threadsPerGrid)),
	}

	r, _, _ = cuLaunchKernel.Call(
		cudaFunction,
		gridDimX, 1, 1, // Grid size
		blockDimX, 1, 1, // Block size
		0, // Shared memory bytes
		0, // Stream ID
		uintptr(unsafe.Pointer(&args[0])),
		0,
	)
	if r != 0 {
		fmt.Printf("[GPU ERROR] cuLaunchKernel failed: %d\n", r)
		return
	}

	// 6. Synchronize execution
	cuCtxSynchronize.Call()

	// 7. Copy output data back to host memory
	outputData := make([]byte, dataLen)
	r, _, _ = cuMemcpyDtoH.Call(uintptr(unsafe.Pointer(&outputData[0])), deviceData, uintptr(dataLen))
	if r == 0 {
		mod.Memory().Write(dataPtr, outputData)
	}
}

// simulateParallelCPU acts as a high-performance multithreaded simulation when CUDA is not present
func simulateParallelCPU(dataBytes []byte, threadsPerGrid uint32, mod api.Module, dataPtr uint32) {
	// For prototype simulation: perform parallelized array squaring or standard operations
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

// Convert bytes to hex string helper
func bytesToHex(b []byte) string {
	return hex.EncodeToString(b)
}
