package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"easygo-agent/rpc/rpctest"
)

func TestCommandProcess(t *testing.T) {
	if os.Getenv("RPC_COMMAND_HELPER") == "1" {
		for i, a := range os.Args {
			if a == "--" {
				if e := run(os.Args[i+1:]); e != nil {
					fmt.Fprintln(os.Stderr, e)
					os.Exit(1)
				}
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	p := rpctest.NewPKI(t)
	server := p.Issue("server", false)
	caller := p.Issue("caller", false)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := listener.Addr().String()
	listener.Close()
	config := map[string]any{"listen": addr, "tls": server, "authorization": []any{map[string]any{"id": "test", "cert_file": caller.CertFile, "methods": []string{"*"}, "namespaces": []string{"test"}}}}
	config["workshop"] = map[string]any{"root": filepath.Join(t.TempDir(), "data"), "concurrency": 1, "queue_capacity": 1, "engines": map[string]any{}, "workflows": []any{}}

	raw, e := json.Marshal(config)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCommandProcess$", "--", "--config", path)
	cmd.Env = append(os.Environ(), "RPC_COMMAND_HELPER=1", "GATEWAY_COMMAND_KEY=test-only-secret")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	defer func() {
		if !exited {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	client := rpctest.Client(t, caller, server.CertFile)
	url := "https://" + addr
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, e := client.Get(url + "/healthz")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("RPC command never became healthy")
	}
	_, out := rpctest.Call(t, client, url, "workshop.list", `{"namespace":"test"}`)
	if out.Error != nil {
		t.Fatalf("workshop list %+v", out)
	}

	_, out = rpctest.Raw(t, client, url, `{"jsonrpc":"2.0","id":"workshop-failure","method":"workshop.get","params":{"namespace":"test","task_id":"private-task-id"}}`)
	if out.Error == nil || out.Error.Code != -32004 {
		t.Fatalf("missing task %+v", out)
	}
	if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		exited = true
		if e != nil {
			t.Fatalf("shutdown: %v %s", e, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command failed graceful shutdown")
	}
	for _, secret := range []string{"test-only-secret", "private prompt", "command response", "private-task-id"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("command logs leaked request/response/credential")
		}
	}
	seen := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if e = json.Unmarshal(line, &record); e != nil {
			t.Fatalf("invalid audit JSON: %s", line)
		}
		id, _ := record["request_id"].(string)
		if id == "" {
			continue
		}
		seen[id] = true
		if record["kind"] != "rpc" || record["principal_id"] != "test" || record["namespace"] != "test" || record["duration"].(float64) <= 0 || record["status"] != float64(200) {
			t.Fatalf("invalid audit: %+v", record)
		}
		if id == "test-id" {
			if record["method"] != "workshop.list" || record["error_code"] != nil {
				t.Fatal("incorrect success audit")
			}
		} else if id == "workshop-failure" {
			if record["method"] != "workshop.get" || record["error_code"] != "not_found" {
				t.Fatal("incorrect failure audit")
			}
		} else {
			t.Fatalf("unexpected RPC ID %s", id)
		}
	}
	if !seen["test-id"] || !seen["workshop-failure"] {
		t.Fatalf("missing audit records: %s", output.String())
	}

}
func TestMissingConfig(t *testing.T) {
	if e := run(nil); e == nil {
		t.Fatal("missing config accepted")
	}
}
