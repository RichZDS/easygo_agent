package config

import (
	"testing"
	"time"
)

func TestLoadYAML(t *testing.T) {
	cfg, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MySQL.DSN != "user:pass@tcp(localhost:3306)/app" {
		t.Fatalf("mysql dsn = %q", cfg.MySQL.DSN)
	}
	if cfg.Redis.Address != "localhost:6379" || cfg.Redis.DB != 2 {
		t.Fatalf("redis config = %+v", cfg.Redis)
	}
	if cfg.HTTP.ReadTimeout != 12*time.Second {
		t.Fatalf("read timeout = %s", cfg.HTTP.ReadTimeout)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	if _, err := Load("testdata/invalid_duration.yaml"); err == nil {
		t.Fatal("Load() error = nil, want invalid duration error")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	if _, err := Load("testdata/unknown_field.yaml"); err == nil {
		t.Fatal("Load() error = nil, want unknown field error")
	}
}

func TestLoadRejectsInvalidPool(t *testing.T) {
	if _, err := Load("testdata/invalid_pool.yaml"); err == nil {
		t.Fatal("Load() error = nil, want invalid pool error")
	}
}
