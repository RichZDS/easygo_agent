package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"easygo-agent/internal/platform/logger"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type Config struct {
	MySQL  MySQL  `yaml:"mysql"`
	Logger Logger `yaml:"logger"`
	Skills Skills `yaml:"skills"`
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

type Skills struct {
	RootDir           string `yaml:"root_dir"`
	ReadmeSrc         string `yaml:"readme_src"`
	MaxZipBytes       int64  `yaml:"max_zip_bytes"`
	MaxExtractedBytes int64  `yaml:"max_extracted_bytes"`
	MaxFiles          int    `yaml:"max_files"`
}

// Load reads, defaults, and validates the application configuration at path.
func Load(path string) (Config, error) {
	if path == "" {
		err := fmt.Errorf("config path cannot be empty")
		logger.Error("load config failed", zap.Error(err))
		return Config{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		wrappedErr := fmt.Errorf("read config %q: %w", path, err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}

	cfg := Config{
		Skills: Skills{
			RootDir:           "skills",
			ReadmeSrc:         "README.md",
			MaxZipBytes:       5 << 20,
			MaxExtractedBytes: 20 << 20,
			MaxFiles:          200,
		},
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		wrappedErr := fmt.Errorf("decode config %q: %w", path, err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	if err := cfg.Validate(); err != nil {
		wrappedErr := fmt.Errorf("validate config %q: %w", path, err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	return cfg, nil
}

// Validate checks that the application configuration contains usable values.
func (cfg Config) Validate() error {
	if cfg.MySQL.Host == "" || cfg.MySQL.Port <= 0 || cfg.MySQL.Port > 65535 {
		err := fmt.Errorf("mysql host or port is invalid")
		logger.Error("validate config failed", zap.Int("mysql_port", cfg.MySQL.Port), zap.Error(err))
		return err
	}
	if cfg.MySQL.User == "" || cfg.MySQL.Database == "" || cfg.MySQL.Charset == "" {
		err := fmt.Errorf("mysql user, database and charset cannot be empty")
		logger.Error("validate config failed", zap.Error(err))
		return err
	}
	if cfg.MySQL.MaxOpenConns <= 0 || cfg.MySQL.MaxIdleConns < 0 || cfg.MySQL.MaxIdleConns > cfg.MySQL.MaxOpenConns {
		err := fmt.Errorf("invalid mysql connection pool settings")
		logger.Error("validate config failed",
			zap.Int("mysql_max_open_conns", cfg.MySQL.MaxOpenConns),
			zap.Int("mysql_max_idle_conns", cfg.MySQL.MaxIdleConns),
			zap.Error(err),
		)
		return err
	}
	if cfg.MySQL.ConnMaxLifetime <= 0 {
		err := fmt.Errorf("mysql conn_max_lifetime must be greater than zero")
		logger.Error("validate config failed", zap.Int("mysql_conn_max_lifetime", cfg.MySQL.ConnMaxLifetime), zap.Error(err))
		return err
	}
	if strings.TrimSpace(cfg.Skills.RootDir) == "" {
		err := fmt.Errorf("skills root_dir cannot be empty")
		logger.Error("validate config failed", zap.Error(err))
		return err
	}
	if strings.TrimSpace(cfg.Skills.ReadmeSrc) == "" {
		err := fmt.Errorf("skills readme_src cannot be empty")
		logger.Error("validate config failed", zap.Error(err))
		return err
	}
	if cfg.Skills.MaxZipBytes <= 0 {
		err := fmt.Errorf("skills max_zip_bytes must be greater than zero")
		logger.Error("validate config failed", zap.Int64("skills_max_zip_bytes", cfg.Skills.MaxZipBytes), zap.Error(err))
		return err
	}
	if cfg.Skills.MaxExtractedBytes <= 0 {
		err := fmt.Errorf("skills max_extracted_bytes must be greater than zero")
		logger.Error("validate config failed", zap.Int64("skills_max_extracted_bytes", cfg.Skills.MaxExtractedBytes), zap.Error(err))
		return err
	}
	if cfg.Skills.MaxFiles <= 0 {
		err := fmt.Errorf("skills max_files must be greater than zero")
		logger.Error("validate config failed", zap.Int("skills_max_files", cfg.Skills.MaxFiles), zap.Error(err))
		return err
	}
	return nil
}
