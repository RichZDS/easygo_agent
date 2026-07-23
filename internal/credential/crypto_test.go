package credential

import (
	"encoding/base64"
	"testing"
)

func TestCipherRoundTripAndAssociatedData(t *testing.T) {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := NewFromBase64(base64.StdEncoding.EncodeToString(key), "v1")
	if err != nil {
		t.Fatal(err)
	}
	value, err := c.Encrypt("sk-user-secret", []byte("user:7:provider:deepseek"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(value, []byte("user:7:provider:deepseek"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-user-secret" {
		t.Fatalf("got %q", got)
	}
	if _, err := c.Decrypt(value, []byte("user:8:provider:deepseek")); err == nil {
		t.Fatal("expected associated-data failure")
	}
}

func TestCipherRejectsNon32ByteKey(t *testing.T) {
	if _, err := NewFromBase64(base64.StdEncoding.EncodeToString(make([]byte, 31)), "v1"); err == nil {
		t.Fatal("expected key length failure")
	}
}
