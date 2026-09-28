package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// FileConfig keeps all credential values outside JSON. HeaderEnv allows vendor
// credentials other than APIKey without embedding secret header values.
type FileConfig struct {
	Models           map[string]FileModel `json:"models"`
	BearerTokenEnv   string               `json:"bearer_token_env,omitempty"`
	MaxRequestBytes  int64                `json:"max_request_bytes,omitempty"`
	MaxResponseBytes int64                `json:"max_response_bytes,omitempty"`
	MaxStreamBytes   int64                `json:"max_stream_bytes,omitempty"`
	MaxEventBytes    int                  `json:"max_event_bytes,omitempty"`
}
type FileModel struct {
	Model
	APIKeyEnv string            `json:"api_key_env,omitempty"`
	HeaderEnv map[string]string `json:"header_env,omitempty"`
}

// LoadConfig strictly decodes JSON and resolves environment variable references.
// A named but missing/empty environment variable fails closed. It does not read
// dotenv files or log environment values. New validates the resolved models.
func LoadConfig(r io.Reader) (Config, HandlerConfig, error) {
	var file FileConfig
	raw, err := readBounded(r, defaultBodyLimit)
	if err != nil {
		return Config{}, HandlerConfig{}, fail("invalid_config", "configuration exceeds limit or cannot be read")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&file) != nil {
		return Config{}, HandlerConfig{}, fail("invalid_config", "invalid gateway configuration JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Config{}, HandlerConfig{}, fail("invalid_config", "trailing configuration data")
	}
	c := Config{Models: make(map[string]Model), MaxResponseBytes: file.MaxResponseBytes, MaxStreamBytes: file.MaxStreamBytes, MaxEventBytes: file.MaxEventBytes}
	h := HandlerConfig{MaxRequestBytes: file.MaxRequestBytes}
	resolve := func(name string) (string, error) {
		if name == "" {
			return "", nil
		}
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			return "", fail("missing_environment", "configured credential environment variable is unset")
		}
		return value, nil
	}
	h.BearerToken, err = resolve(file.BearerTokenEnv)
	if err != nil {
		return Config{}, HandlerConfig{}, err
	}
	for alias, f := range file.Models {
		f.Model.APIKey, err = resolve(f.APIKeyEnv)
		if err != nil {
			return Config{}, HandlerConfig{}, err
		}
		if f.Headers == nil {
			f.Headers = map[string]string{}
		}
		for k, v := range f.Headers {
			if strings.Contains(strings.ToLower(k), "key") || strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "auth") || strings.Contains(strings.ToLower(k), "secret") || strings.EqualFold(k, "Cookie") {
				return Config{}, HandlerConfig{}, fail("invalid_config", "credential headers require header_env")
			}
			if !validHeader(k, v) {
				return Config{}, HandlerConfig{}, fail("invalid_config", "invalid header")
			}
		}
		for k, name := range f.HeaderEnv {
			if name == "" {
				return Config{}, HandlerConfig{}, fail("invalid_config", "header_env requires an environment name")
			}
			if _, ok := f.Headers[k]; ok {
				return Config{}, HandlerConfig{}, fail("invalid_config", "header and header_env overlap")
			}
			value, e := resolve(name)
			if e != nil {
				return Config{}, HandlerConfig{}, e
			}
			f.Headers[k] = value
		}
		c.Models[alias] = f.Model
	}
	if h.MaxRequestBytes < 0 {
		return Config{}, HandlerConfig{}, fail("invalid_config", "request limit must be positive")
	}
	return c, h, nil
}
