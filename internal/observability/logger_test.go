package observability

import (
	"testing"

	"go.uber.org/zap"
)

// TestNewLoggerReplacesGlobals 验证生产 logger 会安装为进程级全局实例。
func TestNewLoggerReplacesGlobals(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)

	logger, err := NewLogger()
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	if logger == nil {
		t.Fatal("NewLogger() logger = nil")
	}
	if zap.L() != logger {
		t.Fatal("zap.L() was not replaced with the production logger")
	}
}
