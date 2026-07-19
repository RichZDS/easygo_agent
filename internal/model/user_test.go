package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUserSecretsAreNotSerialized(t *testing.T) {
	user := User{Name: "alice", PasswordHash: "hash-value", Salt: "salt-value"}
	encoded, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	value := string(encoded)
	if strings.Contains(value, "hash-value") || strings.Contains(value, "salt-value") || strings.Contains(value, "password") {
		t.Fatalf("serialized user exposes secrets: %s", value)
	}
}

func TestUserTableName(t *testing.T) {
	if got := (User{}).TableName(); got != "user" {
		t.Fatalf("TableName() = %q, want user", got)
	}
}
