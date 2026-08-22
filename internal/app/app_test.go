package app

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestIsIgnorableSyncError 验证终端/控制台无法 flush 时的 Sync 错误应被忽略。
func TestIsIgnorableSyncError(t *testing.T) {
	t.Parallel()

	invalidHandle := &os.PathError{
		Op:   "sync",
		Path: "/dev/stderr",
		Err:  syscall.Errno(6),
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: true},
		{name: "einval", err: syscall.EINVAL, want: true},
		{name: "enotty", err: syscall.ENOTTY, want: true},
		{name: "ebadf", err: syscall.EBADF, want: true},
		{name: "windows invalid handle", err: invalidHandle, want: true},
		{name: "wrapped windows invalid handle", err: errors.Join(errors.New("flush"), invalidHandle), want: true},
		{name: "other error", err: errors.New("disk full"), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isIgnorableSyncError(tc.err); got != tc.want {
				t.Fatalf("isIgnorableSyncError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
