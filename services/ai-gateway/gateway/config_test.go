package gateway

import (
	"strings"
	"testing"
)

func TestLoadConfigEnvironmentOnly(t *testing.T) {
	t.Setenv("GW_TEST_API_KEY", "not-a-real-key")
	t.Setenv("GW_TEST_BEARER", "not-a-real-token")
	t.Setenv("GW_TEST_VENDOR_SECRET", "not-a-real-secret")
	c, h, e := LoadConfig(strings.NewReader(`{"bearer_token_env":"GW_TEST_BEARER","models":{"test":{"protocol":"responses","endpoint":"http://localhost/v1/responses","model":"u","api_key_env":"GW_TEST_API_KEY","header_env":{"X-Vendor-Secret":"GW_TEST_VENDOR_SECRET"},"headers":{"X-Deployment":"local"}}}}`))
	if e != nil {
		t.Fatal(e)
	}
	if c.Models["test"].APIKey != "not-a-real-key" || h.BearerToken != "not-a-real-token" || c.Models["test"].Headers["X-Vendor-Secret"] != "not-a-real-secret" {
		t.Fatal("env references unresolved")
	}
	if _, e = New(c); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`{"models":{"x":{"api_key":"secret"}}}`, `{"bearer_token":"secret"}`, `{"models":{"x":{"headers":{"X-Api-Key":"secret"}}}}`, `{"models":{"x":{"headers":{"Cookie":"secret"}}}}`, `{"models":{"x":{"api_key_env":"GW_MISSING_SECRET_999"}}}`, `{} {}`} {
		if _, _, e := LoadConfig(strings.NewReader(raw)); e == nil {
			t.Fatalf("invalid configuration accepted: %s", raw)
		}
	}
}
func TestResolveLeavesFileConfigUntouched(t *testing.T) {
	t.Setenv("GW_TEST_VENDOR_SECRET", "not-a-real-secret")
	file := FileConfig{Models: map[string]FileModel{"test": {Model: Model{Protocol: "responses", Endpoint: "http://localhost/v1/responses", Model: "u", Headers: map[string]string{"X-Deployment": "local"}}, HeaderEnv: map[string]string{"X-Vendor-Secret": "GW_TEST_VENDOR_SECRET"}}}}
	for i := 0; i < 2; i++ {
		c, _, e := Resolve(file)
		if h := c.Models["test"].Headers; e != nil || h["X-Vendor-Secret"] != "not-a-real-secret" || h["X-Deployment"] != "local" {
			t.Fatalf("resolve %d: %v %v", i, e, h)
		}
	}
	if h := file.Models["test"].Headers; len(h) != 1 || h["X-Deployment"] != "local" {
		t.Fatalf("caller headers modified: %v", h)
	}
}
func TestLoadConfigNamesUnknownField(t *testing.T) {
	_, _, e := LoadConfig(strings.NewReader(`{"models":{},"extra_field":1}`))
	requireCode(t, e, "invalid_config")
	if !strings.Contains(e.Error(), `"extra_field"`) {
		t.Fatalf("unknown field not named: %v", e)
	}
}
