package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"easygo-agent/rpc"
	"github.com/google/uuid"
)

func TestDockerAndHostShellTools(t *testing.T) {
	for _, engine := range []string{"claude", "pi", "openclaw"} {
		for _, policy := range []string{"read-only", "workspace-write"} {
			for _, resume := range []bool{false, true} {
				name := engine + "/" + policy
				if resume {
					name += "/resume"
				}
				t.Run(name, func(t *testing.T) {
					r, f, in := dockerFixture(t)
					in.Workflow.Engine = engine
					in.Workflow.Policy = policy
					in.Workflow.RuntimeSpec.Engine = engine
					if engine == "claude" {
						in.Workflow.RuntimeSpec.Protocol = "anthropic"
					}
					if resume {
						in.SessionID = "11111111-1111-4111-8111-111111111111"
					}
					hostArgs, err := engineArgs(in)
					if err != nil {
						t.Fatal(err)
					}
					hostEnv := map[string]string{"HOME": t.TempDir()}
					var hostConfig map[string]any
					hostArgs, err = configureRuntimeEndpoint(in, hostArgs, hostEnv, "http://127.0.0.1:1/v1", uuid.NewString(), func(_ string, value any) error {
						if engine == "openclaw" {
							hostConfig = value.(map[string]any)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if engine == "openclaw" {
						tools := hostConfig["tools"].(map[string]any)
						if slices.Contains(tools["allow"].([]string), "exec") || tools["exec"] != nil {
							t.Fatal("host OpenClaw gained shell")
						}
					} else {
						shell := map[string]string{"claude": "Bash", "pi": "bash"}[engine]
						if slices.Contains(strings.Split(option(hostArgs, "--tools"), ","), shell) || slices.Contains(hostArgs, "--allowedTools") {
							t.Fatal("host gained shell")
						}
					}
					r.gateway = nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
					observed := false
					f.start = func(_ context.Context, c *fakeContainer, _ io.Reader, _, _ io.Writer) error {
						observed = true
						workspaceRO := false
						for i, arg := range c.args {
							if arg == "--mount" && strings.Contains(c.args[i+1], ",dst=/workspace,") {
								workspaceRO = strings.HasSuffix(c.args[i+1], ",readonly")
							}
						}
						if workspaceRO != (policy == "read-only") {
							t.Fatal("workspace mount policy changed")
						}
						if option(c.args, "--network") != "none" || option(c.args, "--user") != "1000:1000" {
							t.Fatal("outer boundary changed")
						}
						switch engine {
						case "claude":
							if !slices.Contains(strings.Split(option(c.args, "--tools"), ","), "Bash") || option(c.args, "--allowedTools") != "Bash" || !slices.Contains(c.args, "--bare") || !slices.Contains(c.args, "--restricted") || !slices.Contains(c.args, "--strict-mcp-config") || option(c.args, "--permission-prompts") != "none" {
								t.Fatal("Docker Claude shell configuration")
							}
							if option(c.args, "--resume") != in.SessionID {
								t.Fatal("Claude resume changed")
							}
						case "pi":
							if !slices.Contains(strings.Split(option(c.args, "--tools"), ","), "bash") || !slices.Contains(c.args, "--no-extensions") || !slices.Contains(c.args, "--no-approve") {
								t.Fatal("Docker Pi shell configuration")
							}
							if resume && option(c.args, "--session-id") != in.SessionID {
								t.Fatal("Pi resume changed")
							}
						case "openclaw":
							raw, err := os.ReadFile(filepath.Join(in.Workspace, ".workshop-home", "openclaw", "openclaw.json"))
							if err != nil {
								t.Fatal(err)
							}
							var config struct {
								Tools struct {
									Allow []string
									Exec  struct{ Host, Mode string }
								}
								Plugins struct{ Enabled bool }
							}
							if json.Unmarshal(raw, &config) != nil || !slices.Contains(config.Tools.Allow, "exec") || config.Tools.Exec.Host != "gateway" || config.Tools.Exec.Mode != "full" || config.Plugins.Enabled {
								t.Fatal("Docker OpenClaw shell configuration")
							}
							if resume && option(c.args, "--session-id") != in.SessionID {
								t.Fatal("OpenClaw resume changed")
							}
						}
						return errors.New("configuration observed; no model call")
					}
					if _, err := r.Run(context.Background(), in, func(Event) error { return nil }); err == nil || !observed {
						t.Fatal("Docker path not exercised", err)
					}
					r.initialized = false
					before := len(f.calls)
					if _, err := r.Run(context.Background(), in, func(Event) error { return nil }); err == nil || len(f.calls) != before {
						t.Fatal("uninitialized runner granted shell")
					}
				})
			}
		}
	}
}
