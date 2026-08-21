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
	defaultSystemPrompt       = "You are a helpful assistant."
	defaultMaxSteps           = 8
	defaultTimeout            = "120s"
	defaultTracingExporter    = "stdout"
)

var envRefPattern = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Config 包含模板的全部运行时配置。
type Config struct {
	Agent   AgentConfig
	Model   ModelConfig
	Tracing TracingConfig
}

// AgentConfig 控制 Eino ReAct 行为。
type AgentConfig struct {
	SystemPrompt string
	MaxSteps     int
}

// ModelConfig 控制单个 OpenAI 兼容模型。
type ModelConfig struct {
	Name    string
	BaseURL string
	Timeout time.Duration
	APIKey  string
}

// TracingConfig 控制可选的 OpenTelemetry 导出。
type TracingConfig struct {
	Enabled  bool
	Exporter string
}

type rawConfig struct {
	Agent   rawAgentConfig   `yaml:"agent"`
	Model   rawModelConfig   `yaml:"model"`
	Tracing rawTracingConfig `yaml:"tracing"`
}

type rawAgentConfig struct {
	SystemPrompt string `yaml:"system_prompt"`
	MaxSteps     int    `yaml:"max_steps"`
}

type rawModelConfig struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	Timeout string `yaml:"timeout"`
	APIKey  string `yaml:"apikey"`
}

type rawTracingConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Exporter string `yaml:"exporter"`
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
			SystemPrompt: defaultSystemPrompt,
			MaxSteps:     defaultMaxSteps,
		},
		Model: rawModelConfig{Timeout: defaultTimeout},
		Tracing: rawTracingConfig{
			Exporter: defaultTracingExporter,
		},
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	// 允许未知字段 true 为不允许，false 允许
	decoder.KnownFields(false)
	if err := decoder.Decode(&raw); err != nil {
		wrappedErr := fmt.Errorf("decode config: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
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
	apiKey, err := resolveAPIKey(raw.Model.APIKey, lookupEnv)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve model apikey: %w", err)
		logger.Error("load config failed", zap.String("field", "model.apikey"), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}

	cfg := Config{
		Agent: AgentConfig{
			SystemPrompt: strings.TrimSpace(raw.Agent.SystemPrompt),
			MaxSteps:     raw.Agent.MaxSteps,
		},
		Model: ModelConfig{
			Name:    strings.TrimSpace(raw.Model.Name),
			BaseURL: strings.TrimSpace(raw.Model.BaseURL),
			Timeout: timeout,
			APIKey:  apiKey,
		},
		Tracing: TracingConfig{
			Enabled:  raw.Tracing.Enabled,
			Exporter: strings.TrimSpace(raw.Tracing.Exporter),
		},
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
	if cfg.Agent.SystemPrompt == "" {
		err := errors.New("agent system_prompt cannot be empty")
		logger.Error("validate config failed", zap.String("field", "agent.system_prompt"), zap.Error(err))
		return err
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
	if cfg.Tracing.Exporter != defaultTracingExporter {
		err := fmt.Errorf("unsupported tracing exporter %q", cfg.Tracing.Exporter)
		logger.Error("validate config failed", zap.String("field", "tracing.exporter"), zap.Error(err))
		return err
	}
	return nil
}
