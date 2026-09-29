package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type crewAction struct {
	Op        string   `json:"op"`
	Args      []string `json:"args,omitempty"`
	Path      string   `json:"path,omitempty"`
	Text      string   `json:"text,omitempty"`
	ClientID  string   `json:"client_id,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Tests     string   `json:"tests,omitempty"`
	After     uint64   `json:"after,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
	ExitCode  int      `json:"exit_code,omitempty"`
}

func crewOutput(raw []byte) {
	text := strings.ReplaceAll(string(raw), os.Getenv("EASYGO_CREW_TOKEN"), "[REDACTED]")
	line, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": text}})
	fmt.Println(string(line))
}
func crewSuccess() int {
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":0,"output_tokens":0}}`)
	return 0
}
func crewScript(actions []crewAction, input string) int {
	fmt.Println(`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`)
	for _, a := range actions {
		switch a.Op {
		case "crew":
			output, err := exec.Command("/usr/local/bin/easygo-crew", a.Args...).CombinedOutput()
			if err != nil {
				fmt.Fprintln(os.Stderr, "crew command failed")
				return 4
			}
			crewOutput(output)
		case "write":
			if !filepath.IsLocal(a.Path) || a.Path == "." {
				return 2
			}
			if err := os.MkdirAll(filepath.Dir(a.Path), 0700); err != nil {
				return 3
			}
			if err := os.WriteFile(a.Path, []byte(a.Text), 0600); err != nil {
				return 3
			}
		case "echo_input":
			text := input
			if len(text) > 64<<10 {
				end := 64 << 10
				for end > 0 && !utf8.RuneStart(text[end]) {
					end--
				}
				text = text[:end]
			}
			crewOutput([]byte(text))
		case "inbox":
			timeout := a.TimeoutMS
			if timeout == 0 {
				timeout = 5000
			}
			if timeout < 1 || timeout > 60000 {
				return 2
			}
			deadline := time.Now().Add(time.Duration(timeout) * time.Millisecond)
			found := false
			for time.Now().Before(deadline) {
				raw, err := exec.Command("/usr/local/bin/easygo-crew", "inbox", "--after", strconv.FormatUint(a.After, 10)).CombinedOutput()
				if err != nil {
					return 4
				}
				var inbox struct {
					Messages []struct{ Text string }
					Next     uint64
				}
				if json.Unmarshal(raw, &inbox) != nil {
					return 4
				}
				for _, m := range inbox.Messages {
					if a.Text == "" || strings.Contains(m.Text, a.Text) {
						found = true
					}
				}
				if found {
					crewOutput(raw)
					break
				}
				a.After = inbox.Next
				time.Sleep(20 * time.Millisecond)
			}
			if !found {
				fmt.Fprintln(os.Stderr, "crew inbox timeout")
				return 5
			}
		case "duplicate":
			kind := a.Kind
			if kind == "" {
				kind = "report"
			}
			p := map[string]any{"client_id": a.ClientID, "kind": kind, "text": a.Text}
			if kind == "submit" {
				p["claims"] = map[string]string{"tests": a.Tests}
			}
			body, _ := json.Marshal(p)
			client := &http.Client{Timeout: 5 * time.Second}
			var first []byte
			for i := 0; i < 2; i++ {
				req, err := http.NewRequest("POST", os.Getenv("EASYGO_CREW_URL")+"/messages", bytes.NewReader(body))
				if err != nil {
					return 4
				}
				req.Header.Set("Authorization", "Bearer "+os.Getenv("EASYGO_CREW_TOKEN"))
				response, err := client.Do(req)
				if err != nil {
					return 4
				}
				raw, err := io.ReadAll(io.LimitReader(response.Body, 16384))
				response.Body.Close()
				if err != nil || response.StatusCode != 200 {
					return 4
				}
				if i == 0 {
					first = raw
				} else if !bytes.Equal(first, raw) {
					return 4
				}
			}
			crewOutput(first)
		case "silent":
			return crewSuccess()
		case "exit":
			if a.ExitCode == 0 {
				return crewSuccess()
			}
			if a.ExitCode < 1 || a.ExitCode > 125 {
				return 2
			}
			return a.ExitCode
		default:
			return 2
		}
	}
	return crewSuccess()
}
