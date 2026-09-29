//go:build linux

package workshop

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestRunNativeDiagnosticsNeverLeaveSecretPrefixAtBoundary formalizes R1's
// W-1 review probe: stderr fills the diagnostics buffer with a complete
// secret at the start and the same secret's first len-1 characters at the
// end. Redacting the leading full occurrence shrinks the buffer, so the
// later diagnosticLimit cut used to land inside the trailing partial
// occurrence and leave a 53-character fragment of the secret in the emitted
// diagnostic event.
func TestRunNativeDiagnosticsNeverLeaveSecretPrefixAtBoundary(t *testing.T) {
	secret := strings.Repeat("0123456789abcdef", 4) // 64 hex chars
	tail := secret[:len(secret)-1]
	limit := diagnosticLimit + maxSecretLength([]string{secret})
	payload := secret + strings.Repeat("B", limit-len(secret)-len(tail)) + tail
	var events []Event
	_, err := runNative(context.Background(), "codex", 1<<20, []string{secret}, func(event Event) error {
		events = append(events, event)
		return nil
	}, func(ctx context.Context, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, payload)
		fakeSuccess(stdout)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(events)
	for n := minSecretSuffixLength; n <= len(secret); n++ {
		if strings.Contains(string(blob), secret[:n]) {
			t.Fatalf("diagnostic events leak a %d-byte secret prefix: %s", n, blob)
		}
	}
	var diagnosticText string
	found := false
	for _, event := range events {
		if event.Kind == "diagnostic" {
			diagnosticText, found = event.Text, true
		}
	}
	if !found {
		t.Fatal("missing diagnostic event")
	}
	// The fix must remove no more than the leaking suffix: most of the
	// padding should survive.
	if len(diagnosticText) < diagnosticLimit-100 {
		t.Fatalf("diagnostic text over-truncated: %d bytes", len(diagnosticText))
	}
}

// TestRedactAndBoundEdgeCases exercises secretPrefixSuffixLength/
// redactAndBound directly, covering: a secret straddling the truncation
// point, multiple distinct secrets, no secrets configured, a buffer exactly
// at the limit, and a buffer one byte under the limit.
func TestRedactAndBoundEdgeCases(t *testing.T) {
	secretA := "abcdefghijklmnopqrst" // 20 chars
	secretB := "0123456789ZYXWVUTSRQ" // 20 chars, distinct from secretA
	cases := []struct {
		name      string
		secrets   []string
		raw       string
		limit     int
		wantText  string
		wantTrunc bool
	}{
		{
			name:      "straddles-truncation-point",
			secrets:   []string{secretA},
			raw:       secretA + secretA[:len(secretA)-1],
			limit:     25,
			wantText:  "[REDACTED]",
			wantTrunc: true,
		},
		{
			name:      "multiple-secrets-both-checked",
			secrets:   []string{secretA, secretB},
			raw:       secretA + secretB[:10],
			limit:     18,
			wantText:  "[REDACTED]",
			wantTrunc: true,
		},
		{
			name:      "no-secret-configured",
			secrets:   nil,
			raw:       "plain diagnostic output with nothing sensitive",
			limit:     1000,
			wantText:  "plain diagnostic output with nothing sensitive",
			wantTrunc: false,
		},
		{
			name:      "buffer-exactly-at-limit",
			secrets:   []string{secretA},
			raw:       strings.Repeat("x", 40),
			limit:     40,
			wantText:  strings.Repeat("x", 40),
			wantTrunc: false,
		},
		{
			name:      "buffer-one-under-limit",
			secrets:   []string{secretA},
			raw:       strings.Repeat("x", 39),
			limit:     40,
			wantText:  strings.Repeat("x", 39),
			wantTrunc: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, truncated := redactAndBound(c.raw, redactor(c.secrets), c.secrets, c.limit)
			if text != c.wantText || truncated != c.wantTrunc {
				t.Fatalf("got text=%q truncated=%v, want text=%q truncated=%v", text, truncated, c.wantText, c.wantTrunc)
			}
			for _, secret := range c.secrets {
				for n := minSecretSuffixLength; n <= len(secret); n++ {
					if strings.HasSuffix(text, secret[:n]) {
						t.Fatalf("result still ends with a %d-byte secret prefix", n)
					}
				}
			}
		})
	}
}
