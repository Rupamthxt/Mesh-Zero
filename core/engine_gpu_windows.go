//go:build gpu && windows

package core

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/tetratelabs/wazero/api"
)

var (
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

func detectCudaPlatform() bool {
	cudaMutex.Lock()
	defer cudaMutex.Unlock()

	if cudaInitialized {
		return true
	}

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
}

func runGPUCompute(shaderSource, entryPoint string, dataBytes []byte, threadsPerGrid uint32, mod api.Module, dataPtr uint32) {
	if cudaInitialized {
		executeCUDA(shaderSource, entryPoint, dataBytes, threadsPerGrid, mod, dataPtr)
	} else {
		simulateParallelCPU(dataBytes, threadsPerGrid, mod, dataPtr)
	}
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
	var blockDimX uintptr = 256
	gridDimX := uintptr((threadsPerGrid + 255) / 256)

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
