package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"unsafe"
)

//go:wasmimport meshzero_gpu gpu_run_compute_shader
//go:noescape
func gpu_run_compute_shader(shaderSourcePtr uint32, shaderSourceLen uint32, entryPointPtr uint32, entryPointLen uint32, dataPtr uint32, dataLen uint32, threadsPerGrid uint32)

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Printf("Error reading input: %v\n", err)
		return
	}

	if len(input)%4 != 0 {
		fmt.Println("Error: input must be a multiple of 4 bytes (float32 elements)")
		return
	}

	numElements := len(input) / 4
	floats := make([]float32, numElements)
	for i := 0; i < numElements; i++ {
		bits := binary.LittleEndian.Uint32(input[i*4 : (i+1)*4])
		floats[i] = math.Float32frombits(bits)
	}

	fmt.Printf("[WASM GPU TASK] Input floats: %v\n", floats)

	shaderSource := `
#include <metal_stdlib>
using namespace metal;
kernel void multiply_by_five(device float* data [[buffer(0)]], uint id [[thread_position_in_grid]]) {
    data[id] = data[id] * 5.0;
}
`
	entryPoint := "multiply_by_five"

	shaderBytes := []byte(shaderSource)
	entryBytes := []byte(entryPoint)

	shaderPtr := uint32(uintptr(unsafe.Pointer(&shaderBytes[0])))
	entryPtr := uint32(uintptr(unsafe.Pointer(&entryBytes[0])))
	dataPtr := uint32(uintptr(unsafe.Pointer(&floats[0])))

	gpu_run_compute_shader(
		shaderPtr, uint32(len(shaderBytes)),
		entryPtr, uint32(len(entryBytes)),
		dataPtr, uint32(len(floats)*4),
		uint32(len(floats)),
	)

	fmt.Printf("[WASM GPU TASK] Output floats: %v\n", floats)
}
