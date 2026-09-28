package workshop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

var ErrDiskQuotaExceeded = errors.New("disk_quota_exceeded")
var ErrDiskQuotaScanFailed = errors.New("disk_quota_scan_failed")

type DiskUsage struct {
	UsedBytes int64 `json:"used_bytes"`
	UsedFiles int64 `json:"used_files"`
}

// Counters at the first exceeded bound are a lower bound, not a full-tree census.
// Error text is persisted as the run error and exposed without filesystem paths.
type DiskQuotaError struct {
	DiskUsage
	LimitBytes int64 `json:"limit_bytes"`
	LimitFiles int64 `json:"limit_files"`
}

func (e *DiskQuotaError) Error() string {
	return fmt.Sprintf("disk_quota_exceeded used_bytes=%d used_files=%d limit_bytes=%d limit_files=%d", e.UsedBytes, e.UsedFiles, e.LimitBytes, e.LimitFiles)
}
func (e *DiskQuotaError) Unwrap() error { return ErrDiskQuotaExceeded }

func (r *DockerRunner) CheckDiskQuota(ctx context.Context, workspace string) error {
	rel, err := filepath.Rel(filepath.Join(r.root, "workspaces"), workspace)
	if err != nil || strings.Contains(rel, string(filepath.Separator)) {
		return ErrDiskQuotaScanFailed
	}
	if _, err = uuid.Parse(rel); err != nil {
		return ErrDiskQuotaScanFailed
	}
	if err = noSymlinks(workspace); err != nil {
		return ErrDiskQuotaScanFailed
	}
	_, err = scanDiskUsage(ctx, workspace, r.cfg.DiskQuotaBytes, r.cfg.DiskQuotaFiles)
	return err
}
