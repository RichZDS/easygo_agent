// Package config 加载模板的非密钥 YAML 配置。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"easygo-agent/internal/logger"
	"easygo-agent/internal/task"
	"easygo-agent/internal/usermemory"

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
	defaultSandboxBaseURL     = "http://127.0.0.1:8787"
	defaultSandboxTimeout     = "11m"
	defaultSandboxOutputBytes = 65536
)

var envRefPattern = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Config 包含模板的全部运行时配置。
type Config struct {
	Tasks    task.Config
	Agent    AgentConfig
	Model    ModelConfig
	SubAgent ModelConfig
	Database DatabaseConfig
	Memory   usermemory.Config
	HTTP     HTTPConfig
	Queue    QueueConfig
	Sandbox  SandboxConfig
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

// SandboxConfig controls the optional local sandbox controller tools. The
// controller owns Docker access and lifecycle; this process only calls HTTP.
type SandboxConfig struct {
	Enabled        bool
	BaseURL        string
	AuthToken      string
	RequestTimeout time.Duration
	MaxOutputBytes int
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
	Tasks    rawTaskConfig    `yaml:"tasks"`
	Agent    rawAgentConfig   `yaml:"agent"`
	Model    rawModelConfig   `yaml:"model"`
	SubAgent *rawModelConfig  `yaml:"subagent"`
	Database DatabaseConfig   `yaml:"database"`
	Memory   rawMemoryConfig  `yaml:"memory"`
	HTTP     HTTPConfig       `yaml:"http"`
	Queue    rawQueueConfig   `yaml:"queue"`
	Sandbox  rawSandboxConfig `yaml:"sandbox"`
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

type rawMemoryConfig struct {
	Enabled     bool   `yaml:"enabled"`
	DailyAt     string `yaml:"daily_at"`
	Timezone    string `yaml:"timezone"`
	BatchTurns  int    `yaml:"batch_turns"`
	TopK        int    `yaml:"top_k"`
	StorageRoot string `yaml:"storage_root"`
	DSN         string `yaml:"dsn"`
	MaxConns    int32  `yaml:"max_conns"`
}

type rawQueueConfig struct {
	MaxPending   int    `yaml:"max_pending"`
	MaxWorkers   int    `yaml:"max_workers"`
	PollInterval string `yaml:"poll_interval"`
	LeaseTTL     string `yaml:"lease_ttl"`
}

type rawSandboxConfig struct {
	Enabled        bool   `yaml:"enabled"`
	BaseURL        string `yaml:"base_url"`
	AuthToken      string `yaml:"auth_token"`
	RequestTimeout string `yaml:"request_timeout"`
	MaxOutputBytes int    `yaml:"max_output_bytes"`
}

// Load 读取一份严格 YAML，并把敏感字段中的 {ENV} 引用解析为环境变量值。

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

	memoryDefaults := usermemory.DefaultConfig()
	raw := rawConfig{
		Agent: rawAgentConfig{
			MaxSteps:      defaultMaxSteps,
			ContextTokens: 24000,
		},
		Model:    rawModelConfig{Timeout: defaultTimeout},
		Database: DatabaseConfig{Driver: "postgres", DSN: "{DATABASE_URL}", MaxConns: 16},
		Memory:   rawMemoryConfig{Enabled: memoryDefaults.Enabled, DailyAt: memoryDefaults.DailyAt, Timezone: memoryDefaults.Timezone, BatchTurns: memoryDefaults.BatchTurns, TopK: memoryDefaults.TopK, StorageRoot: memoryDefaults.StorageRoot, DSN: "{MEMORY_DATABASE_URL}", MaxConns: memoryDefaults.MaxConns},
		HTTP:     HTTPConfig{Address: "127.0.0.1:8080"},
		Queue:    rawQueueConfig{MaxPending: 100, MaxWorkers: 4, PollInterval: "250ms", LeaseTTL: "30s"},
		Sandbox:  rawSandboxConfig{BaseURL: defaultSandboxBaseURL, RequestTimeout: defaultSandboxTimeout, MaxOutputBytes: defaultSandboxOutputBytes},
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
	if strings.TrimSpace(raw.Sandbox.BaseURL) == "" {
		raw.Sandbox.BaseURL = defaultSandboxBaseURL
	}
	if strings.TrimSpace(raw.Sandbox.RequestTimeout) == "" {
		raw.Sandbox.RequestTimeout = defaultSandboxTimeout
	}
	if raw.Sandbox.MaxOutputBytes == 0 {
		raw.Sandbox.MaxOutputBytes = defaultSandboxOutputBytes
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
	sandboxRequestTimeout, err := parseDuration(raw.Sandbox.RequestTimeout)
	if err != nil {
		return Config{}, fmt.Errorf("parse sandbox.request_timeout: %w", err)
	}
	apiKey, err := resolveAPIKey(raw.Model.APIKey, lookupEnv)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve model apikey: %w", err)
		logger.Error("load config failed", zap.String("field", "model.apikey"), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	sandboxAuthToken := ""
	if raw.Sandbox.Enabled {
		sandboxAuthToken, err = resolveEnvironmentReference(raw.Sandbox.AuthToken, "sandbox.auth_token", lookupEnv)
		if err != nil {
			return Config{}, fmt.Errorf("resolve sandbox controller auth token: %w", err)
		}
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
		Memory:   usermemory.Config{Enabled: raw.Memory.Enabled, DailyAt: raw.Memory.DailyAt, Timezone: raw.Memory.Timezone, BatchTurns: raw.Memory.BatchTurns, TopK: raw.Memory.TopK, StorageRoot: raw.Memory.StorageRoot, DSN: raw.Memory.DSN, MaxConns: raw.Memory.MaxConns},
		HTTP:     raw.HTTP,
		Queue: QueueConfig{
			MaxPending:   raw.Queue.MaxPending,
			MaxWorkers:   raw.Queue.MaxWorkers,
			PollInterval: pollInterval,
			LeaseTTL:     leaseTTL,
		},
		Sandbox: SandboxConfig{
			Enabled:        raw.Sandbox.Enabled,
			BaseURL:        strings.TrimRight(strings.TrimSpace(raw.Sandbox.BaseURL), "/"),
			AuthToken:      sandboxAuthToken,
			RequestTimeout: sandboxRequestTimeout,
			MaxOutputBytes: raw.Sandbox.MaxOutputBytes,
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
	if err := cfg.Memory.Validate(); err != nil {
		return Config{}, fmt.Errorf("memory: %w", err)
	}
	if cfg.Database.Driver == "postgres" && cfg.Memory.Enabled {
		matches := envRefPattern.FindStringSubmatch(cfg.Memory.DSN)
		if matches == nil {
			return Config{}, errors.New("memory.dsn must be an environment reference like {MEMORY_DATABASE_URL}")
		}
		value, ok := lookupEnv(matches[1])
		if !ok || strings.TrimSpace(value) == "" {
			return Config{}, fmt.Errorf("environment variable %s is required", matches[1])
		}
		cfg.Memory.DSN = value
	}
	if cfg.Memory.MaxConns < 1 {
		return Config{}, errors.New("memory.max_conns must be at least 1")
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
	if cfg.Sandbox.MaxOutputBytes <= 0 || cfg.Sandbox.MaxOutputBytes > 1024*1024 {
		return Config{}, errors.New("sandbox.max_output_bytes must be between 1 and 1048576")
	}
	if cfg.Sandbox.Enabled {
		parsedURL, parseErr := url.ParseRequestURI(cfg.Sandbox.BaseURL)
		if parseErr != nil || parsedURL.Scheme == "" || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
			return Config{}, errors.New("sandbox.base_url must be an absolute http or https URL without credentials, query, or fragment")
		}
		hostname := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
		hostIP := net.ParseIP(hostname)
		if hostname != "localhost" && (hostIP == nil || !hostIP.IsLoopback()) {
			return Config{}, errors.New("sandbox.base_url must use a loopback host")
		}
		if len(cfg.Sandbox.AuthToken) < 32 || strings.IndexFunc(cfg.Sandbox.AuthToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return Config{}, errors.New("sandbox.auth_token must resolve to at least 32 printable ASCII characters without spaces")
		}
	}
	if err := validateConfig(cfg); err != nil {
		wrappedErr := fmt.Errorf("validate config: %w", err)
		logger.Error("load config failed", zap.String("path", path), zap.Error(wrappedErr))
		return Config{}, wrappedErr
	}
	cfg.Tasks, err = taskConfig(raw.Tasks)
	if err != nil {
		return Config{}, fmt.Errorf("tasks: %w", err)
	}

	if cfg.Tasks.Enabled && cfg.Database.Driver == "postgres" && int64(cfg.Database.MaxConns) < int64(cfg.Queue.MaxWorkers)+2 {
		return Config{}, errors.New("tasks require database.max_conns >= queue.max_workers + 2; session leases must leave connections for task persistence and notifications")
	}
	return cfg, nil
}

// resolveAPIKey 将 YAML 中的 {ENV_NAME} 引用解析为对应环境变量值。
func resolveAPIKey(raw string, lookupEnv func(string) (string, bool)) (string, error) {
	return resolveEnvironmentReference(raw, "model.apikey", lookupEnv)
}

func resolveEnvironmentReference(raw, field string, lookupEnv func(string) (string, bool)) (string, error) {
	trimmed := strings.TrimSpace(raw)
	matches := envRefPattern.FindStringSubmatch(trimmed)
	if matches == nil {
		err := fmt.Errorf("%s must be an environment reference like {ENV_NAME}", field)
		logger.Error("resolve environment reference failed", zap.String("field", field), zap.Error(err))
		return "", err
	}
	envName := matches[1]
	value, ok := lookupEnv(envName)
	if !ok || strings.TrimSpace(value) == "" {
		err := fmt.Errorf("environment variable %s is required", envName)
		logger.Error("resolve environment reference failed", zap.String("field", field), zap.String("env", envName), zap.Error(err))
		return "", err
	}
	return value, nil
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

type rawTaskConfig struct {
	Enabled      bool                 `yaml:"enabled"`
	Workers      int                  `yaml:"workers"`
	MaxSteps     int                  `yaml:"max_steps"`
	Timeout      string               `yaml:"timeout"`
	LeaseTTL     string               `yaml:"lease_ttl"`
	PollInterval string               `yaml:"poll_interval"`
	Roles        map[string]task.Role `yaml:"roles"`
}

func taskConfig(raw rawTaskConfig) (task.Config, error) {
	c := task.DefaultConfig()
	c.Enabled = raw.Enabled
	if raw.Workers != 0 {
		c.Workers = raw.Workers
	}
	if raw.MaxSteps != 0 {
		c.MaxSteps = raw.MaxSteps
	}
	for _, pair := range []struct {
		value  string
		target *time.Duration
	}{{raw.Timeout, &c.Timeout}, {raw.LeaseTTL, &c.LeaseTTL}, {raw.PollInterval, &c.PollInterval}} {
		if pair.value != "" {
			d, err := time.ParseDuration(pair.value)
			if err != nil {
				return c, err
			}
			*pair.target = d
		}
	}
	if raw.Roles != nil {
		c.Roles = raw.Roles
	}
	if c.Workers < 1 || c.MaxSteps < 1 || c.Timeout <= 0 || c.LeaseTTL < 30*time.Millisecond || c.PollInterval <= 0 {
		return c, errors.New("invalid task worker count or execution budgets")
	}
	if len(c.Roles) == 0 {
		return c, errors.New("at least one task role is required")
	}
	return c, nil
}
