//go:build windows && amd64

#include "textflag.h"

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
