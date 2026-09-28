package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func writeRuntimeJSON(path string, value any) error {
	if err := mkdirNoSymlinks(filepath.Dir(path), 0700); err != nil {
		return err
	}
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime config directory must not be a symlink")
		}
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("runtime config must be a regular file")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
func apiName(protocol string) string {
	return map[string]string{"responses": "openai-responses", "anthropic": "anthropic-messages", "chat_completions": "openai-completions"}[protocol]
}
func reservedEnvironment(name string) bool {
	return name == "HOME" || name == "PATH" || name == "CODEX_HOME" || name == "CLAUDE_CONFIG_DIR" || name == "NODE_OPTIONS" || name == "LD_PRELOAD" || name == "LD_LIBRARY_PATH" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "PI_") || strings.HasPrefix(name, "OPENCLAW_") || strings.HasPrefix(name, "EASYGO_RUNTIME_")
}

// configureRuntime returns task-only args/env. Secret values stay in process
// environment; generated provider files contain environment references only.
func (r *CommandRunner) configureRuntime(ctx context.Context, in Invocation, args []string, env map[string]string) ([]string, []string, func(), error) {
	cleanup := func() {}
	p := in.Workflow.RuntimeSpec
	if p == nil {
		if in.Workflow.Engine == "pi" || in.Workflow.Engine == "openclaw" {
			return nil, nil, cleanup, errors.New("runtime profile required")
		}
		return args, nil, cleanup, nil
	}
	if err := p.validate(); err != nil {
		return nil, nil, cleanup, err
	}
	baseURL, key := p.BaseURL, ""
	if p.GatewayModel != "" {
		var err error
		baseURL, key, cleanup, err = startModelRelay(ctx, r.gateway, in.Namespace, *p)
		if err != nil {
			return nil, nil, func() {}, err
		}
	} else {
		key = os.Getenv(p.APIKeyEnv)
		if key == "" {
			return nil, nil, cleanup, errors.New("runtime credential is unset")
		}
	}
	args, err := configureRuntimeEndpoint(in, args, env, baseURL, key, writeRuntimeJSON)
	if err != nil {
		cleanup()
		return nil, nil, func() {}, err
	}
	return args, []string{key}, cleanup, nil
}

// writeConfig maps container paths to controller paths without rewriting content.
func configureRuntimeEndpoint(in Invocation, args []string, env map[string]string, baseURL, key string, writeConfig func(string, any) error) ([]string, error) {
	p := in.Workflow.RuntimeSpec
	env["EASYGO_RUNTIME_API_KEY"] = key
	model := p.model()
	home := env["HOME"]
	quote := func(value string) string { b, _ := json.Marshal(value); return string(b) }
	switch p.Engine {
	case "codex":
		// CLI -c overrides survive --ignore-user-config, unlike config.toml.
		options := []string{"-c", `model_provider="easygo"`, "-c", `model_providers.easygo.name="EasyGo"`, "-c", "model_providers.easygo.base_url=" + quote(baseURL), "-c", `model_providers.easygo.env_key="EASYGO_RUNTIME_API_KEY"`, "-c", `model_providers.easygo.wire_api="responses"`, "-c", `model_providers.easygo.request_max_retries=0`, "-c", `model_providers.easygo.stream_max_retries=0`}
		position := 1
		if len(args) > 1 && args[1] == "resume" {
			position = 2
		}
		configured := append([]string{}, args[:position]...)
		configured = append(configured, options...)
		args = append(configured, args[position:]...)
	case "claude":
		// Anthropic SDK appends /v1/messages to the base URL.
		env["ANTHROPIC_BASE_URL"] = strings.TrimSuffix(baseURL, "/v1")
		env["ANTHROPIC_API_KEY"] = key
		delete(env, "ANTHROPIC_AUTH_TOKEN")
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
	case "pi":
		dir := filepath.Join(home, "pi")
		env["PI_CODING_AGENT_DIR"] = dir
		env["PI_OFFLINE"] = "1"
		env["PI_TELEMETRY"] = "0"
		config := map[string]any{"providers": map[string]any{"easygo": map[string]any{"baseUrl": baseURL, "api": apiName(p.Protocol), "apiKey": "${EASYGO_RUNTIME_API_KEY}", "models": []any{map[string]any{"id": model, "name": model, "reasoning": false, "input": []string{"text"}, "contextWindow": 128000, "maxTokens": 8192}}}}}
		if err := writeConfig(filepath.Join(dir, "models.json"), config); err != nil {
			return nil, err
		}
		session := in.SessionID
		if session == "" {
			session = uuid.NewString()
		}
		args = append(args, "--provider", "easygo", "--session-dir", filepath.Join(dir, "sessions"), "--session-id", session)
	case "openclaw":
		dir := filepath.Join(home, "openclaw")
		env["OPENCLAW_HOME"] = home
		env["OPENCLAW_STATE_DIR"] = dir
		env["OPENCLAW_CONFIG_PATH"] = filepath.Join(dir, "openclaw.json")
		tools := []string{"read"}
		if in.Workflow.Policy == "workspace-write" {
			tools = append(tools, "write", "edit")
		}
		config := map[string]any{
			"models":  map[string]any{"mode": "replace", "providers": map[string]any{"easygo": map[string]any{"baseUrl": baseURL, "api": apiName(p.Protocol), "apiKey": "${EASYGO_RUNTIME_API_KEY}", "models": []any{map[string]any{"id": model, "name": model, "reasoning": false, "input": []string{"text"}, "contextWindow": 128000, "maxTokens": 8192}}}}},
			"agents":  map[string]any{"defaults": map[string]any{"workspace": in.Workspace, "skipBootstrap": true, "model": map[string]any{"primary": "easygo/" + model}}},
			"tools":   map[string]any{"allow": tools, "fs": map[string]any{"workspaceOnly": true}},
			"plugins": map[string]any{"enabled": false},
		}
		if err := writeConfig(env["OPENCLAW_CONFIG_PATH"], config); err != nil {
			return nil, err
		}
		session := in.SessionID
		if session == "" {
			session = uuid.NewString()
		}
		args = append(args, "--session-id", session, "--message", in.Workflow.Instructions+"\n\nUser input:\n"+in.Input)
	}
	return args, nil
}
