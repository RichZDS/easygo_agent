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
