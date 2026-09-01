//go:build windows && amd64

package goja

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	memCommit  = 0x1000
	memReserve = 0x2000
	memRelease = 0x8000

	pageReadWrite        = 0x04
	pageExecuteRead      = 0x20
	pageExecuteReadWrite = 0x40
)

type nativeExecutableMemory struct {
	address  atomic.Uintptr
	size     uintptr
	close    sync.Once
	closeErr error
}

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procVirtualAlloc          = kernel32.NewProc("VirtualAlloc")
	procVirtualProtect        = kernel32.NewProc("VirtualProtect")
	procVirtualFree           = kernel32.NewProc("VirtualFree")
	procVirtualQuery          = kernel32.NewProc("VirtualQuery")
	procFlushInstructionCache = kernel32.NewProc("FlushInstructionCache")
	procGetCurrentProcess     = kernel32.NewProc("GetCurrentProcess")
)

func allocateNativeExecutable(code []byte) (*nativeExecutableMemory, error) {
	if len(code) == 0 {
		return nil, errors.New("cannot allocate empty native trace")
	}
	size := uintptr(len(code))
	address, _, callErr := procVirtualAlloc.Call(0, size, memCommit|memReserve, pageReadWrite)
	if address == 0 {
		return nil, fmt.Errorf("VirtualAlloc(PAGE_READWRITE): %w", callErr)
	}
	freeOnError := func() {
		procVirtualFree.Call(address, 0, memRelease)
	}
	copyNativeTraceBytes(address, &code[0], size)
	runtime.KeepAlive(code)
	var oldProtect uint32
	ok, _, callErr := procVirtualProtect.Call(address, size, pageExecuteRead, uintptr(unsafe.Pointer(&oldProtect)))
	if ok == 0 {
		freeOnError()
		return nil, fmt.Errorf("VirtualProtect(PAGE_EXECUTE_READ): %w", callErr)
	}
	if oldProtect != pageReadWrite {
		freeOnError()
		return nil, fmt.Errorf("VirtualProtect replaced unexpected protection %#x", oldProtect)
	}
	process, _, callErr := procGetCurrentProcess.Call()
	if process == 0 {
		freeOnError()
		return nil, fmt.Errorf("GetCurrentProcess: %w", callErr)
	}
	ok, _, callErr = procFlushInstructionCache.Call(process, address, size)
	if ok == 0 {
		freeOnError()
		return nil, fmt.Errorf("FlushInstructionCache: %w", callErr)
	}
	memory := &nativeExecutableMemory{size: size}
	memory.address.Store(address)
	runtime.SetFinalizer(memory, finalizeNativeExecutableMemory)
	return memory, nil
}

func finalizeNativeExecutableMemory(memory *nativeExecutableMemory) {
	_ = memory.release()
}

// release is idempotent and exists for focused tests. Production ownership is
// the Runtime tier state; its finalizer releases the RX region once unreachable.
func (m *nativeExecutableMemory) release() error {
	if m == nil {
		return nil
	}
	m.close.Do(func() {
		address := m.address.Load()
		if address == 0 {
			return
		}
		ok, _, callErr := procVirtualFree.Call(address, 0, memRelease)
		if ok == 0 {
			m.closeErr = fmt.Errorf("VirtualFree(MEM_RELEASE): %w", callErr)
			return
		}
		m.address.Store(0)
	})
	return m.closeErr
}

type memoryBasicInformation struct {
	baseAddress       uintptr
	allocationBase    uintptr
	allocationProtect uint32
	_                 uint32
	regionSize        uintptr
	state             uint32
	protect           uint32
	type_             uint32
	_                 uint32
}

func (m *nativeExecutableMemory) protection() (uint32, error) {
	if m == nil || m.address.Load() == 0 {
		return 0, errors.New("native executable memory is released")
	}
	var info memoryBasicInformation
	written, _, callErr := procVirtualQuery.Call(m.address.Load(), uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if written == 0 {
		return 0, fmt.Errorf("VirtualQuery: %w", callErr)
	}
	return info.protect, nil
}

func (m *nativeExecutableMemory) bytes() []byte {
	if m == nil || m.address.Load() == 0 {
		return nil
	}
	result := make([]byte, m.size)
	readNativeTraceBytes(&result[0], m.address.Load(), m.size)
	runtime.KeepAlive(m)
	return result
}
