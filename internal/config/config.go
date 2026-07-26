package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MySQL  MySQL  `yaml:"mysql"`
	Logger Logger `yaml:"logger"`
}

type MySQL struct {
	Host            string `yaml:"host"`
	Port            int    `yaml:"port"`
	User            string `yaml:"user"`
	Password        string `yaml:"password"`
	Database        string `yaml:"database"`
	Charset         string `yaml:"charset"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	ConnMaxLifetime int    `yaml:"conn_max_lifetime"`
}

type Logger struct {
	Environment string `yaml:"environment"`
	Level       string `yaml:"level"`
	File        string `yaml:"file"`
	MaxSizeMB   int    `yaml:"max_size_mb"`
	MaxBackups  int    `yaml:"max_backups"`
	MaxAgeDays  int    `yaml:"max_age_days"`
	Compress    bool   `yaml:"compress"`
}

func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("config path cannot be empty")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if cfg.MySQL.Host == "" || cfg.MySQL.Port <= 0 || cfg.MySQL.Port > 65535 {
		return fmt.Errorf("mysql host or port is invalid")
	}
	if cfg.MySQL.User == "" || cfg.MySQL.Database == "" || cfg.MySQL.Charset == "" {
		return fmt.Errorf("mysql user, database and charset cannot be empty")
	}
	if cfg.MySQL.MaxOpenConns <= 0 || cfg.MySQL.MaxIdleConns < 0 || cfg.MySQL.MaxIdleConns > cfg.MySQL.MaxOpenConns {
		return fmt.Errorf("invalid mysql connection pool settings")
	}
	if cfg.MySQL.ConnMaxLifetime <= 0 {
		return fmt.Errorf("mysql conn_max_lifetime must be greater than zero")
	}
	return nil
}
