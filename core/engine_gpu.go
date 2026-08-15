//go:build gpu && darwin
// +build gpu,darwin

package core

/*
#cgo LDFLAGS: -framework Metal -framework Foundation
#include <stdlib.h>
void RunMetalCompute(const char* shaderSource, const char* entryPoint, void* data, int dataSize, int threadsPerGrid);
*/
import "C"

import (
	"context"
	"unsafe"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func init() {
	currentNodeCapabilities.HasGPU = true
	DefaultHooks = append(DefaultHooks, registerGPUHook)
}

func registerGPUHook(ctx context.Context, r wazero.Runtime) error {
	_, err := r.NewHostModuleBuilder("emdash_gpu").
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

			// Copy to a local buffer for CGO safe access
			tempData := make([]byte, dataLen)
			copy(tempData, dataBytes)

			cShader := C.CString(shaderSource)
			defer C.free(unsafe.Pointer(cShader))

			cEntry := C.CString(entryPoint)
			defer C.free(unsafe.Pointer(cEntry))

			C.RunMetalCompute(cShader, cEntry, unsafe.Pointer(&tempData[0]), C.int(dataLen), C.int(threadsPerGrid))

			mod.Memory().Write(dataPtr, tempData)
		}).
		Export("gpu_run_compute_shader").
		Instantiate(ctx)

	return err
}