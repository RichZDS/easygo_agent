package gateway

import (
	"os"
	"strings"
)

// FileConfig keeps all credential values outside JSON. HeaderEnv allows vendor
// credentials other than APIKey without embedding secret header values.
type FileConfig struct {
	Models           map[string]FileModel `json:"models"`
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

// Resolve resolves environment variable references in a decoded FileConfig.
// A named but missing/empty environment variable fails closed. It does not read
// dotenv files or log environment values, and it never modifies file's maps.
// New validates the resolved models.
func Resolve(file FileConfig) (Config, error) {
	var err error
	c := Config{Models: make(map[string]Model), MaxResponseBytes: file.MaxResponseBytes, MaxStreamBytes: file.MaxStreamBytes, MaxEventBytes: file.MaxEventBytes}
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
	for alias, f := range file.Models {
		f.Model.APIKey, err = resolve(f.APIKeyEnv)
		if err != nil {
			return Config{}, err
		}
		headers := make(map[string]string, len(f.Headers)+len(f.HeaderEnv))
		for k, v := range f.Headers {
			headers[k] = v
		}
		f.Headers = headers
		for k, v := range f.Headers {
			if strings.Contains(strings.ToLower(k), "key") || strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "auth") || strings.Contains(strings.ToLower(k), "secret") || strings.EqualFold(k, "Cookie") {
				return Config{}, fail("invalid_config", "credential headers require header_env")
			}
			if !validHeader(k, v) {
				return Config{}, fail("invalid_config", "invalid header")
			}
		}
		for k, name := range f.HeaderEnv {
			if name == "" {
				return Config{}, fail("invalid_config", "header_env requires an environment name")
			}
			if _, ok := f.Headers[k]; ok {
				return Config{}, fail("invalid_config", "header and header_env overlap")
			}
			value, e := resolve(name)
			if e != nil {
				return Config{}, e
			}
			f.Headers[k] = value
		}
		c.Models[alias] = f.Model
	}
	if file.MaxRequestBytes < 0 {
		return Config{}, fail("invalid_config", "request limit must be positive")
	}
	return c, nil
}
