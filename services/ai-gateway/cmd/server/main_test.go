package main

import (
	"bytes"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/ai-gateway/meter"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only-secret" {
			t.Error("credential environment was not resolved")
		}
		if calls.Add(1) > 1 {
			http.Error(w, "private-upstream-secret", 502)
			return
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"command response"}]}]}`)
	}))
	defer upstream.Close()
	config["models"] = map[string]any{"chat": map[string]any{"protocol": "responses", "endpoint": upstream.URL, "model": "fixture", "api_key_env": "GATEWAY_COMMAND_KEY"}}

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
	_, out := rpctest.Raw(t, client, url, `{"jsonrpc":"2.0","id":"generation-success","method":"gateway.generate","params":{"namespace":"test","request":{"model":"chat","messages":[{"role":"user","content":[{"type":"text","text":"private prompt"}]}]}}}`)
	if out.Error != nil || !strings.Contains(fmt.Sprint(out.Result), "command response") {
		t.Fatalf("generation %+v", out)
	}

	_, out = rpctest.Raw(t, client, url, `{"jsonrpc":"2.0","id":"generation-failure","method":"gateway.generate","params":{"namespace":"test","request":{"model":"chat","messages":[{"role":"user","content":[{"type":"text","text":"private prompt"}]}]}}}`)
	if out.Error == nil || out.Error.Data.Code != "upstream_http_error" {
		t.Fatalf("upstream failure %+v", out)
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
	for _, secret := range []string{"test-only-secret", "private prompt", "command response", "private-upstream-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("command logs leaked request/response/credential")
		}
	}
	records := map[string]map[string]map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if e = json.Unmarshal(line, &record); e != nil {
			t.Fatalf("invalid JSON observation: %s", line)
		}
		id, _ := record["request_id"].(string)
		if id == "" {
			continue
		}
		kind, _ := record["kind"].(string)
		if records[id] == nil {
			records[id] = map[string]map[string]any{}
		}
		if records[id][kind] != nil {
			t.Fatalf("duplicate %s record for %s", kind, id)
		}
		records[id][kind] = record
	}
	for _, id := range []string{"generation-success", "generation-failure"} {
		model, audit := records[id]["model"], records[id]["rpc"]
		if model == nil || audit == nil {
			t.Fatalf("missing correlated records for %s: %s", id, output.String())
		}
		if model["model"] != "chat" || model["protocol"] != "responses" || model["latency"].(float64) <= 0 || model["usage"] == nil || model["cost"] == nil || model["has_first_delta"] == nil || model["first_delta"] == nil {
			t.Fatalf("model metadata missing: %+v", model)
		}
		if audit["principal_id"] != "test" || audit["namespace"] != "test" || audit["method"] != "gateway.generate" || audit["duration"].(float64) <= 0 || audit["status"] != float64(200) {
			t.Fatalf("RPC metadata missing: %+v", audit)
		}
		if id == "generation-success" {
			if model["http_status"] != float64(200) || model["error_code"] != nil || audit["error_code"] != nil {
				t.Fatal("incorrect success observation")
			}
		} else {
			if model["http_status"] != float64(502) || model["error_code"] != "upstream_http_error" || audit["error_code"] != "upstream_http_error" {
				t.Fatal("incorrect failure observation")
			}
		}
	}

}
func TestMissingConfig(t *testing.T) {
	if e := run(nil); e == nil {
		t.Fatal("missing config accepted")
	}
}

func TestMeterLogOnlyOnChange(t *testing.T) {
	var lines []map[string]any
	record := meterLog(func(v any) {
		raw, _ := json.Marshal(v)
		var line map[string]any
		json.Unmarshal(raw, &line)
		lines = append(lines, line)
	})
	for _, r := range []meter.FlushReport{{}, {Pending: 2}, {Pending: 2}, {Pending: 2, Error: "wallet transport unavailable"}, {Pending: 2, Error: "wallet transport unavailable"}, {}, {Expired: 3}, {}} {
		record(r)
	}
	if len(lines) != 4 || lines[0]["pending"] != float64(2) || lines[1]["error"] != "wallet transport unavailable" || lines[2]["pending"] != float64(0) || lines[3]["expired"] != float64(3) {
		t.Fatalf("logged %v", lines)
	}
	for _, line := range lines {
		if _, ok := line["time"]; line["kind"] != "meter" || !ok || len(line) > 5 {
			t.Fatalf("meter line %v", line)
		}
	}
}
