// easygo-crew is the task-scoped worker/foreman channel client.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	base, token := getenv("EASYGO_CREW_URL"), getenv("EASYGO_CREW_TOKEN")
	fail := func(code int, message string) int {
		fmt.Fprintln(stderr, strings.ReplaceAll(message, token, "[REDACTED]"))
		return code
	}
	if base == "" || token == "" {
		fmt.Fprintln(stderr, "crew channel unavailable")
		return 2
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fail(2, "invalid crew URL")
	}
	if len(args) == 0 {
		return fail(2, "usage: easygo-crew report|ask|blocked|submit|inbox")
	}
	path := "/messages"
	var payload any
	switch args[0] {
	case "report", "ask", "blocked":
		if len(args) != 2 {
			return fail(2, "message text required")
		}
		payload = map[string]any{"client_id": uuid.NewString(), "kind": args[0], "text": args[1]}
	case "submit":
		if len(args) != 4 || args[1] != "--tests" || (args[2] != "pass" && args[2] != "fail" && args[2] != "not_run") {
			return fail(2, "usage: easygo-crew submit --tests pass|fail|not_run <text>")
		}
		payload = map[string]any{"client_id": uuid.NewString(), "kind": "submit", "text": args[3], "claims": map[string]string{"tests": args[2]}}
	case "inbox":
		after := uint64(0)
		if len(args) != 1 {
			if len(args) != 3 || args[1] != "--after" {
				return fail(2, "usage: easygo-crew inbox [--after N]")
			}
			after, err = strconv.ParseUint(args[2], 10, 64)
			if err != nil {
				return fail(2, "invalid inbox cursor")
			}
		}
		path = "/inbox"
		payload = map[string]uint64{"after": after}
	default:
		return fail(2, "unknown crew command")
	}
	body, _ := json.Marshal(payload) // Generated once: every network retry uses the same client_id.
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+path, bytes.NewReader(body))
		if err != nil {
			return fail(2, "invalid crew URL")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		response.Body.Close()
		if readErr != nil {
			continue
		}
		if response.StatusCode != 200 {
			return fail(1, fmt.Sprintf("crew request rejected (HTTP %d)", response.StatusCode))
		}
		if len(raw) > 1<<20 || !json.Valid(raw) {
			return fail(1, "invalid crew response")
		}
		// Only render recognized response fields; never echo arbitrary server text
		// on errors or a credential even if a faulty endpoint returns one.
		if path == "/messages" {
			var receipt struct {
				ID       string `json:"id"`
				Sequence uint64 `json:"sequence"`
			}
			if json.Unmarshal(raw, &receipt) != nil || receipt.Sequence == 0 {
				return fail(1, "invalid crew receipt")
			}
			if _, err := uuid.Parse(receipt.ID); err != nil {
				return fail(1, "invalid crew receipt")
			}
			raw, _ = json.Marshal(receipt)
		}
		fmt.Fprintln(stdout, strings.ReplaceAll(string(raw), token, "[REDACTED]"))
		return 0
	}
	return fail(1, "crew network request failed after retries")
}
