package logger

import (
	"strings"
	"testing"
	"time"

	"easygo-agent/internal/platform/requestid"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestPrettyConsoleFormat(t *testing.T) {
	enc := newPrettyConsoleEncoder(false)
	buf, err := enc.EncodeEntry(zapcore.Entry{
		Level:   zapcore.InfoLevel,
		Time:    time.Date(2026, 7, 26, 20, 31, 35, 0, time.Local),
		Message: "http request",
		Caller:  zapcore.EntryCaller{Defined: true, File: "middleware/logger.go", Line: 40},
	}, []zapcore.Field{
		zap.String("trace_id", "abc"),
		zap.String("method", "POST"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	buf.Free()

	if !strings.HasPrefix(got, "2026-07-26T20:31:35[INFO] ") {
		t.Fatalf("prefix = %q", got)
	}
	if !strings.Contains(got, "middleware/logger.go:40 http request ") {
		t.Fatalf("caller/msg = %q", got)
	}
	if !strings.Contains(got, `"trace_id":"abc"`) || !strings.Contains(got, `"method":"POST"`) {
		t.Fatalf("fields = %q", got)
	}
}

func TestInfoAutoTraceID(t *testing.T) {
	if err := Init(Config{
		Environment: "development",
		Level:       "info",
	}); err != nil {
		t.Fatal(err)
	}
	requestid.Bind("tid-auto")
	defer requestid.Unbind()
	Info("auto trace smoke", zap.String("k", "v"))
}
