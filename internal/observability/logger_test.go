package observability

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"easygo-agent/internal/logger"

	"go.uber.org/zap"
)

// TestNewLoggerReplacesGlobals 验证生产 logger 会安装为进程级全局实例。
func TestNewLoggerReplacesGlobals(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)

	produced, err := NewLogger(filepath.Join(t.TempDir(), "logs", "app.log"))
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	t.Cleanup(closeOpenSink)
	if produced == nil {
		t.Fatal("NewLogger() logger = nil")
	}
	if zap.L() != produced {
		t.Fatal("zap.L() was not replaced with the production logger")
	}
}

// TestNewLoggerWritesJSONToFile 验证日志写入指定文件，并自动创建父目录。
func TestNewLoggerWritesJSONToFile(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)

	path := filepath.Join(t.TempDir(), "logs", "app.log")
	produced, err := NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	t.Cleanup(closeOpenSink)
	logger.Info("application initialized", zap.String("mode", "tui"))
	if err := produced.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Contains(content, []byte(`"msg":"application initialized"`)) {
		t.Fatalf("log file = %q, want msg application initialized", content)
	}
	if !bytes.Contains(content, []byte(`"mode":"tui"`)) {
		t.Fatalf("log file = %q, want mode tui", content)
	}
}

// TestNewLoggerRejectsEmptyPath 验证空路径会被拒绝。
func TestNewLoggerRejectsEmptyPath(t *testing.T) {
	restore := zap.ReplaceGlobals(zap.NewNop())
	t.Cleanup(restore)

	produced, err := NewLogger("   ")
	if err == nil {
		t.Fatal("NewLogger() error = nil, want empty path error")
	}
	if produced != nil {
		t.Fatal("NewLogger() logger != nil, want nil")
	}
}
