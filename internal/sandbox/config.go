package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	controllerTokenEnvironment = "SANDBOX_CONTROLLER_TOKEN"
	dockerSocketEnvironment    = "DOCKER_SOCKET_PATH"
	maximumHardTTL             = 5 * time.Hour
	minimumCPUs                = 0.01
	maximumCPUs                = 64.0
	maximumRunning             = 64
	maximumApplications        = 1000
	minimumWaitTimeout         = 10 * time.Millisecond
	minimumIdleTTL             = time.Second
	minimumReaperInterval      = time.Second
)

var (
	namespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	environmentRef   = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

// Config is the controller's complete trusted runtime template. None of these
// values may be supplied by an agent request.
type Config struct {
	Namespace           string
	ListenAddress       string
	AuthToken           string
	StatePath           string
	DockerHost          string
	Image               string
	MaxRunning          int
	MaxApplications     int
	MaxStarting         int
	WaitTimeout         time.Duration
	BaseIdleTTL         time.Duration
	HardTTL             time.Duration
	ReaperInterval      time.Duration
	CPUs                float64
	MemoryBytes         int64
	PIDsLimit           int64
	ShmSizeBytes        int64
	TmpSizeBytes        int64
	WorkspaceSoftLimit  int64
	TotalWorkspaceLimit int64
	MinFreeDisk         int64
	MaxOutputBytes      int
	CommandTimeout      time.Duration
	MaxCommandTimeout   time.Duration
}

type rawConfig struct {
	Namespace           string  `yaml:"namespace"`
	ListenAddress       string  `yaml:"listen_address"`
	AuthToken           string  `yaml:"auth_token"`
	StatePath           string  `yaml:"state_path"`
	DockerHost          string  `yaml:"docker_host"`
	Image               string  `yaml:"image"`
	MaxRunning          int     `yaml:"max_running"`
	MaxApplications     int     `yaml:"max_applications"`
	MaxStarting         int     `yaml:"max_starting"`
	WaitTimeout         string  `yaml:"wait_timeout"`
	BaseIdleTTL         string  `yaml:"base_idle_ttl"`
	HardTTL             string  `yaml:"hard_ttl"`
	ReaperInterval      string  `yaml:"reaper_interval"`
	CPUs                float64 `yaml:"cpus"`
	Memory              string  `yaml:"memory"`
	PIDsLimit           int64   `yaml:"pids_limit"`
	ShmSize             string  `yaml:"shm_size"`
	TmpSize             string  `yaml:"tmp_size"`
	WorkspaceSoftLimit  string  `yaml:"workspace_soft_limit"`
	TotalWorkspaceLimit string  `yaml:"total_workspace_limit"`
	MinFreeDisk         string  `yaml:"min_free_disk"`
	MaxOutputBytes      int     `yaml:"max_output_bytes"`
	CommandTimeout      string  `yaml:"command_timeout"`
	MaxCommandTimeout   string  `yaml:"max_command_timeout"`
}

// LoadConfig decodes one strict YAML document and resolves only the two
// controller-specific environment variables documented by the deployment.
func LoadConfig(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	if strings.TrimSpace(path) == "" {
		return Config{}, errors.New("config path cannot be empty")
	}
	if lookupEnv == nil {
		return Config{}, errors.New("environment lookup cannot be nil")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read controller config: %w", err)
	}
	raw := defaultRawConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode controller config: %w", err)
	}
	var trailer any
	if err := decoder.Decode(&trailer); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("decode controller config trailer: %w", err)
	}

	token, err := resolveControllerToken(raw.AuthToken, lookupEnv)
	if err != nil {
		return Config{}, err
	}
	dockerHost := strings.TrimSpace(raw.DockerHost)
	if override, ok := lookupEnv(dockerSocketEnvironment); ok && strings.TrimSpace(override) != "" {
		override = strings.TrimSpace(override)
		if strings.Contains(override, "://") {
			dockerHost = override
		} else {
			dockerHost = "unix://" + override
		}
	}
	parseDuration := func(field, value string) (time.Duration, error) {
		result, parseErr := time.ParseDuration(strings.TrimSpace(value))
		if parseErr != nil {
			return 0, fmt.Errorf("%s must be a duration: %w", field, parseErr)
		}
		return result, nil
	}
	waitTimeout, err := parseDuration("wait_timeout", raw.WaitTimeout)
	if err != nil {
		return Config{}, err
	}
	baseIdleTTL, err := parseDuration("base_idle_ttl", raw.BaseIdleTTL)
	if err != nil {
		return Config{}, err
	}
	hardTTL, err := parseDuration("hard_ttl", raw.HardTTL)
	if err != nil {
		return Config{}, err
	}
	reaperInterval, err := parseDuration("reaper_interval", raw.ReaperInterval)
	if err != nil {
		return Config{}, err
	}
	commandTimeout, err := parseDuration("command_timeout", raw.CommandTimeout)
	if err != nil {
		return Config{}, err
	}
	maxCommandTimeout, err := parseDuration("max_command_timeout", raw.MaxCommandTimeout)
	if err != nil {
		return Config{}, err
	}
	parseSize := func(field, value string) (int64, error) {
		result, parseErr := parseByteSize(value)
		if parseErr != nil {
			return 0, fmt.Errorf("%s: %w", field, parseErr)
		}
		return result, nil
	}
	memoryBytes, err := parseSize("memory", raw.Memory)
	if err != nil {
		return Config{}, err
	}
	shmSizeBytes, err := parseSize("shm_size", raw.ShmSize)
	if err != nil {
		return Config{}, err
	}
	tmpSizeBytes, err := parseSize("tmp_size", raw.TmpSize)
	if err != nil {
		return Config{}, err
	}
	workspaceSoftLimit, err := parseSize("workspace_soft_limit", raw.WorkspaceSoftLimit)
	if err != nil {
		return Config{}, err
	}
	totalWorkspaceLimit, err := parseSize("total_workspace_limit", raw.TotalWorkspaceLimit)
	if err != nil {
		return Config{}, err
	}
	minFreeDisk, err := parseSize("min_free_disk", raw.MinFreeDisk)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Namespace:           strings.TrimSpace(raw.Namespace),
		ListenAddress:       strings.TrimSpace(raw.ListenAddress),
		AuthToken:           token,
		StatePath:           strings.TrimSpace(raw.StatePath),
		DockerHost:          dockerHost,
		Image:               strings.TrimSpace(raw.Image),
		MaxRunning:          raw.MaxRunning,
		MaxApplications:     raw.MaxApplications,
		MaxStarting:         raw.MaxStarting,
		WaitTimeout:         waitTimeout,
		BaseIdleTTL:         baseIdleTTL,
		HardTTL:             hardTTL,
		ReaperInterval:      reaperInterval,
		CPUs:                raw.CPUs,
		MemoryBytes:         memoryBytes,
		PIDsLimit:           raw.PIDsLimit,
		ShmSizeBytes:        shmSizeBytes,
		TmpSizeBytes:        tmpSizeBytes,
		WorkspaceSoftLimit:  workspaceSoftLimit,
		TotalWorkspaceLimit: totalWorkspaceLimit,
		MinFreeDisk:         minFreeDisk,
		MaxOutputBytes:      raw.MaxOutputBytes,
		CommandTimeout:      commandTimeout,
		MaxCommandTimeout:   maxCommandTimeout,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaultRawConfig() rawConfig {
	return rawConfig{
		Namespace:           "easygo-agent",
		ListenAddress:       "127.0.0.1:8787",
		AuthToken:           "{SANDBOX_CONTROLLER_TOKEN}",
		StatePath:           "/var/lib/easygo-sandbox/state.db",
		DockerHost:          "unix:///var/run/docker.sock",
		Image:               "easygo-agent-sandbox-runtime:local",
		MaxRunning:          3,
		MaxApplications:     30,
		MaxStarting:         2,
		WaitTimeout:         "30s",
		BaseIdleTTL:         "1m",
		HardTTL:             "5h",
		ReaperInterval:      "10s",
		CPUs:                1,
		Memory:              "2GiB",
		PIDsLimit:           256,
		ShmSize:             "64MiB",
		TmpSize:             "256MiB",
		WorkspaceSoftLimit:  "1GiB",
		TotalWorkspaceLimit: "20GiB",
		MinFreeDisk:         "10GiB",
		MaxOutputBytes:      65536,
		CommandTimeout:      "120s",
		MaxCommandTimeout:   "10m",
	}
}

// Validate rejects dangerous or internally inconsistent controller settings.
func (cfg Config) Validate() error {
	if !namespacePattern.MatchString(cfg.Namespace) {
		return errors.New("namespace must contain 1 to 63 letters, digits, dots, underscores, or hyphens")
	}
	if _, _, err := net.SplitHostPort(cfg.ListenAddress); err != nil {
		return fmt.Errorf("listen_address must be host:port: %w", err)
	}
	if len(cfg.AuthToken) < 32 || strings.IndexFunc(cfg.AuthToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return errors.New("auth_token must resolve to at least 32 printable ASCII characters without spaces")
	}
	if !filepath.IsAbs(cfg.StatePath) {
		return errors.New("state_path must be absolute")
	}
	dockerURL, err := url.Parse(cfg.DockerHost)
	if err != nil || dockerURL.Scheme != "unix" || dockerURL.Host != "" || !filepath.IsAbs(dockerURL.Path) || dockerURL.RawPath != "" || dockerURL.User != nil || dockerURL.RawQuery != "" || dockerURL.Fragment != "" {
		return errors.New("docker_host must be a local absolute unix socket URL without host, credentials, query, or fragment")
	}
	if cfg.Image == "" {
		return errors.New("image is required")
	}
	if cfg.MaxRunning < 1 || cfg.MaxRunning > maximumRunning || cfg.MaxApplications < 1 || cfg.MaxApplications > maximumApplications || cfg.MaxStarting < 1 {
		return fmt.Errorf("max_running must be 1..%d, max_applications 1..%d, and max_starting positive", maximumRunning, maximumApplications)
	}
	if cfg.MaxRunning > cfg.MaxApplications {
		return errors.New("max_running cannot exceed max_applications")
	}
	if cfg.MaxStarting > cfg.MaxRunning {
		return errors.New("max_starting cannot exceed max_running")
	}
	if cfg.WaitTimeout < minimumWaitTimeout || cfg.WaitTimeout > 10*time.Minute {
		return fmt.Errorf("wait_timeout must be between %s and 10m", minimumWaitTimeout)
	}
	if cfg.BaseIdleTTL < minimumIdleTTL || cfg.BaseIdleTTL > maximumHardTTL {
		return fmt.Errorf("base_idle_ttl must be between %s and 5h", minimumIdleTTL)
	}
	if cfg.HardTTL <= 0 || cfg.HardTTL > maximumHardTTL || cfg.BaseIdleTTL > cfg.HardTTL {
		return errors.New("hard_ttl must be positive, at most 5h, and at least base_idle_ttl")
	}
	if cfg.ReaperInterval < minimumReaperInterval || cfg.ReaperInterval > cfg.HardTTL {
		return fmt.Errorf("reaper_interval must be between %s and hard_ttl", minimumReaperInterval)
	}
	if math.IsNaN(cfg.CPUs) || math.IsInf(cfg.CPUs, 0) || cfg.CPUs < minimumCPUs || cfg.CPUs > maximumCPUs {
		return fmt.Errorf("cpus must be finite and between %.2f and %.0f", minimumCPUs, maximumCPUs)
	}
	if cfg.MemoryBytes < 64<<20 || cfg.MemoryBytes > 64<<30 {
		return errors.New("memory must be between 64MiB and 64GiB")
	}
	if cfg.PIDsLimit < 16 || cfg.PIDsLimit > 32768 {
		return errors.New("pids_limit must be between 16 and 32768")
	}
	if cfg.ShmSizeBytes < 1<<20 || cfg.ShmSizeBytes > cfg.MemoryBytes {
		return errors.New("shm_size must be between 1MiB and memory")
	}
	if cfg.TmpSizeBytes < 1<<20 || cfg.TmpSizeBytes > cfg.MemoryBytes {
		return errors.New("tmp_size must be between 1MiB and memory")
	}
	if cfg.WorkspaceSoftLimit <= 0 || cfg.TotalWorkspaceLimit <= 0 || cfg.WorkspaceSoftLimit > cfg.TotalWorkspaceLimit {
		return errors.New("workspace limits must be positive and the per-workspace limit cannot exceed the total")
	}
	if cfg.MinFreeDisk <= 0 {
		return errors.New("min_free_disk must be greater than zero")
	}
	if cfg.MaxOutputBytes < 1 || cfg.MaxOutputBytes > 1<<20 {
		return errors.New("max_output_bytes must be between 1 and 1048576")
	}
	if cfg.CommandTimeout <= 0 || cfg.MaxCommandTimeout <= 0 || cfg.CommandTimeout > cfg.MaxCommandTimeout || cfg.MaxCommandTimeout > 10*time.Minute {
		return errors.New("command timeouts must be positive, ordered, and max_command_timeout cannot exceed 10m")
	}
	return nil
}

func resolveControllerToken(value string, lookupEnv func(string) (string, bool)) (string, error) {
	match := environmentRef.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 2 || match[1] != controllerTokenEnvironment {
		return "", fmt.Errorf("auth_token must be {%s}", controllerTokenEnvironment)
	}
	value, ok := lookupEnv(controllerTokenEnvironment)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment variable %s is required", controllerTokenEnvironment)
	}
	return value, nil
}

func parseByteSize(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("size is required")
	}
	units := []struct {
		suffix string
		factor int64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1_000},
		{"B", 1},
	}
	for _, unit := range units {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed <= 0 || parsed > (1<<63-1)/unit.factor {
			return 0, fmt.Errorf("invalid positive byte size %q", value)
		}
		return parsed * unit.factor, nil
	}
	return 0, fmt.Errorf("invalid byte size %q (use B, KiB, MiB, or GiB)", value)
}
