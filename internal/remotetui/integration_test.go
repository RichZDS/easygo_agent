package remotetui

import (
	"context"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

// Foreman runs against the actual integrated TS public server with dummy accounts.
// This checks authenticated namespace injection/session/knowledge, without model spend.
func TestPlatformIntegration(t *testing.T) {
	address := os.Getenv("EASYGO_REMOTE_TEST_URL")
	if address == "" {
		t.Skip("requires foreman integrated TS public server; no live provider calls")
	}
	email, password := os.Getenv("EASYGO_REMOTE_TEST_EMAIL"), os.Getenv("EASYGO_REMOTE_TEST_PASSWORD")
	if email == "" || password == "" {
		t.Fatal("dedicated fixture credentials required")
	}
	c, err := NewClient(address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = c.Login(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	defer c.Logout(ctx)
	s, err := c.CreateSession(ctx)
	if err != nil || s.ID == "" {
		t.Fatal(s, err)
	}
	if _, err = c.History(ctx, s.ID, 0); err != nil {
		t.Fatal(err)
	}
	skill := "fixture-" + uuid.NewString()
	var saved struct {
		Version int `json:"version"`
	}
	if err = c.RPC(ctx, "agent.skills.upsert", map[string]any{"name": skill, "description": "integration fixture", "content": "dummy body"}, &saved); err != nil {
		t.Fatal(err)
	}
	defer func() {
		var out any
		if err := c.RPC(ctx, "agent.skills.delete", map[string]any{"name": skill, "expected_version": saved.Version}, &out); err != nil {
			t.Error(err)
		}
	}()
	var got struct {
		Content string `json:"content"`
	}
	if err = c.RPC(ctx, "agent.skills.get", map[string]any{"name": skill}, &got); err != nil || got.Content != "dummy body" {
		t.Fatal(got, err)
	}
	var profile any
	if err = c.RPC(ctx, "agent.memory.list", nil, &profile); err != nil {
		t.Fatal(err)
	}
}
