package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestDefaultPathUsesCurrentDate 验证默认日志路径按当天日期生成。
func TestDefaultPathUsesCurrentDate(t *testing.T) {
	originalNow := now
	// restoreNow 恢复默认时间函数。
	restoreNow := func() { now = originalNow }
	defer restoreNow()
	// fixedNow 返回测试固定日期。
	now = func() time.Time {
		return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	}

	got := DefaultPath()
	want := filepath.Join(DefaultDir, "2026-08-25.log")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

// TestNewRejectsEmptyPath 验证空路径不能创建 logger。
func TestNewRejectsEmptyPath(t *testing.T) {
	core, observed := observer.New(zap.ErrorLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()

	produced, err := New("   ")
	if err == nil {
		t.Fatal("New empty path returned nil error")
	}
	if produced != nil {
		t.Fatal("New empty path returned a logger")
	}
	if observed.FilterMessage("create logger failed").Len() == 0 {
		t.Fatal("empty path error was not logged")
	}
}

// TestNewWritesDebugLogsToFile 验证 New 把 Debug 日志写入指定文件。
func TestNewWritesDebugLogsToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	t.Cleanup(func() {
		closeOpenSink()
		zap.ReplaceGlobals(zap.NewNop())
	})

	produced, err := New(path)
	if err != nil {
		t.Fatalf("New(%q) error = %v", path, err)
	}
	produced.Debug("hello from file logger")
	if err := produced.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !strings.Contains(string(content), "hello from file logger") {
		t.Fatalf("log file missing debug message: %s", content)
	}
}
