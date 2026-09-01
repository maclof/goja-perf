//go:build linux && amd64

package goja

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	pageReadWrite        uint32 = syscall.PROT_READ | syscall.PROT_WRITE
	pageExecuteRead      uint32 = syscall.PROT_READ | syscall.PROT_EXEC
	pageExecuteReadWrite uint32 = syscall.PROT_READ | syscall.PROT_WRITE | syscall.PROT_EXEC
)

type nativeExecutableMemory struct {
	address  atomic.Uintptr
	mapping  []byte
	close    sync.Once
	closeErr error
}

func allocateNativeExecutable(code []byte) (*nativeExecutableMemory, error) {
	if len(code) == 0 {
		return nil, errors.New("cannot allocate empty native trace")
	}
	mapping, err := syscall.Mmap(-1, 0, len(code), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return nil, fmt.Errorf("mmap(PROT_READ|PROT_WRITE): %w", err)
	}
	copy(mapping, code)
	runtime.KeepAlive(code)
	if err = syscall.Mprotect(mapping, syscall.PROT_READ|syscall.PROT_EXEC); err != nil {
		_ = syscall.Munmap(mapping)
		return nil, fmt.Errorf("mprotect(PROT_READ|PROT_EXEC): %w", err)
	}
	memory := &nativeExecutableMemory{mapping: mapping}
	memory.address.Store(uintptr(unsafe.Pointer(&mapping[0])))
	runtime.SetFinalizer(memory, finalizeNativeExecutableMemory)
	return memory, nil
}

func finalizeNativeExecutableMemory(memory *nativeExecutableMemory) {
	_ = memory.release()
}

// release is idempotent and exists for focused tests. Production ownership is
// the Runtime tier state; its finalizer unmaps the RX region once unreachable.
func (m *nativeExecutableMemory) release() error {
	if m == nil {
		return nil
	}
	m.close.Do(func() {
		if m.address.Load() == 0 {
			return
		}
		if err := syscall.Munmap(m.mapping); err != nil {
			m.closeErr = fmt.Errorf("munmap: %w", err)
			return
		}
		m.mapping = nil
		m.address.Store(0)
	})
	return m.closeErr
}

func (m *nativeExecutableMemory) protection() (uint32, error) {
	if m == nil || m.address.Load() == 0 {
		return 0, errors.New("native executable memory is released")
	}
	file, err := os.Open("/proc/self/maps")
	if err != nil {
		return 0, fmt.Errorf("open /proc/self/maps: %w", err)
	}
	defer file.Close()
	address := uint64(m.address.Load())
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		bounds := strings.SplitN(fields[0], "-", 2)
		if len(bounds) != 2 {
			continue
		}
		start, startErr := strconv.ParseUint(bounds[0], 16, 64)
		end, endErr := strconv.ParseUint(bounds[1], 16, 64)
		if startErr != nil || endErr != nil || address < start || address >= end {
			continue
		}
		var protection uint32
		permissions := fields[1]
		if len(permissions) > 0 && permissions[0] == 'r' {
			protection |= syscall.PROT_READ
		}
		if len(permissions) > 1 && permissions[1] == 'w' {
			protection |= syscall.PROT_WRITE
		}
		if len(permissions) > 2 && permissions[2] == 'x' {
			protection |= syscall.PROT_EXEC
		}
		return protection, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan /proc/self/maps: %w", err)
	}
	return 0, fmt.Errorf("native mapping %#x not found in /proc/self/maps", address)
}

func (m *nativeExecutableMemory) bytes() []byte {
	if m == nil || m.address.Load() == 0 {
		return nil
	}
	result := append([]byte(nil), m.mapping...)
	runtime.KeepAlive(m)
	return result
}
