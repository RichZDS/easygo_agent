// easygo-remote runs the existing Bubble Tea UI against the TS platform only.
package main

import (
	"context"
	"easygo-agent/internal/remotetui"
	"easygo-agent/internal/tui"
	"encoding/json"
	"flag"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"io"
	"os"
	"slices"
	"strings"
)

// personalRPC lists the memory/skills methods that --rpc may call, as
// documented in doc/platform-knowledge.md. The server binds every call to the
// logged-in user's namespace.
var personalRPC = []string{
	"agent.memory.list", "agent.memory.upsert", "agent.memory.delete", "agent.memory.consolidate", "agent.memory.import",
	"agent.skills.list", "agent.skills.get", "agent.skills.upsert", "agent.skills.delete", "agent.skills.import",
}

func checkRPC(method string) error {
	if !slices.Contains(personalRPC, method) {
		return fmt.Errorf("--rpc is limited to memory/skills methods: %s", strings.Join(personalRPC, " "))
	}
	return nil
}

func run() error {
	address := flag.String("url", "http://127.0.0.1:8090", "platform public origin")
	email := flag.String("email", "", "account email")
	passwordEnv := flag.String("password-env", "", "environment variable NAME containing password; otherwise secure prompt")
	session := flag.String("session", "", "session ID to reconnect; empty creates a session")
	runtime := flag.String("runtime", "", "configured workshop runtime")
	list := flag.Bool("sessions", false, "list recent sessions and exit")
	catalog := flag.Bool("runtimes", false, "show workshop runtime catalog and exit")
	rpc := flag.String("rpc", "", "memory/skills method to call, e.g. agent.memory.list")
	flag.Parse()
	if *email == "" {
		return fmt.Errorf("--email required")
	}
	var password []byte
	if *passwordEnv != "" {
		password = []byte(os.Getenv(*passwordEnv))
		os.Unsetenv(*passwordEnv)
	} else {
		fmt.Fprint(os.Stderr, "Password: ")
		var err error
		password, err = term.ReadPassword(os.Stdin.Fd())
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
	}
	if len(password) == 0 {
		return fmt.Errorf("empty password")
	}
	c, err := remotetui.NewClient(*address)
	if err != nil {
		return err
	}
	ctx := context.Background()
	err = c.Login(ctx, *email, string(password))
	clear(password)
	if err != nil {
		return err
	}
	defer c.Logout(ctx)
	print := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *list {
		s, err := c.Sessions(ctx)
		if err != nil {
			return err
		}
		return print(s)
	}
	if *catalog {
		var out any
		if err := c.RPC(ctx, "agent.workshop.catalog", nil, &out); err != nil {
			return err
		}
		return print(out)
	}
	if *rpc != "" {
		if err := checkRPC(*rpc); err != nil {
			return err
		}
		params := map[string]any{}
		if term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprintln(os.Stderr, "Enter one JSON params object, then EOF:")
		}
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 1048577))
		if err := decoder.Decode(&params); err != nil {
			return err
		}
		var out any
		if err := c.RPC(ctx, *rpc, params, &out); err != nil {
			return err
		}
		return print(out)
	}
	if *session == "" {
		s, err := c.CreateSession(ctx)
		if err != nil {
			return err
		}
		*session = s.ID
	}
	fmt.Fprintf(os.Stderr, "Session: %s (use --session to reconnect)\n", *session)
	q := remotetui.NewQueue(c, *runtime)
	defer q.Close()
	lines := []string{}
	var after int64
	transcriptBytes := 0
historyLoop:
	for pages := 0; pages < 10; pages++ {
		h, err := c.History(ctx, *session, after)
		if err != nil {
			return err
		}
		if pages == 0 && h.RunsTruncated {
			lines = append(lines, "Queue preview is limited to 100 runs; remaining runs stay on the server.")
		}
		for _, m := range h.Messages {
			if m.Role == "system" {
				continue
			}
			for _, b := range m.Content {
				if b.Type == "text" {
					transcriptBytes += len(b.Text)
					if transcriptBytes > 2<<20 {
						lines = append(lines, "History preview reached 2 MiB; full history remains on the server.")
						break historyLoop
					}
					lines = append(lines, m.Role+": "+b.Text)
				} else if b.Type == "tool_call" || b.Type == "tool_result" {
					lines = append(lines, "tool: "+b.Name+" "+b.Type)
				}
			}
		}
		if h.NextAfter == nil {
			break
		}
		if *h.NextAfter <= after {
			return fmt.Errorf("invalid history cursor")
		}
		after = *h.NextAfter
		if pages == 9 {
			lines = append(lines, "History preview reached 10 pages; full history remains on the server.")
		}
	}
	model := tui.NewQueue(q, *email, *session)
	defer model.Close()
	model.RestoreLines(lines)
	_, err = tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
