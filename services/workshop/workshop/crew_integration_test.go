package workshop

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"easygo-agent/rpc"
	"github.com/google/uuid"
)

func TestCrewDockerIntegration(t *testing.T) {
	endpoint, image := os.Getenv("EASYGO_DOCKER_TEST_ENDPOINT"), os.Getenv("EASYGO_DOCKER_TEST_IMAGE")
	if endpoint == "" || image == "" {
		t.Skip("requires explicit dedicated Docker endpoint and fixture image")
	}
	root, err := os.MkdirTemp("/tmp", "crew-proof-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	gateway := nativeRelayFixture(t, func(context.Context, json.RawMessage, *rpc.Stream) (any, *rpc.Error) { return nil, nil })
	cfg := Config{Root: root, Concurrency: 1, QueueCapacity: 1, ModelGateway: gateway, Engines: map[string]EngineConfig{"codex": {}}, RuntimeProfiles: map[string]RuntimeProfile{"fixture": {Engine: "codex", Protocol: "responses", GatewayModel: "fixture"}}, Workflows: []Workflow{{Name: "crew", Version: "1", Runtime: "fixture", Policy: "workspace-write", Instructions: "offline", TimeoutSeconds: 30, Artifacts: []string{"artifact.txt"}}}, Sandbox: SandboxConfig{Mode: "docker", DockerBinary: os.Getenv("EASYGO_DOCKER_TEST_BINARY"), Endpoint: endpoint, Image: image, Owner: "crew-proof-" + uuid.NewString(), HostRoot: root}}
	s, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input := `{"mode":"crew","script":[{"op":"crew","args":["report","started"]},{"op":"crew","args":["ask","ready for note"]},{"op":"duplicate","client_id":"dedup","kind":"report","text":"once"},{"op":"inbox","text":"foreman reply","timeout_ms":15000},{"op":"write","path":"artifact.txt","text":"done"},{"op":"crew","args":["submit","--tests","pass","ready"]}]}`
	task, err := s.Submit(SubmitRequest{Namespace: "task-namespace", Workflow: "crew", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	asked := false
	for time.Now().Before(deadline) {
		events, err := s.Events(task.Namespace, task.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Message != nil && event.Message.Kind == "ask" {
				asked = true
			}
		}
		if asked {
			break
		}
		current, err := s.Get(task.Namespace, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal(current.Status) {
			t.Fatal("worker stopped before asking", current.Status, current.Runs[0].Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !asked {
		t.Fatal("container did not send ask")
	}
	receipt, err := s.Message(task.Namespace, task.ID, "foreman reply", "reply-1")
	if err != nil {
		t.Fatal(err)
	}
	task = waitTask(t, s, task.Namespace, task.ID, func(task *Task) bool { return terminal(task.Status) })
	if task.Status != Succeeded || task.Runs[0].Outcome != "submitted" || len(task.Runs[0].Artifacts) != 1 {
		t.Fatal(task.Status, task.Runs[0])
	}
	if !strings.Contains(task.Runs[0].Text, "foreman reply") {
		t.Fatal("inbox response not seen inside container")
	}
	events, err := s.Events(task.Namespace, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	read := false
	for _, event := range events {
		if event.Message != nil && event.Message.Direction == "from_worker" {
			kinds = append(kinds, event.Message.Kind)
		}
		if event.Read != nil {
			for _, id := range event.Read.MessageIDs {
				if id == receipt.ID {
					read = true
				}
			}
		}
		if event.Message != nil || event.Read != nil {
			raw, _ := json.Marshal(event)
			t.Log(string(raw))
		}
	}
	if strings.Join(kinds, ",") != "report,ask,report,submit" || !read {
		t.Fatal("wrong message order/read proof", kinds, read)
	}
	docker := s.runner.(*DockerRunner)
	ids, err := docker.output(context.Background(), "ps", "-aq", "--filter", "label="+ownerLabel+"="+cfg.Sandbox.Owner)
	if err != nil || ids != "" {
		t.Fatal("task container leaked", err)
	}
	t.Log("real container: report/ask/submit, duplicate receipt, live foreman inbox, submitted outcome, artifact, cleanup verified")
}
