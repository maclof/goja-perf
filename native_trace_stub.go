//go:build !windows || !amd64

package goja

// nativeTraceCode is deliberately empty on unsupported platforms. Typed Go IR
// remains the exact fallback, so Programs compiled on one platform remain
// portable and shared Programs never contain platform-specific state.
type nativeTraceCode struct{}

func compileNativeTrace(*typedIntLoopTrace) (*nativeTraceCode, error) {
	return nil, nil
}

func (t *typedIntLoopTrace) executeNative(*vm, *programTierState, *typedTraceEntry, *nativeTraceCode, [typedTraceRegisterCount]int64) {
	panic("native trace execution is unavailable on this platform")
}
