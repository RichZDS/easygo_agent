// Package usermemory turns raw user conversations into a bounded, durable
// profile and materializes that profile for inspection outside the database.
package usermemory

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultDailyAt     = "03:00"
	DefaultTimezone    = "Local"
	DefaultBatchTurns  = 20
	DefaultStorageRoot = "Storage/Long-term Memory/User profile"
)

type Config struct {
	Enabled     bool
	DailyAt     string
	Timezone    string
	BatchTurns  int
	TopK        int
	StorageRoot string
	DSN         string
	MaxConns    int32
}

func DefaultConfig() Config {
	return Config{Enabled: true, DailyAt: DefaultDailyAt, Timezone: DefaultTimezone, BatchTurns: DefaultBatchTurns, TopK: 5, StorageRoot: DefaultStorageRoot, MaxConns: 4}
}

func (c Config) Validate() error {
	if _, _, err := parseDailyAt(c.DailyAt); err != nil {
		return err
	}
	if strings.TrimSpace(c.Timezone) == "" {
		return errors.New("memory.timezone is required")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return errors.New("memory.timezone must be a valid IANA timezone or Local")
	}
	if c.BatchTurns < 1 || c.BatchTurns > 100 {
		return errors.New("memory.batch_turns must be 1..100")
	}
	if c.TopK != 5 {
		return errors.New("memory.top_k must be 5 to preserve the five-row user-profile limit")
	}
	if strings.TrimSpace(c.StorageRoot) == "" || filepath.IsAbs(c.StorageRoot) {
		return errors.New("memory.storage_root must be a non-empty relative path")
	}
	return nil
}
