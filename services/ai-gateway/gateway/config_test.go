package gateway

import "testing"

func TestResolveEnvironmentOnly(t *testing.T) {
	t.Setenv("GW_TEST_API_KEY", "not-a-real-key")
	t.Setenv("GW_TEST_VENDOR_SECRET", "not-a-real-secret")
	c, e := Resolve(FileConfig{Models: map[string]FileModel{"test": {Model: Model{Protocol: "responses", Endpoint: "http://localhost/v1/responses", Model: "u", Headers: map[string]string{"X-Deployment": "local"}}, APIKeyEnv: "GW_TEST_API_KEY", HeaderEnv: map[string]string{"X-Vendor-Secret": "GW_TEST_VENDOR_SECRET"}}}})
	if e != nil {
		t.Fatal(e)
	}
	if c.Models["test"].APIKey != "not-a-real-key" || c.Models["test"].Headers["X-Vendor-Secret"] != "not-a-real-secret" {
		t.Fatal("env references unresolved")
	}
	if _, e = New(c); e != nil {
		t.Fatal(e)
	}
	for _, m := range []FileModel{{Model: Model{Headers: map[string]string{"X-Api-Key": "secret"}}}, {Model: Model{Headers: map[string]string{"Cookie": "secret"}}}, {APIKeyEnv: "GW_MISSING_SECRET_999"}} {
		if _, e := Resolve(FileConfig{Models: map[string]FileModel{"x": m}}); e == nil {
			t.Fatalf("invalid configuration accepted: %+v", m)
		}
	}
}
func TestResolveLeavesFileConfigUntouched(t *testing.T) {
	t.Setenv("GW_TEST_VENDOR_SECRET", "not-a-real-secret")
	file := FileConfig{Models: map[string]FileModel{"test": {Model: Model{Protocol: "responses", Endpoint: "http://localhost/v1/responses", Model: "u", Headers: map[string]string{"X-Deployment": "local"}}, HeaderEnv: map[string]string{"X-Vendor-Secret": "GW_TEST_VENDOR_SECRET"}}}}
	for i := 0; i < 2; i++ {
		c, e := Resolve(file)
		if h := c.Models["test"].Headers; e != nil || h["X-Vendor-Secret"] != "not-a-real-secret" || h["X-Deployment"] != "local" {
			t.Fatalf("resolve %d: %v %v", i, e, h)
		}
	}
	if h := file.Models["test"].Headers; len(h) != 1 || h["X-Deployment"] != "local" {
		t.Fatalf("caller headers modified: %v", h)
	}
}
