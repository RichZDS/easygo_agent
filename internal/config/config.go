package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App      App      `yaml:"app"`
	HTTP     HTTP     `yaml:"http"`
	MySQL    MySQL    `yaml:"mysql"`
	Redis    Redis    `yaml:"redis"`
	Log      Log      `yaml:"log"`
	Security Security `yaml:"security"`
}

type App struct {
	Name string `yaml:"name"`
	Env  string `yaml:"environment"`
}

type HTTP struct {
	Address           string        `yaml:"address"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}

type MySQL struct {
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	AutoMigrate     bool          `yaml:"auto_migrate"`
}

type Redis struct {
	Address      string        `yaml:"address"`
	Password     string        `yaml:"password"`
	DB           int           `yaml:"db"`
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type Log struct {
	Level      string `yaml:"level"`
	File       string `yaml:"file"`
	MaxSizeMB  int    `yaml:"max_size_mb"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAgeDays int    `yaml:"max_age_days"`
	Compress   bool   `yaml:"compress"`
}

type Security struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// Load reads one YAML file. KnownFields rejects misspelled configuration keys
// so a typo cannot silently start the application with an unexpected value.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, fmt.Errorf("config path cannot be empty")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	cfg := defaults()
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

func defaults() Config {
	return Config{
		App: App{Name: "easygo-agent", Env: "development"},
		HTTP: HTTP{
			Address:           ":8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			ShutdownTimeout:   10 * time.Second,
		},
		MySQL: MySQL{
			MaxOpenConns:    25,
			MaxIdleConns:    10,
			ConnMaxLifetime: 30 * time.Minute,
		},
		Redis: Redis{
			DB:           0,
			DialTimeout:  5 * time.Second,
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		},
		Log: Log{
			Level:      "info",
			File:       "logs/app.log",
			MaxSizeMB:  100,
			MaxBackups: 10,
			MaxAgeDays: 30,
			Compress:   true,
		},
		Security: Security{
			AllowedOrigins: []string{"http://localhost:3000", "http://localhost:5173"},
		},
	}
}

func (cfg Config) Validate() error {
	if cfg.MySQL.DSN == "" {
		return fmt.Errorf("mysql.dsn cannot be empty")
	}
	if cfg.Redis.Address == "" {
		return fmt.Errorf("redis.address cannot be empty")
	}
	if cfg.MySQL.MaxOpenConns <= 0 || cfg.MySQL.MaxIdleConns < 0 || cfg.MySQL.MaxIdleConns > cfg.MySQL.MaxOpenConns {
		return fmt.Errorf("invalid mysql connection pool settings")
	}
	if cfg.HTTP.Address == "" {
		return fmt.Errorf("http.address cannot be empty")
	}
	return nil
}
