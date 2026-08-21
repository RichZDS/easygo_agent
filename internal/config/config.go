// Package config loads the non-secret template configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	// APIKeyEnvironmentVariable names the only supported model credential source.
	APIKeyEnvironmentVariable = "EASYGO_AGENT_API_KEY"
	defaultSystemPrompt       = "You are a helpful assistant."
	defaultMaxSteps           = 8
	defaultTimeout            = "120s"
	defaultTracingExporter    = "stdout"
)

// Config contains all runtime configuration for the template.
type Config struct {
	Agent   AgentConfig
	Model   ModelConfig
	Tracing TracingConfig
}

// AgentConfig controls Eino ReAct behavior.
type AgentConfig struct {
	SystemPrompt string
	MaxSteps     int
}

// ModelConfig controls the single OpenAI-compatible model.
type ModelConfig struct {
	Name    string
	BaseURL string
	Timeout time.Duration
	APIKey  string
}

// TracingConfig controls optional OpenTelemetry export.
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
}

type rawTracingConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Exporter string `yaml:"exporter"`
}

// Load reads one strict YAML document and injects the model credential from the environment.
func Load(path string, lookupEnv func(string) (string, bool), logger *zap.Logger) (Config, error) {
	logger = safeLogger(logger)
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
	decoder.KnownFields(true)
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

	timeout, err := parseDuration(raw.Model.Timeout, logger)
	if err != nil {
		wrappedErr := fmt.Errorf("parse model timeout: %w", err)
		logger.Error("load config failed", zap.String("field", "model.timeout"), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	apiKey, ok := lookupEnv(APIKeyEnvironmentVariable)
	if !ok || strings.TrimSpace(apiKey) == "" {
		err := fmt.Errorf("%s is required", APIKeyEnvironmentVariable)
		logger.Error("load config failed", zap.String("field", APIKeyEnvironmentVariable), zap.Error(err))
		return Config{}, err
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
	if err := validateConfig(cfg, logger); err != nil {
		wrappedErr := fmt.Errorf("validate config: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	return cfg, nil
}

// parseDuration converts a YAML duration string into a positive Go duration.
func parseDuration(value string, logger *zap.Logger) (time.Duration, error) {
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

// validateConfig checks only the fields required to run the template.
func validateConfig(cfg Config, logger *zap.Logger) error {
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
	if cfg.Tracing.Exporter != defaultTracingExporter {
		err := fmt.Errorf("unsupported tracing exporter %q", cfg.Tracing.Exporter)
		logger.Error("validate config failed", zap.String("field", "tracing.exporter"), zap.Error(err))
		return err
	}
	return nil
}

// safeLogger replaces a nil logger with a no-op logger.
func safeLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger
}
