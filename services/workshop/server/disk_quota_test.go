package server

import (
	"easygo-agent/services/workshop/workshop"
	"fmt"
	"testing"
)

func TestQuotaDomainErrorsAreDistinctAndBounded(t *testing.T) {
	quota := &workshop.DiskQuotaError{DiskUsage: workshop.DiskUsage{UsedBytes: 17000000, UsedFiles: 42}, LimitBytes: 16777216, LimitFiles: 1000}
	got := domainError(fmt.Errorf("must-not-leak/private/path: %w", quota))
	if got.Code != -32014 || got.Data.Code != "disk_quota_exceeded" || got.Message != quota.Error() {
		t.Fatal("quota domain error lost stats or leaked wrapper")
	}
	scan := domainError(workshop.ErrDiskQuotaScanFailed)
	if scan.Code != -32015 || scan.Data.Code != "disk_quota_scan_failed" {
		t.Fatal("scan failure is not fail-closed")
	}
}
