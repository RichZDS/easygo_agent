//go:build !linux

package workshop

import "context"

func scanDiskUsage(context.Context, string, int64, int64) (DiskUsage, error) {
	return DiskUsage{}, ErrDiskQuotaScanFailed
}
