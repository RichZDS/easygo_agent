package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestFileLevelKeepsMetadataAndGatesFullMessages(t *testing.T) {
	previous := zap.L()
	defer zap.ReplaceGlobals(previous)
	defer closeOpenSink()
	for _, level := range []zap.AtomicLevel{zap.NewAtomicLevelAt(zap.InfoLevel), zap.NewAtomicLevelAt(zap.DebugLevel)} {
		path := filepath.Join(t.TempDir(), "agent.log")
		log, err := New(path, level.Level())
		if err != nil {
			t.Fatal(err)
		}
		log.Info("agent.phase", zap.String("run_id", "r"))
		log.Debug("agentic message", zap.String("message", "private payload"))
		if err := log.Sync(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		want := 1
		if level.Level() == zap.DebugLevel {
			want = 2
		}
		if len(lines) != want {
			t.Fatalf("level=%v: %s", level, data)
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["msg"] != "agent.phase" || entry["run_id"] != "r" {
			t.Fatal(entry)
		}
		if strings.Contains(string(data), "private payload") != (want == 2) {
			t.Fatal(string(data))
		}
	}
}
