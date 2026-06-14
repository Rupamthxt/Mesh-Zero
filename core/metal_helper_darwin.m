//go:build gpu && darwin
// +build gpu,darwin

#import <Metal/Metal.h>
#import <Foundation/Foundation.h>

void RunMetalCompute(const char* shaderSource, const char* entryPoint, void* data, int dataSize, int threadsPerGrid) {
    @autoreleasepool {
        id<MTLDevice> device = MTLCreateSystemDefaultDevice();
        if (!device) {
            return;
        }
        
        NSError* error = nil;
        NSString* sourceStr = [NSString stringWithUTF8String:shaderSource];
        id<MTLLibrary> library = [device newLibraryWithSource:sourceStr options:nil error:&error];
        if (!library) {
            return;
        }
        
        NSString* entryStr = [NSString stringWithUTF8String:entryPoint];
        id<MTLFunction> function = [library newFunctionWithName:entryStr];
        if (!function) {
            return;
        }
        
        id<MTLComputePipelineState> pipelineState = [device newComputePipelineStateWithFunction:function error:&error];
        if (!pipelineState) {
            return;
        }
        
        id<MTLCommandQueue> commandQueue = [device newCommandQueue];
        id<MTLCommandBuffer> commandBuffer = [commandQueue commandBuffer];
        id<MTLComputeCommandEncoder> computeEncoder = [commandBuffer computeCommandEncoder];
        
        id<MTLBuffer> dataBuffer = [device newBufferWithBytes:data length:dataSize options:MTLResourceStorageModeShared];
        
        [computeEncoder setComputePipelineState:pipelineState];
        [computeEncoder setBuffer:dataBuffer offset:0 atIndex:0];
        
        NSUInteger w = pipelineState.threadExecutionWidth;
        if (w == 0) w = 1;
        MTLSize threadsPerThreadgroup = MTLSizeMake(w, 1, 1);
        MTLSize threadgroupsPerGrid = MTLSizeMake((threadsPerGrid + w - 1) / w, 1, 1);
        
        [computeEncoder dispatchThreadgroups:threadgroupsPerGrid threadsPerThreadgroup:threadsPerThreadgroup];
        [computeEncoder endEncoding];
        
        [commandBuffer commit];
        [commandBuffer waitUntilCompleted];
        
        memcpy(data, [dataBuffer contents], dataSize);
    }
}
