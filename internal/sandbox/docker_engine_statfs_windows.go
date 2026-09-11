//go:build windows

package sandbox

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func controllerFilesystemFreeBytes() (int64, error) {
	wd, err := os.Getwd()
	if err != nil {
		return 0, fmt.Errorf("measure controller filesystem free space: %w", err)
	}
	root := filepath.VolumeName(wd)
	if root == "" {
		root = "C:"
	}
	path, err := syscall.UTF16PtrFromString(root + "\\")
	if err != nil {
		return 0, fmt.Errorf("measure controller filesystem free space: %w", err)
	}
	var free, total, totalFree uint64
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	r1, _, callErr := proc.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
	if r1 == 0 {
		return 0, fmt.Errorf("measure controller filesystem free space: %w", callErr)
	}
	if free > uint64(math.MaxInt64) {
		return math.MaxInt64, nil
	}
	return int64(free), nil
}
