package main

import (
	"bytes"
	"easygo-agent/pkg/gateway"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestGatewayCommandProcess(t *testing.T) {
	if os.Getenv("GATEWAY_COMMAND_HELPER") == "1" {
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-only-key" {
			t.Error("command did not resolve key")
		}
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"command works"}]}]}`)
	}))
	defer upstream.Close()
	f := filepath.Join(t.TempDir(), "config.json")
	raw := fmt.Sprintf(`{"models":{"local":{"protocol":"responses","endpoint":%q,"model":"fixture","api_key_env":"GATEWAY_COMMAND_KEY"}}}`, upstream.URL)
	if e := os.WriteFile(f, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := listener.Addr().String()
	listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGatewayCommandProcess$", "--", "-config", f, "-listen", addr)
	cmd.Env = append(os.Environ(), "GATEWAY_COMMAND_HELPER=1", "GATEWAY_COMMAND_KEY=test-only-key")
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
			cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, e := client.Get("http://" + addr + "/healthz")
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
		t.Fatal("command never became healthy")
	}
	resp, e := client.Post("http://"+addr+"/v1/generate", "application/json", strings.NewReader(`{"model":"local","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "command works") {
		t.Fatalf("bad generation: %s", body)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("generation status=%d", resp.StatusCode)
	}
	if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-done:
		exited = true
		if e != nil {
			t.Fatalf("shutdown: %v; output=%s", e, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not shut down")
	}
	var observation gateway.Observation
	if e := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &observation); e != nil {
		t.Fatalf("missing JSON observation: %v output=%s", e, output.String())
	}
	if observation.Model != "local" || observation.Protocol != "responses" || observation.HTTPStatus != 200 || observation.RequestID == "" || observation.Latency <= 0 {
		t.Fatalf("bad observation: %+v", observation)
	}
	if strings.Contains(output.String(), "test-only-key") || strings.Contains(output.String(), "hello") || strings.Contains(output.String(), "command works") {
		t.Fatal("stderr observation leaked request/response or key")
	}
}
func TestMissingConfiguration(t *testing.T) {
	if e := run(nil); e == nil {
		t.Fatal("missing config accepted")
	}
}

func TestListenRequiresBearer(t *testing.T) {
	for _, tc := range []struct {
		address, token string
		ok             bool
	}{{"127.0.0.1:8090", "", true}, {"[::1]:8090", "", true}, {"localhost:8090", "", true}, {":8090", "", false}, {"0.0.0.0:8090", "", false}, {"[::]:8090", "", false}, {"0.0.0.0:8090", "token", true}, {"bad", "token", false}} {
		if e := validateListen(tc.address, tc.token); (e == nil) != tc.ok {
			t.Errorf("listen=%s error=%v", tc.address, e)
		}
	}
	f := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(f, []byte(`{"models":{"local":{"protocol":"responses","endpoint":"http://localhost","model":"fixture"}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"-config", f, "-listen", "0.0.0.0:0"}); e == nil || !strings.Contains(e.Error(), "requires bearer_token_env") {
		t.Fatalf("unprotected external listen: %v", e)
	}
}
func TestJSONObserverConcurrent(t *testing.T) {
	var b bytes.Buffer
	observe := jsonObserver(&b)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			observe(gateway.Observation{RequestID: "id", Model: "alias", Protocol: "responses", HTTPStatus: 200})
		}()
	}
	wg.Wait()
	lines := bytes.Split(bytes.TrimSpace(b.Bytes()), []byte("\n"))
	if len(lines) != 16 {
		t.Fatalf("records=%d", len(lines))
	}
	for _, line := range lines {
		var o gateway.Observation
		if json.Unmarshal(line, &o) != nil || o.RequestID != "id" {
			t.Fatalf("invalid JSON record: %s", line)
		}
	}
}
