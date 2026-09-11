//go:build !windows

package sandbox

import (
	"fmt"
	"math"
	"syscall"
)

func controllerFilesystemFreeBytes() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, fmt.Errorf("measure controller filesystem free space: %w", err)
	}
	available := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize == 0 || available <= uint64(math.MaxInt64)/blockSize {
		return int64(available * blockSize), nil
	}
	return math.MaxInt64, nil
}
