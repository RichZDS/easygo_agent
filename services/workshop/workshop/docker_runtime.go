package workshop

import (
	"errors"
	"slices"
	"strings"
)

// Only the initialized DockerRunner calls these overrides. Shell execution is
// confined by the outer container, including read-only workspace mounts; host
// CommandRunner continues to use its file-only tool lists.
func dockerRuntimeArgs(engine string, args []string) ([]string, error) {
	tool := ""
	switch engine {
	case "claude":
		tool = "Bash"
	case "pi":
		tool = "bash"
	default:
		return args, nil
	}
	args = slices.Clone(args)
	index := slices.Index(args, "--tools")
	if index < 0 || index+1 >= len(args) {
		return nil, errors.New("missing native tool allowlist")
	}
	allowed := strings.Split(args[index+1], ",")
	if !slices.Contains(allowed, tool) {
		allowed = append(allowed, tool)
	}
	args[index+1] = strings.Join(allowed, ",")
	if engine == "claude" {
		// 2.1.281 --restricted explicitly permits tools named by --tools; --bare
		// preserves built-ins. Keep both, and allow Bash without an interactive
		// permission prompt. The outer container is the execution boundary.
		args = append(args, "--allowedTools", "Bash")
	}
	return args, nil
}
func dockerRuntimeConfig(engine string, value any) (any, error) {
	if engine != "openclaw" {
		return value, nil
	}
	config, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid OpenClaw runtime config")
	}
	tools, ok := config["tools"].(map[string]any)
	if !ok {
		return nil, errors.New("missing OpenClaw tool policy")
	}
	allow, ok := tools["allow"].([]string)
	if !ok {
		return nil, errors.New("missing OpenClaw tool allowlist")
	}
	tools["allow"] = append(slices.Clone(allow), "exec")
	// The embedded CLI's gateway target is this task container, not the host
	// controller. No inner sandbox daemon or interactive approver is available.
	tools["exec"] = map[string]any{"host": "gateway", "mode": "full"}
	return value, nil
}
