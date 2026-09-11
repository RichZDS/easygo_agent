package sandbox

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

const testConfigToken = "test-config-controller-token-0123456789abcdef"

func TestLoadConfigUsesSafeDefaultsAndOnlyDocumentedEnvironmentOverrides(t *testing.T) {
	path := t.TempDir() + "/controller.yaml"
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, func(name string) (string, bool) {
		switch name {
		case controllerTokenEnvironment:
			return testConfigToken, true
		case dockerSocketEnvironment:
			return "/custom/docker.sock", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRunning != 3 || cfg.MaxApplications != 30 || cfg.MaxStarting != 2 {
		t.Fatalf("capacity defaults=%d/%d/%d", cfg.MaxRunning, cfg.MaxApplications, cfg.MaxStarting)
	}
	if cfg.BaseIdleTTL != time.Minute || cfg.HardTTL != 5*time.Hour || cfg.CommandTimeout != 2*time.Minute || cfg.MaxCommandTimeout != 10*time.Minute {
		t.Fatalf("duration defaults=%+v", cfg)
	}
	if cfg.DockerHost != "unix:///custom/docker.sock" || cfg.AuthToken != testConfigToken {
		t.Fatalf("environment resolution=%+v", cfg)
	}
}

func TestLoadConfigRejectsUnknownFieldsMultipleDocumentsAndUndocumentedTokenReferences(t *testing.T) {
	for name, content := range map[string]string{
		"unknown":       "unknown_field: true\n",
		"multiple":      "{}\n---\n{}\n",
		"wrong_token":   "auth_token: '{OTHER_TOKEN}'\n",
		"literal_token": "auth_token: 'literal-controller-token-0123456789abcdef'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/controller.yaml"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path, func(name string) (string, bool) {
				if name == controllerTokenEnvironment {
					return testConfigToken, true
				}
				return "", false
			})
			if err == nil {
				t.Fatalf("invalid config accepted: %s", content)
			}
		})
	}
}

func TestConfigValidationCoversEverySafetyBoundary(t *testing.T) {
	base := testConfig()
	tests := map[string]func(*Config){
		"namespace":             func(cfg *Config) { cfg.Namespace = "bad namespace" },
		"listen":                func(cfg *Config) { cfg.ListenAddress = "missing-port" },
		"token":                 func(cfg *Config) { cfg.AuthToken = "short" },
		"state_path":            func(cfg *Config) { cfg.StatePath = "relative.db" },
		"docker_host":           func(cfg *Config) { cfg.DockerHost = "tcp://127.0.0.1:2375" },
		"image":                 func(cfg *Config) { cfg.Image = "" },
		"max_running_zero":      func(cfg *Config) { cfg.MaxRunning = 0 },
		"max_running_too_large": func(cfg *Config) { cfg.MaxRunning = maximumRunning + 1 },
		"max_apps_zero":         func(cfg *Config) { cfg.MaxApplications = 0 },
		"max_apps_too_large":    func(cfg *Config) { cfg.MaxApplications = maximumApplications + 1 },
		"max_running_apps":      func(cfg *Config) { cfg.MaxRunning = cfg.MaxApplications + 1 },
		"max_starting_zero":     func(cfg *Config) { cfg.MaxStarting = 0 },
		"max_starting":          func(cfg *Config) { cfg.MaxStarting = cfg.MaxRunning + 1 },
		"wait_timeout":          func(cfg *Config) { cfg.WaitTimeout = 11 * time.Minute },
		"wait_timeout_tiny":     func(cfg *Config) { cfg.WaitTimeout = time.Nanosecond },
		"base_idle_ttl":         func(cfg *Config) { cfg.BaseIdleTTL = 0 },
		"base_idle_ttl_tiny":    func(cfg *Config) { cfg.BaseIdleTTL = time.Nanosecond },
		"hard_ttl_zero":         func(cfg *Config) { cfg.HardTTL = 0 },
		"hard_ttl_above_max":    func(cfg *Config) { cfg.HardTTL = 5*time.Hour + time.Nanosecond },
		"hard_ttl_below_idle":   func(cfg *Config) { cfg.HardTTL = 30 * time.Second },
		"reaper":                func(cfg *Config) { cfg.ReaperInterval = 0 },
		"reaper_tiny":           func(cfg *Config) { cfg.ReaperInterval = time.Nanosecond },
		"reaper_above_hard":     func(cfg *Config) { cfg.ReaperInterval = cfg.HardTTL + time.Second },
		"cpus_zero":             func(cfg *Config) { cfg.CPUs = 0 },
		"cpus_negative":         func(cfg *Config) { cfg.CPUs = -1 },
		"cpus_below_minimum":    func(cfg *Config) { cfg.CPUs = 0.001 },
		"cpus_too_large":        func(cfg *Config) { cfg.CPUs = maximumCPUs + 1 },
		"cpus_nan":              func(cfg *Config) { cfg.CPUs = math.NaN() },
		"cpus_infinity":         func(cfg *Config) { cfg.CPUs = math.Inf(1) },
		"memory":                func(cfg *Config) { cfg.MemoryBytes = 32 << 20 },
		"memory_too_large":      func(cfg *Config) { cfg.MemoryBytes = (64 << 30) + 1 },
		"pids":                  func(cfg *Config) { cfg.PIDsLimit = 15 },
		"pids_too_large":        func(cfg *Config) { cfg.PIDsLimit = 32769 },
		"shm":                   func(cfg *Config) { cfg.ShmSizeBytes = cfg.MemoryBytes + 1 },
		"shm_too_small":         func(cfg *Config) { cfg.ShmSizeBytes = 1<<20 - 1 },
		"tmp":                   func(cfg *Config) { cfg.TmpSizeBytes = cfg.MemoryBytes + 1 },
		"tmp_too_small":         func(cfg *Config) { cfg.TmpSizeBytes = 1<<20 - 1 },
		"workspace_limits":      func(cfg *Config) { cfg.WorkspaceSoftLimit = cfg.TotalWorkspaceLimit + 1 },
		"workspace_soft_zero":   func(cfg *Config) { cfg.WorkspaceSoftLimit = 0 },
		"workspace_total_zero":  func(cfg *Config) { cfg.TotalWorkspaceLimit = 0 },
		"free_disk":             func(cfg *Config) { cfg.MinFreeDisk = 0 },
		"output":                func(cfg *Config) { cfg.MaxOutputBytes = 1<<20 + 1 },
		"output_zero":           func(cfg *Config) { cfg.MaxOutputBytes = 0 },
		"command_timeout_zero":  func(cfg *Config) { cfg.CommandTimeout = 0 },
		"command_timeout_order": func(cfg *Config) { cfg.CommandTimeout = cfg.MaxCommandTimeout + time.Second },
		"command_timeout_max":   func(cfg *Config) { cfg.MaxCommandTimeout = 10*time.Minute + time.Second },
		"token_space":           func(cfg *Config) { cfg.AuthToken = "test-controller-token 0123456789abcdef" },
		"token_nonprintable":    func(cfg *Config) { cfg.AuthToken = "test-controller-token-0123456789abcde\x7f" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("invalid config accepted: %+v", cfg)
			}
		})
	}
}

func TestConfigRejectsEveryNonLocalDockerEndpointForm(t *testing.T) {
	for _, host := range []string{
		"tcp://127.0.0.1:2375",
		"http://127.0.0.1:2375",
		"unix://relative/socket",
		"unix:///var/run/docker.sock?option=value",
		"unix:///var/run/docker.sock#fragment",
		"unix:///var/run/%64ocker.sock",
	} {
		t.Run(strings.NewReplacer(":", "_", "/", "_").Replace(host), func(t *testing.T) {
			cfg := testConfig()
			cfg.DockerHost = host
			if err := cfg.Validate(); err == nil {
				t.Fatalf("non-local Docker endpoint %q was accepted", host)
			}
		})
	}
}

func TestParseByteSizeUsesExplicitBinaryOrDecimalUnits(t *testing.T) {
	for input, want := range map[string]int64{"1B": 1, "2KiB": 2 << 10, "3MiB": 3 << 20, "4GiB": 4 << 30, "5MB": 5_000_000} {
		got, err := parseByteSize(input)
		if err != nil || got != want {
			t.Fatalf("parseByteSize(%q)=%d,%v want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0B", "1.5GiB", "1", "-1MiB", strings.Repeat("9", 40) + "GiB"} {
		if _, err := parseByteSize(input); err == nil {
			t.Fatalf("invalid size %q accepted", input)
		}
	}
}
