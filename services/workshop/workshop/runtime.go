package workshop

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"easygo-agent/rpc"
)

// RuntimeProfile is operator-controlled; callers select its ID, never command
// flags, URLs or credentials. GatewayModel reuses an existing AI gateway route.
type RuntimeProfile struct {
	Engine       string `json:"engine"`
	Protocol     string `json:"protocol"`
	Model        string `json:"model,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	APIKeyEnv    string `json:"api_key_env,omitempty"`
	GatewayModel string `json:"gateway_model,omitempty"`
}
type ModelGateway struct {
	URL                 string        `json:"url"`
	PeerCertificateFile string        `json:"peer_certificate_file"`
	TLS                 rpc.TLSConfig `json:"tls"`
}
type RuntimeChoice struct {
	ID       string `json:"id"`
	Engine   string `json:"engine"`
	Model    string `json:"model"`
	Protocol string `json:"protocol"`
	Source   string `json:"source"`
}

func knownEngine(engine string) bool {
	return engine == "codex" || engine == "claude" || engine == "pi" || engine == "openclaw"
}
func (p RuntimeProfile) validate() error {
	if !knownEngine(p.Engine) {
		return fmt.Errorf("%w: unknown runtime engine", ErrInvalid)
	}
	if p.Protocol != "responses" && p.Protocol != "anthropic" && p.Protocol != "chat_completions" {
		return fmt.Errorf("%w: unsupported runtime protocol", ErrInvalid)
	}
	if (p.Engine == "codex" && p.Protocol != "responses") || (p.Engine == "claude" && p.Protocol != "anthropic") {
		return fmt.Errorf("%w: runtime protocol mismatch", ErrInvalid)
	}
	if p.GatewayModel != "" {
		if p.BaseURL != "" || p.Model != "" || p.APIKeyEnv != "" {
			return fmt.Errorf("%w: gateway route cannot contain direct provider settings", ErrInvalid)
		}
	} else {
		u, e := url.Parse(p.BaseURL)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(p.Model) == "" || !envName.MatchString(p.APIKeyEnv) {
			return fmt.Errorf("%w: runtime requires model, base URL and credential environment reference", ErrInvalid)
		}
	}
	return nil
}
func (p RuntimeProfile) model() string {
	if p.GatewayModel != "" {
		return p.GatewayModel
	}
	return p.Model
}
func (p RuntimeProfile) choice(id string) RuntimeChoice {
	source := "direct"
	if p.GatewayModel != "" {
		source = "gateway"
	}
	return RuntimeChoice{ID: id, Engine: p.Engine, Model: p.model(), Protocol: p.Protocol, Source: source}
}
func (s *Service) selectRuntime(w Workflow, id string) (Workflow, error) {
	if id == "" {
		id = w.Runtime
	}
	if id == "" {
		return w, nil
	}
	if id != w.Runtime && !slices.Contains(w.AllowedRuntimes, id) {
		return w, fmt.Errorf("%w: runtime not allowed for workflow", ErrInvalid)
	}
	p, ok := s.runtimes[id]
	if !ok {
		return w, fmt.Errorf("%w: unknown runtime profile", ErrInvalid)
	}
	w.Runtime = id
	w.Engine = p.Engine
	w.Model = p.model()
	w.RuntimeSpec = &p
	return w, nil
}
