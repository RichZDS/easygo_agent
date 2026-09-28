package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/rpc/rpctest"
	"easygo-agent/services/workshop/workshop"
)

type artifactSizeRunner struct{}

func (artifactSizeRunner) Run(ctx context.Context, in workshop.Invocation, emit func(workshop.Event) error) (workshop.Result, error) {
	size := 8 << 20
	if in.Input == "over-limit" {
		size++
	}
	err := os.WriteFile(filepath.Join(in.Workspace, "file.bin"), bytes.Repeat([]byte{'x'}, size), 0600)
	return workshop.Result{SessionID: "11111111-1111-4111-8111-111111111111"}, err
}

func TestArtifactDownloadLimitOverMTLS(t *testing.T) {
	service, err := workshop.New(workshop.Config{Root: t.TempDir(), Concurrency: 1, QueueCapacity: 1, Workflows: []workshop.Workflow{{Name: "file", Version: "1", Instructions: "offline fixture", Engine: "codex", Model: "fixture", Policy: "workspace-write", TimeoutSeconds: 10, Artifacts: []string{"file.bin"}}}}, artifactSizeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	pki := rpctest.NewPKI(t)
	identity := pki.Issue("workshop", false)
	caller := pki.Issue("web", false)
	server, err := rpc.NewServer(rpc.ServerConfig{Listen: "127.0.0.1:0", TLS: identity, Authorization: []rpc.Authorization{{ID: "web", CertFile: caller.CertFile, Methods: []string{"workshop.artifact"}, Namespaces: []string{"owner"}}}}, Methods(service), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ts := rpctest.Start(t, server)
	client := rpctest.Client(t, caller, identity.CertFile)
	for _, mode := range []string{"limit", "over-limit"} {
		task, err := service.Submit(workshop.SubmitRequest{Namespace: "owner", Workflow: "file", Input: mode})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for task.Status != workshop.Succeeded && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
			task, err = service.Get("owner", task.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if task.Status != workshop.Succeeded {
			t.Fatal("artifact fixture did not complete", task.Status)
		}
		request := fmt.Sprintf(`{"jsonrpc":"2.0","id":"download","method":"workshop.artifact","params":{"namespace":"owner","task_id":%q,"path":"file.bin"}}`, task.ID)
		response, err := client.Post(ts.URL+"/rpc", "application/json", bytes.NewBufferString(request))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		response.Body.Close()
		if err != nil || len(raw) > 16<<20 {
			t.Fatal("download exceeds upstream envelope limit", len(raw), err)
		}
		var envelope struct {
			Result *workshop.ArtifactDownload `json:"result"`
			Error  *rpc.Error                 `json:"error"`
		}
		if err = json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if mode == "over-limit" {
			if envelope.Result != nil || envelope.Error == nil || envelope.Error.Code != -32013 || envelope.Error.Data.Code != "artifact_too_large" {
				t.Fatal("oversized artifact did not fail explicitly")
			}
			continue
		}
		if response.StatusCode != 200 || envelope.Error != nil || envelope.Result == nil {
			t.Fatal("valid bounded download failed", response.StatusCode, envelope.Error)
		}
		data, err := base64.StdEncoding.DecodeString(envelope.Result.DataBase64)
		if err != nil || len(data) != 8<<20 || !bytes.Equal(data, bytes.Repeat([]byte{'x'}, 8<<20)) {
			t.Fatal("transport changed artifact bytes")
		}
		t.Logf("8 MiB artifact JSON envelope: %d bytes (under 16 MiB)", len(raw))
	}
}
