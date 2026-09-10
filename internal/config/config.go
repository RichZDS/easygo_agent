// Package config 加载模板的非密钥 YAML 配置。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"easygo-agent/internal/logger"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultPath 是应用唯一读取的运行时配置文件。
	DefaultPath = "configs/config.yaml"
	// APIKeyEnvironmentVariable 是默认的模型凭证环境变量名。
	APIKeyEnvironmentVariable = "EASYGO_AGENT_API_KEY"
	defaultMaxSteps           = 8
	defaultTimeout            = "120s"
)

var envRefPattern = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Config 包含模板的全部运行时配置。
type Config struct {
	Agent    AgentConfig
	Model    ModelConfig
	SubAgent ModelConfig
	Database DatabaseConfig
	HTTP     HTTPConfig
	Queue    QueueConfig
}

type DatabaseConfig struct {
	Driver   string `yaml:"driver"`
	DSN      string `yaml:"dsn"`
	MaxConns int32  `yaml:"max_conns"`
}
type HTTPConfig struct {
	Address string `yaml:"address"`
}

type QueueConfig struct {
	MaxPending   int
	MaxWorkers   int
	PollInterval time.Duration
	LeaseTTL     time.Duration
}

// AgentConfig 控制 Eino ReAct 行为。
type AgentConfig struct {
	MaxSteps      int
	ContextTokens int
}

// ModelConfig 控制单个 OpenAI 兼容模型。
type ModelConfig struct {
	Name    string
	BaseURL string
	Timeout time.Duration
	APIKey  string
}

type rawConfig struct {
	Agent    rawAgentConfig  `yaml:"agent"`
	Model    rawModelConfig  `yaml:"model"`
	SubAgent *rawModelConfig `yaml:"subagent"`
	Database DatabaseConfig  `yaml:"database"`
	HTTP     HTTPConfig      `yaml:"http"`
	Queue    rawQueueConfig  `yaml:"queue"`
}

type rawAgentConfig struct {
	MaxSteps      int `yaml:"max_steps"`
	ContextTokens int `yaml:"context_tokens"`
}

type rawModelConfig struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	Timeout string `yaml:"timeout"`
	APIKey  string `yaml:"apikey"`
}

type rawQueueConfig struct {
	MaxPending   int    `yaml:"max_pending"`
	MaxWorkers   int    `yaml:"max_workers"`
	PollInterval string `yaml:"poll_interval"`
	LeaseTTL     string `yaml:"lease_ttl"`
}

// Load 读取一份严格 YAML，并把 apikey 中的 {ENV} 引用解析为环境变量值。
func Load(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	if strings.TrimSpace(path) == "" {
		err := errors.New("config path cannot be empty")
		logger.Error("load config failed", zap.String("field", "path"), zap.Error(err))
		return Config{}, err
	}
	if lookupEnv == nil {
		err := errors.New("environment lookup cannot be nil")
		logger.Error("load config failed", zap.String("field", "lookup_env"), zap.Error(err))
		return Config{}, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		wrappedErr := fmt.Errorf("read config: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}

	raw := rawConfig{
		Agent: rawAgentConfig{
			MaxSteps:      defaultMaxSteps,
			ContextTokens: 24000,
		},
		Model:    rawModelConfig{Timeout: defaultTimeout},
		Database: DatabaseConfig{Driver: "postgres", DSN: "{DATABASE_URL}", MaxConns: 16},
		HTTP:     HTTPConfig{Address: "127.0.0.1:8080"},
		Queue:    rawQueueConfig{MaxPending: 100, MaxWorkers: 4, PollInterval: "250ms", LeaseTTL: "30s"},
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	// 允许未知字段 true 为不允许，false 允许
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		wrappedErr := fmt.Errorf("decode config: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	if raw.Queue.MaxPending == 0 {
		raw.Queue.MaxPending = 100
	}
	if raw.Queue.MaxWorkers == 0 {
		raw.Queue.MaxWorkers = 4
	}
	if strings.TrimSpace(raw.Queue.PollInterval) == "" {
		raw.Queue.PollInterval = "250ms"
	}
	if strings.TrimSpace(raw.Queue.LeaseTTL) == "" {
		raw.Queue.LeaseTTL = "30s"
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not allowed")
		}
		wrappedErr := fmt.Errorf("decode config trailer: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}

	timeout, err := parseDuration(raw.Model.Timeout)
	if err != nil {
		wrappedErr := fmt.Errorf("parse model timeout: %w", err)
		logger.Error("load config failed", zap.String("field", "model.timeout"), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	pollInterval, err := parseDuration(raw.Queue.PollInterval)
	if err != nil {
		return Config{}, fmt.Errorf("parse queue.poll_interval: %w", err)
	}
	leaseTTL, err := parseDuration(raw.Queue.LeaseTTL)
	if err != nil {
		return Config{}, fmt.Errorf("parse queue.lease_ttl: %w", err)
	}
	apiKey, err := resolveAPIKey(raw.Model.APIKey, lookupEnv)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve model apikey: %w", err)
		logger.Error("load config failed", zap.String("field", "model.apikey"), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}

	cfg := Config{
		Agent: AgentConfig{
			MaxSteps:      raw.Agent.MaxSteps,
			ContextTokens: raw.Agent.ContextTokens,
		},
		Model: ModelConfig{
			Name:    strings.TrimSpace(raw.Model.Name),
			BaseURL: strings.TrimSpace(raw.Model.BaseURL),
			Timeout: timeout,
			APIKey:  apiKey,
		},
		Database: raw.Database,
		HTTP:     raw.HTTP,
		Queue: QueueConfig{
			MaxPending:   raw.Queue.MaxPending,
			MaxWorkers:   raw.Queue.MaxWorkers,
			PollInterval: pollInterval,
			LeaseTTL:     leaseTTL,
		},
	}
	cfg.SubAgent = cfg.Model
	if raw.SubAgent != nil {
		sub := raw.SubAgent
		if sub.Timeout == "" {
			sub.Timeout = defaultTimeout
		}
		subTimeout, subErr := parseDuration(sub.Timeout)
		if subErr != nil {
			return Config{}, fmt.Errorf("subagent timeout: %w", subErr)
		}
		subKey, subErr := resolveAPIKey(sub.APIKey, lookupEnv)
		if subErr != nil {
			return Config{}, fmt.Errorf("subagent apikey: %w", subErr)
		}
		cfg.SubAgent = ModelConfig{Name: strings.TrimSpace(sub.Name), BaseURL: strings.TrimSpace(sub.BaseURL), Timeout: subTimeout, APIKey: subKey}
		if subErr = validateConfig(Config{Agent: cfg.Agent, Model: cfg.SubAgent}); subErr != nil {
			return Config{}, fmt.Errorf("subagent: %w", subErr)
		}
	}
	if cfg.Database.Driver != "postgres" && cfg.Database.Driver != "memory" {
		return Config{}, errors.New("database.driver must be postgres or memory")
	}
	if cfg.Database.MaxConns < 2 {
		return Config{}, errors.New("database.max_conns must be at least 2")
	}
	if cfg.Database.Driver == "postgres" {
		matches := envRefPattern.FindStringSubmatch(cfg.Database.DSN)
		if matches == nil {
			return Config{}, errors.New("database.dsn must be an environment reference like {DATABASE_URL}")
		}
		var ok bool
		cfg.Database.DSN, ok = lookupEnv(matches[1])
		if !ok || strings.TrimSpace(cfg.Database.DSN) == "" {
			return Config{}, fmt.Errorf("environment variable %s is required", matches[1])
		}
	}
	if strings.TrimSpace(cfg.HTTP.Address) == "" {
		return Config{}, errors.New("http.address is required")
	}
	if cfg.Queue.MaxPending <= 0 {
		return Config{}, errors.New("queue.max_pending must be greater than zero")
	}
	if cfg.Queue.MaxWorkers <= 0 {
		return Config{}, errors.New("queue.max_workers must be greater than zero")
	}
	if cfg.Queue.PollInterval <= 0 || cfg.Queue.LeaseTTL <= 0 {
		return Config{}, errors.New("queue durations must be greater than zero")
	}
	if err := validateConfig(cfg); err != nil {
		wrappedErr := fmt.Errorf("validate config: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	return cfg, nil
}

// resolveAPIKey 将 YAML 中的 {ENV_NAME} 引用解析为对应环境变量值。
func resolveAPIKey(raw string, lookupEnv func(string) (string, bool)) (string, error) {
	trimmed := strings.TrimSpace(raw)
	matches := envRefPattern.FindStringSubmatch(trimmed)
	if matches == nil {
		err := errors.New("model apikey must be an env reference like {EASYGO_AGENT_API_KEY}")
		logger.Error("resolve apikey failed", zap.String("field", "model.apikey"), zap.Error(err))
		return "", err
	}
	envName := matches[1]
	apiKey, ok := lookupEnv(envName)
	if !ok || strings.TrimSpace(apiKey) == "" {
		err := fmt.Errorf("environment variable %s is required", envName)
		logger.Error("resolve apikey failed", zap.String("env", envName), zap.Error(err))
		return "", err
	}
	return apiKey, nil
}

// parseDuration 将 YAML 时长字符串解析为正的 Go duration。
func parseDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		wrappedErr := fmt.Errorf("invalid duration: %w", err)
		logger.Error("parse duration failed", zap.String("field", "model.timeout"), zap.Error(wrappedErr))
		return 0, wrappedErr
	}
	if duration <= 0 {
		err := errors.New("duration must be greater than zero")
		logger.Error("parse duration failed", zap.String("field", "model.timeout"), zap.Error(err))
		return 0, err
	}
	return duration, nil
}

// validateConfig 只校验运行模板所需的字段。
func validateConfig(cfg Config) error {
	if cfg.Agent.ContextTokens < 1024 {
		return errors.New("agent.context_tokens must be at least 1024")
	}
	if cfg.Agent.MaxSteps <= 0 {
		err := errors.New("agent max_steps must be greater than zero")
		logger.Error("validate config failed", zap.String("field", "agent.max_steps"), zap.Error(err))
		return err
	}
	if cfg.Model.Name == "" {
		err := errors.New("model name cannot be empty")
		logger.Error("validate config failed", zap.String("field", "model.name"), zap.Error(err))
		return err
	}
	if cfg.Model.BaseURL != "" {
		parsedURL, err := url.ParseRequestURI(cfg.Model.BaseURL)
		if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			if err == nil {
				err = errors.New("base_url must be an absolute URL")
			}
			wrappedErr := fmt.Errorf("invalid model base_url: %w", err)
			logger.Error("validate config failed", zap.String("field", "model.base_url"), zap.Error(wrappedErr))
			return wrappedErr
		}
	}
	if cfg.Model.Timeout <= 0 {
		err := errors.New("model timeout must be greater than zero")
		logger.Error("validate config failed", zap.String("field", "model.timeout"), zap.Error(err))
		return err
	}
	if cfg.Model.APIKey == "" {
		err := errors.New("model apikey cannot be empty")
		logger.Error("validate config failed", zap.String("field", "model.apikey"), zap.Error(err))
		return err
	}
	return nil
}
