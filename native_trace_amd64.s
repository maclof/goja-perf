//go:build (windows || linux) && amd64

#include "textflag.h"

// runNativeTrace bridges Go's ABI to the private JIT ABI. The generated block
// receives *nativeTraceFrame in R10, uses only registers that are volatile in
// both Windows and System V AMD64, makes no calls, and returns a nativeTraceExit
// in EAX.
TEXT ·runNativeTrace(SB), NOSPLIT, $0-20
	MOVQ code+0(FP), AX
	MOVQ frame+8(FP), R10
	CALL AX
	MOVL AX, ret+16(FP)
	RET

// These bounded byte copies avoid manufacturing Go slices that point at
// VirtualAlloc memory. Neither helper retains a Go pointer.
TEXT ·copyNativeTraceBytes(SB), NOSPLIT, $0-24
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ size+16(FP), CX
	REP; MOVSB
	RET

TEXT ·readNativeTraceBytes(SB), NOSPLIT, $0-24
	MOVQ dst+0(FP), DI
	MOVQ src+8(FP), SI
	MOVQ size+16(FP), CX
	REP; MOVSB
	RET
