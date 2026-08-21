package logger

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestErrorWritesToGlobalLogger 验证 Error 会写入当前全局 Zap logger。
func TestErrorWritesToGlobalLogger(t *testing.T) {
	core, recorded := observer.New(zapcore.ErrorLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)

	Error("create Gateway failed", zap.String("field", "max_steps"))

	logs := recorded.All()
	if len(logs) != 1 {
		t.Fatalf("log count = %d, want 1", len(logs))
	}
	if logs[0].Message != "create Gateway failed" {
		t.Fatalf("message = %q, want %q", logs[0].Message, "create Gateway failed")
	}
}
