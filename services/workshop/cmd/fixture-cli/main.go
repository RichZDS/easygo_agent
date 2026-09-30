// fixture-cli is an adversarial offline integration fixture, never a production runtime.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type request struct {
	Script          []crewAction `json:"script"`
	Mode            string       `json:"mode"`
	Bytes           int64        `json:"bytes"`
	Files           int          `json:"files"`
	Chunk           int          `json:"chunk"`
	Sparse          bool         `json:"sparse"`
	DelayMS         int          `json:"delay_ms"`
	DurationSeconds int          `json:"duration_seconds"`
	HostSentinel    string       `json:"host_sentinel"`
	Sibling         string       `json:"sibling"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--child" {
		time.Sleep(time.Hour)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	_, input, _ := strings.Cut(string(raw), "User input:\n")
	var req request
	if json.NewDecoder(strings.NewReader(input)).Decode(&req) != nil {
		fmt.Fprintln(os.Stderr, "fixture input invalid")
		os.Exit(2)
	}
	if req.Mode == "crew" {
		os.Exit(crewScript(req.Script, input))
	}
	if req.Mode == "fill" {
		os.Exit(fill(req))
	}
	if req.Mode == "sleep" {
		child := exec.Command(os.Args[0], "--child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		os.WriteFile("ready", []byte("child-running"), 0600)
		time.Sleep(time.Hour)
		return
	}
	if req.Mode == "long" {
		if req.DurationSeconds < 1 || req.DurationSeconds > 300 {
			os.Exit(2)
		}
		os.WriteFile("ready", []byte("long-running"), 0600)
		fmt.Println(`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`)
		started := time.Now()
		rounds := 0
		digest := sha256.Sum256([]byte("bounded workload"))
		for time.Since(started) < time.Duration(req.DurationSeconds)*time.Second {
			for i := 0; i < 250000; i++ {
				digest = sha256.Sum256(digest[:])
				rounds++
			}
			data, _ := json.Marshal(map[string]any{"elapsed_ms": time.Since(started).Milliseconds(), "rounds": rounds})
			os.WriteFile("progress.json", data, 0600)
			time.Sleep(time.Second)
		}
	}
	checks := map[string]bool{}
	checks["uid_1000"] = os.Getuid() == 1000
	for name, path := range map[string]string{"host_sentinel_denied": req.HostSentinel, "sibling_denied": req.Sibling, "docker_socket_denied": "/var/run/docker.sock", "tls_private_key_denied": "/run/easygo/tls/key.pem"} {
		_, err := os.ReadFile(path)
		checks[name] = err != nil
	}
	checks["no_service_credentials"] = true
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.Contains(name, "SECRET") || strings.Contains(name, "PRIVATE_KEY") || strings.HasPrefix(name, "CLOUDFLARE_") {
			checks["no_service_credentials"] = false
		}
	}
	var fs syscall.Statfs_t
	checks["rootfs_readonly"] = syscall.Statfs("/", &fs) == nil && fs.Flags&1 != 0
	interfaces, interfaceErr := net.Interfaces()
	checks["network_only_loopback"] = interfaceErr == nil && len(interfaces) == 1 && interfaces[0].Name == "lo"
	checks["rootfs_write_denied"] = os.WriteFile("/etc/fixture-write", []byte("no"), 0600) != nil
	for name, address := range map[string]string{"host_network_denied": "172.17.0.1:80", "external_network_denied": "1.1.1.1:80"} {
		conn, err := net.DialTimeout("tcp", address, 300*time.Millisecond)
		checks[name] = err != nil
		if conn != nil {
			conn.Close()
		}
	}
	status, _ := os.ReadFile("/proc/self/status")
	checks["no_caps"] = strings.Contains(string(status), "CapEff:\t0000000000000000")
	checks["no_new_privs"] = strings.Contains(string(status), "NoNewPrivs:\t1")
	// Read actual limits from this process's cgroup (v2 or v1).
	limits := map[string]string{}
	for name, paths := range map[string][]string{"memory": {"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}, "pids": {"/sys/fs/cgroup/pids.max", "/sys/fs/cgroup/pids/pids.max"}, "cpu": {"/sys/fs/cgroup/cpu.max", "/sys/fs/cgroup/cpu/cpu.cfs_quota_us"}} {
		for _, path := range paths {
			if b, e := os.ReadFile(path); e == nil {
				limits[name] = strings.TrimSpace(string(b))
				break
			}
		}
	}
	home := filepath.Join(os.Getenv("HOME"), "fixture-persisted")
	if req.Mode == "readonly" {
		checks["workspace_write_denied"] = os.WriteFile("forbidden-artifact.txt", []byte("no"), 0600) != nil
		var workspaceFS syscall.Statfs_t
		checks["workspace_readonly"] = syscall.Statfs("/workspace", &workspaceFS) == nil && workspaceFS.Flags&1 != 0
		checks["home_writable"] = os.WriteFile(home, []byte("own-home"), 0600) == nil
	} else if req.Mode == "resume" {
		b, e := os.ReadFile(home)
		checks["home_persisted"] = e == nil && string(b) == "own-home"
		b, e = os.ReadFile("artifact.txt")
		checks["artifact_persisted"] = e == nil && string(b) == "own-artifact"
	} else {
		checks["home_written"] = os.WriteFile(home, []byte("own-home"), 0600) == nil
		checks["artifact_written"] = os.WriteFile("artifact.txt", []byte("own-artifact"), 0600) == nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 127.0.0.1:18080 is workshop runtimeProxyAddr (served by task-shim).
	post, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:18080/v1/responses", strings.NewReader(`{"model":"child-spoof","input":"offline fixture"}`))
	post.Header.Set("Authorization", "Bearer "+os.Getenv("EASYGO_RUNTIME_API_KEY"))
	resp, err := http.DefaultClient.Do(post)
	checks["model_relay"] = err == nil && resp.StatusCode == 200
	if resp != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	proof, _ := json.Marshal(map[string]any{"checks": checks, "limits": limits})
	proofPath := "isolation-proof.json"
	if req.Mode == "readonly" {
		proofPath = filepath.Join(os.Getenv("HOME"), "isolation-proof.json")
	}
	os.WriteFile(proofPath, proof, 0600)
	for name, ok := range checks {
		if !ok {
			fmt.Fprintln(os.Stderr, "failed isolation check:", name)
			os.Exit(4)
		}
	}
	fmt.Println(`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`)
	msg, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": "offline isolation fixture passed"}})
	fmt.Println(string(msg))
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":3}}`)
}

// A bounded offline writer. bytes is per-file logical size; the entire target is
// capped at 256 MiB and tests use at most 128 MiB. Sparse writes allocate only one block.
func fill(req request) int {
	if req.Bytes < 1 || req.Files < 1 || req.Files > 10000 || req.Bytes > 256<<20 || req.Bytes*int64(req.Files) > 256<<20 || req.Chunk < 1 || req.Chunk > 4<<20 || req.DelayMS < 0 || req.DelayMS > 1000 {
		return 2
	}
	fmt.Println(`{"type":"thread.started","thread_id":"11111111-1111-4111-8111-111111111111"}`)
	if err := os.MkdirAll("fill", 0700); err != nil {
		return 2
	}
	os.WriteFile("artifact.txt", []byte("must-not-publish-on-quota-failure"), 0600)
	started := time.Now()
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		return 2
	}
	meta, _ := json.Marshal(map[string]any{"started_unix_nano": started.UnixNano(), "fsize_soft": limit.Cur, "fsize_hard": limit.Max})
	os.WriteFile("fill-meta.json", meta, 0600)
	data := make([]byte, req.Chunk)
	var writtenTotal int64
	marked := false
	mark := func() {
		if !marked && (writtenTotal >= int64(limit.Cur) || req.Bytes == 1 && writtenTotal >= 1000) {
			marked = true
			b, _ := json.Marshal(map[string]int64{"unix_nano": time.Now().UnixNano()})
			os.WriteFile("fill-crossed.json", b, 0600)
		}
	}
	for i := 0; i < req.Files; i++ {
		f, err := os.Create(fmt.Sprintf("fill/file-%06d", i))
		if err != nil {
			return 2
		}
		if req.Sparse {
			_, err = f.Seek(req.Bytes-1, 0)
			if err == nil {
				_, err = f.Write([]byte{1})
			}
		} else {
			for left := req.Bytes; left > 0 && err == nil; {
				n := int64(len(data))
				if n > left {
					n = left
				}
				var written int
				written, err = f.Write(data[:n])
				left -= int64(written)
				writtenTotal += int64(written)
				mark()
				if req.DelayMS > 0 {
					time.Sleep(time.Duration(req.DelayMS) * time.Millisecond)
				}
			}
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "fill_write_error: %v\n", err)
			return 9
		}
	}
	fmt.Println(`{"type":"item.completed","item":{"type":"agent_message","text":"fill complete"}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":0,"output_tokens":0}}`)
	return 0
}
