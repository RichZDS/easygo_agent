package sandboxintegration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	integrationURLEnvironment = "EASYGO_SANDBOX_INTEGRATION_URL"
	tokenEnvironment          = "SANDBOX_CONTROLLER_TOKEN"
	namespaceEnvironment      = "EASYGO_SANDBOX_NAMESPACE"
	dockerBinaryEnvironment   = "DOCKER_BIN"
	stressEnvironment         = "EASYGO_SANDBOX_STRESS"
	applicationLabel          = "io.easygo.sandbox.application_id"
	namespaceLabel            = "io.easygo.sandbox.namespace"
	managedLabel              = "io.easygo.sandbox.managed"
)

type apiClient struct {
	baseURL   string
	token     string
	sessionID string
	runID     string
	http      *http.Client
}

type applicationEnvelope struct {
	Application struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"application"`
}

type execResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *apiError) Error() string {
	return fmt.Sprintf("controller returned %d %s: %s", err.StatusCode, err.Code, err.Message)
}

type readFileResult struct {
	Content string `json:"content"`
	EOF     bool   `json:"eof"`
}

type dockerInspect struct {
	Config struct {
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
		User   string            `json:"User"`
	} `json:"Config"`
	HostConfig struct {
		Binds          []string            `json:"Binds"`
		CapAdd         []string            `json:"CapAdd"`
		CapDrop        []string            `json:"CapDrop"`
		IpcMode        string              `json:"IpcMode"`
		Memory         int64               `json:"Memory"`
		MemorySwap     int64               `json:"MemorySwap"`
		NanoCPUs       int64               `json:"NanoCpus"`
		NetworkMode    string              `json:"NetworkMode"`
		PidMode        string              `json:"PidMode"`
		PidsLimit      *int64              `json:"PidsLimit"`
		PortBindings   map[string][]any    `json:"PortBindings"`
		Privileged     bool                `json:"Privileged"`
		ReadonlyRootfs bool                `json:"ReadonlyRootfs"`
		SecurityOpt    []string            `json:"SecurityOpt"`
		ShmSize        int64               `json:"ShmSize"`
		Tmpfs          map[string]string   `json:"Tmpfs"`
		Devices        []map[string]string `json:"Devices"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Ports map[string][]any `json:"Ports"`
	} `json:"NetworkSettings"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

func TestDockerSandboxContract(t *testing.T) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(integrationURLEnvironment)), "/")
	if baseURL == "" {
		t.Skipf("set %s to run Docker sandbox integration tests", integrationURLEnvironment)
	}
	token := strings.TrimSpace(os.Getenv(tokenEnvironment))
	if token == "" {
		t.Fatalf("%s is required when %s is set", tokenEnvironment, integrationURLEnvironment)
	}
	namespace := envOrDefault(namespaceEnvironment, "easygo-agent-it")
	dockerBinary := envOrDefault(dockerBinaryEnvironment, "docker")
	docker := dockerCLI{binary: dockerBinary}
	if output, err := docker.run(t.Context(), "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Fatalf("Docker daemon is required; the test never starts it: %v\n%s", err, output)
	}

	client := &apiClient{
		baseURL:   baseURL,
		token:     token,
		sessionID: "sandbox-it-" + randomHex(t, 12),
		runID:     "run-" + randomHex(t, 12),
		http:      &http.Client{Timeout: 11 * time.Minute},
	}

	applicationID := client.apply(t)
	destroyed := false
	t.Cleanup(func() {
		if !destroyed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := client.request(cleanupCtx, http.MethodDelete, applicationPath(applicationID), nil, nil); err != nil {
				t.Errorf("cleanup application: %v", err)
			}
		}
		if !waitForNoResources(docker, applicationID, 15*time.Second) {
			t.Errorf("managed resources remain after API cleanup; forcing removal for application %s", applicationID)
			if output, err := docker.forceRemoveApplication(context.Background(), applicationID); err != nil {
				t.Errorf("force cleanup application: %v\n%s", err, output)
			}
			if !waitForNoResources(docker, applicationID, 5*time.Second) {
				t.Errorf("managed resources still remain after forced cleanup: containers=%v volumes=%v", docker.resources(context.Background(), "container", applicationID), docker.resources(context.Background(), "volume", applicationID))
			}
		}
	})

	client.post(t, applicationPath(applicationID)+"/create", map[string]any{}, nil)
	containerID := oneResource(t, docker, "container", applicationID)
	assertContainerSecurity(t, docker.inspect(t, containerID), namespace, applicationID)

	for name, command := range map[string]string{
		"non-root": "test \"$(id -u)\" = 1000 && test \"$(id -g)\" = 1000",
		"python":   "python3 -c 'print(\"easygo-python-ok\")'",
		"go":       "mkdir -p .integration/go && printf 'module integration\\n\\ngo 1.25\\n' > .integration/go/go.mod && printf 'package main\\nimport \"fmt\"\\nfunc main(){fmt.Println(\"easygo-go-ok\")}\\n' > .integration/go/main.go && (cd .integration/go && go build -o app . && ./app)",
		"node":     "node -e 'console.log(\"easygo-node-ok\")'",
		"cpp":      "mkdir -p .integration/cpp && printf '#include <iostream>\\nint main(){std::cout << \"easygo-cpp-ok\\\\n\";}\\n' > .integration/cpp/main.cpp && c++ -O0 -o .integration/cpp/app .integration/cpp/main.cpp && .integration/cpp/app",
	} {
		name, command := name, command
		t.Run(name, func(t *testing.T) {
			result := client.exec(t, applicationID, command)
			if result.ExitCode != 0 {
				t.Fatalf("exit=%d stderr=%q stdout=%q", result.ExitCode, result.Stderr, result.Stdout)
			}
		})
	}

	t.Run("read-only root filesystem", func(t *testing.T) {
		result := client.exec(t, applicationID, "touch /easygo-sandbox-must-not-exist")
		if result.ExitCode == 0 {
			t.Fatal("unexpectedly wrote to the container root filesystem")
		}
	})

	t.Run("network disabled", func(t *testing.T) {
		result := client.exec(t, applicationID, "python3 -c 'import socket; s=socket.socket(); s.settimeout(1); s.connect((\"1.1.1.1\", 53))'")
		if result.ExitCode == 0 {
			t.Fatal("unexpectedly opened an external network connection")
		}
	})

	t.Run("file tools reject symbolic-link escape", func(t *testing.T) {
		result := client.exec(t, applicationID, "ln -s /etc/passwd /workspace/escape-read && ln -s /tmp /workspace/escape-write")
		if result.ExitCode != 0 {
			t.Fatalf("prepare symbolic links: exit=%d stderr=%q", result.ExitCode, result.Stderr)
		}
		for name, testCase := range map[string]struct {
			endpoint string
			body     map[string]any
		}{
			"read": {
				endpoint: applicationPath(applicationID) + "/files/read",
				body:     map[string]any{"path": "/workspace/escape-read"},
			},
			"write": {
				endpoint: applicationPath(applicationID) + "/files/write",
				body:     map[string]any{"path": "/workspace/escape-write/escaped.txt", "content": "must-not-escape"},
			},
		} {
			name, testCase := name, testCase
			t.Run(name, func(t *testing.T) {
				err := client.request(t.Context(), http.MethodPost, testCase.endpoint, testCase.body, nil)
				var controllerError *apiError
				if !errors.As(err, &controllerError) || controllerError.Code != "invalid_request" {
					t.Fatalf("symbolic-link escape was not rejected as invalid_request: %v", err)
				}
			})
		}
	})

	t.Run("file writes reject directory destinations", func(t *testing.T) {
		result := client.exec(t, applicationID, "mkdir /workspace/existing-directory")
		if result.ExitCode != 0 {
			t.Fatalf("prepare destination directory: exit=%d stderr=%q", result.ExitCode, result.Stderr)
		}
		err := client.request(t.Context(), http.MethodPost, applicationPath(applicationID)+"/files/write", map[string]any{
			"path": "/workspace/existing-directory", "content": "must-not-be-written",
		}, nil)
		var controllerError *apiError
		if !errors.As(err, &controllerError) || controllerError.Code != "invalid_request" {
			t.Fatalf("directory destination was not rejected as invalid_request: %v", err)
		}
		result = client.exec(t, applicationID, "test -d /workspace/existing-directory && test -z \"$(find /workspace/existing-directory -mindepth 1 -maxdepth 1 -print -quit)\"")
		if result.ExitCode != 0 {
			t.Fatalf("failed directory write left content behind: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
		}
	})

	t.Run("file writes treat glob characters literally", func(t *testing.T) {
		const (
			requestedPath = "/workspace/glob*?[x]/file*?[y].txt"
			content       = "literal-glob-path"
		)
		result := client.exec(t, applicationID, "mkdir /workspace/glob-match-ax")
		if result.ExitCode != 0 {
			t.Fatalf("prepare glob decoy directory: exit=%d stderr=%q", result.ExitCode, result.Stderr)
		}
		client.post(t, applicationPath(applicationID)+"/files/write", map[string]any{
			"path": requestedPath, "content": content,
		}, nil)
		var readResult readFileResult
		client.post(t, applicationPath(applicationID)+"/files/read", map[string]any{
			"path": requestedPath,
		}, &readResult)
		if readResult.Content != content || !readResult.EOF {
			t.Fatalf("unexpected literal glob file result: %+v", readResult)
		}
		result = client.exec(t, applicationID, "test ! -e '/workspace/glob-match-ax/file*?[y].txt'")
		if result.ExitCode != 0 {
			t.Fatalf("glob-expanded path was written instead of the requested literal path: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
		}
	})

	t.Run("background processes cleaned", func(t *testing.T) {
		result := client.exec(t, applicationID, "sleep 300 >/dev/null 2>&1 &")
		if result.ExitCode != 0 {
			t.Fatalf("start background process: exit=%d stderr=%q", result.ExitCode, result.Stderr)
		}
		result = client.exec(t, applicationID, "! pgrep -u 1000 sleep")
		if result.ExitCode != 0 {
			t.Fatalf("a uid 1000 background process survived: stdout=%q stderr=%q", result.Stdout, result.Stderr)
		}
	})

	t.Run("timeout stops container and preserves workspace", func(t *testing.T) {
		client.post(t, applicationPath(applicationID)+"/files/write", map[string]any{
			"path": "/workspace/timeout-marker.txt", "content": "timeout-preserved",
		}, nil)
		err := client.request(t.Context(), http.MethodPost, applicationPath(applicationID)+"/exec", map[string]any{
			"command": "sleep 30", "cwd": "/workspace", "timeout_seconds": 1,
		}, nil)
		var controllerError *apiError
		if !errors.As(err, &controllerError) || controllerError.Code != "command_timeout" {
			t.Fatalf("timeout did not return command_timeout: %v", err)
		}
		if docker.inspect(t, containerID).State.Running {
			t.Fatal("timed-out container is still running")
		}
		result := client.exec(t, applicationID, "test \"$(cat /workspace/timeout-marker.txt)\" = timeout-preserved")
		if result.ExitCode != 0 {
			t.Fatalf("workspace was not preserved after timeout: exit=%d stderr=%q", result.ExitCode, result.Stderr)
		}
	})

	t.Run("request cancellation stops container and preserves workspace", func(t *testing.T) {
		requestContext, cancelRequest := context.WithCancel(t.Context())
		requestDone := make(chan error, 1)
		go func() {
			requestDone <- client.request(requestContext, http.MethodPost, applicationPath(applicationID)+"/exec", map[string]any{
				"command": "sleep 30", "cwd": "/workspace", "timeout_seconds": 30,
			}, nil)
		}()
		busyDeadline := time.Now().Add(5 * time.Second)
		for {
			var status applicationEnvelope
			if err := client.request(t.Context(), http.MethodGet, applicationPath(applicationID), nil, &status); err == nil && status.Application.State == "busy" {
				break
			}
			if time.Now().After(busyDeadline) {
				cancelRequest()
				t.Fatal("cancel target did not become busy")
			}
			time.Sleep(25 * time.Millisecond)
		}
		cancelRequest()
		err := <-requestDone
		var controllerError *apiError
		if !errors.Is(err, context.Canceled) && !(errors.As(err, &controllerError) && controllerError.Code == "request_canceled") {
			t.Fatalf("unexpected cancellation result: %v", err)
		}
		stopDeadline := time.Now().Add(10 * time.Second)
		for docker.inspect(t, containerID).State.Running {
			if time.Now().After(stopDeadline) {
				t.Fatal("canceled container is still running")
			}
			time.Sleep(25 * time.Millisecond)
		}
		result := client.exec(t, applicationID, "test \"$(cat /workspace/timeout-marker.txt)\" = timeout-preserved")
		if result.ExitCode != 0 {
			t.Fatalf("workspace was not preserved after cancellation: exit=%d stderr=%q", result.ExitCode, result.Stderr)
		}
	})

	client.post(t, applicationPath(applicationID)+"/files/write", map[string]any{
		"path":    "/workspace/persist.txt",
		"content": "persists-across-lifecycle",
	}, nil)
	var readResult readFileResult
	client.post(t, applicationPath(applicationID)+"/files/read", map[string]any{
		"path": "/workspace/persist.txt",
	}, &readResult)
	if readResult.Content != "persists-across-lifecycle" || !readResult.EOF {
		t.Fatalf("unexpected file result: %+v", readResult)
	}

	client.post(t, applicationPath(applicationID)+"/release", map[string]any{}, nil)
	if docker.inspect(t, containerID).State.Running {
		t.Fatal("released container is still running")
	}
	volumeName := oneResource(t, docker, "volume", applicationID)

	client.runID = "run-" + randomHex(t, 12)
	client.post(t, applicationPath(applicationID)+"/create", map[string]any{}, nil)
	if got := oneResource(t, docker, "container", applicationID); got != containerID {
		t.Fatalf("stop/start replaced container: old=%s new=%s", containerID, got)
	}
	assertPersisted(t, client, applicationID)

	client.post(t, applicationPath(applicationID)+"/release", map[string]any{}, nil)
	if output, err := docker.run(t.Context(), "rm", containerID); err != nil {
		t.Fatalf("remove stopped container: %v\n%s", err, output)
	}
	if got := oneResource(t, docker, "volume", applicationID); got != volumeName {
		t.Fatalf("container removal replaced volume: old=%s new=%s", volumeName, got)
	}
	client.runID = "run-" + randomHex(t, 12)
	client.post(t, applicationPath(applicationID)+"/create", map[string]any{}, nil)
	recreatedID := oneResource(t, docker, "container", applicationID)
	if recreatedID == containerID {
		t.Fatalf("expected a recreated container, still found %s", containerID)
	}
	assertPersisted(t, client, applicationID)

	client.delete(t, applicationPath(applicationID))
	destroyed = true
	if !waitForNoResources(docker, applicationID, 15*time.Second) {
		t.Fatalf("resources remain after destroy: containers=%v volumes=%v", docker.resources(context.Background(), "container", applicationID), docker.resources(context.Background(), "volume", applicationID))
	}
}

func TestDockerSandboxHundredApplicationCap(t *testing.T) {
	if os.Getenv(stressEnvironment) != "1" {
		t.Skipf("set %s=1 to run the 100-application Docker stress test", stressEnvironment)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(integrationURLEnvironment)), "/")
	token := strings.TrimSpace(os.Getenv(tokenEnvironment))
	if baseURL == "" || token == "" {
		t.Fatalf("%s and %s are required", integrationURLEnvironment, tokenEnvironment)
	}
	namespace := envOrDefault(namespaceEnvironment, "easygo-agent-it")
	docker := dockerCLI{binary: envOrDefault(dockerBinaryEnvironment, "docker")}

	const applicationCount = 100
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	sharedHTTP := &http.Client{Timeout: 11 * time.Minute}
	start := make(chan struct{})
	errorsFound := make(chan error, applicationCount*3+1)
	var applied, workers sync.WaitGroup
	var limited atomic.Int64
	var applicationMu sync.Mutex
	applicationIDs := make([]struct {
		client *apiClient
		id     string
	}, 0, applicationCount)

	applied.Add(applicationCount)
	workers.Add(applicationCount)
	for index := 0; index < applicationCount; index++ {
		go func(index int) {
			defer workers.Done()
			client := &apiClient{
				baseURL:   baseURL,
				token:     token,
				sessionID: fmt.Sprintf("sandbox-stress-%03d-%s", index, randomHexValue(8)),
				runID:     "run-" + randomHexValue(8),
				http:      sharedHTTP,
			}
			var envelope applicationEnvelope
			err := client.request(ctx, http.MethodPost, "/v1/applications", map[string]any{}, &envelope)
			if err != nil {
				var controllerError *apiError
				if errors.As(err, &controllerError) && controllerError.Code == "application_limit_reached" {
					limited.Add(1)
					applied.Done()
					return
				}
				errorsFound <- fmt.Errorf("application %d apply: %w", index, err)
				applied.Done()
				return
			}
			if envelope.Application.ID == "" {
				errorsFound <- fmt.Errorf("application %d apply: response omitted application.id", index)
				applied.Done()
				return
			}
			applicationID := envelope.Application.ID
			applicationMu.Lock()
			applicationIDs = append(applicationIDs, struct {
				client *apiClient
				id     string
			}{client: client, id: applicationID})
			applicationMu.Unlock()
			applied.Done()

			<-start
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cleanupCancel()
				if err := client.request(cleanupCtx, http.MethodDelete, applicationPath(applicationID), nil, nil); err != nil {
					errorsFound <- fmt.Errorf("application %d destroy: %w", index, err)
				}
			}()
			if err := client.request(ctx, http.MethodPost, applicationPath(applicationID)+"/create", map[string]any{}, nil); err != nil {
				errorsFound <- fmt.Errorf("application %d create: %w", index, err)
				return
			}
			var result execResult
			if err := client.request(ctx, http.MethodPost, applicationPath(applicationID)+"/exec", map[string]any{
				"command":         "sleep 0.1",
				"cwd":             "/workspace",
				"timeout_seconds": 30,
			}, &result); err != nil {
				errorsFound <- fmt.Errorf("application %d exec: %w", index, err)
				return
			}
			if result.ExitCode != 0 {
				errorsFound <- fmt.Errorf("application %d exec exit code %d", index, result.ExitCode)
			}
		}(index)
	}
	applied.Wait()
	applicationMu.Lock()
	successfulApplications := len(applicationIDs)
	applicationMu.Unlock()
	if successfulApplications < 1 || successfulApplications > 30 {
		t.Errorf("unexpected successful application count: got %d, want 1..30", successfulApplications)
	}
	if got := successfulApplications + int(limited.Load()); got != applicationCount {
		t.Errorf("100 concurrent apply results were not all success/application_limit: accounted=%d", got)
	}

	var maximumRunning atomic.Int64
	samplingDone := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-samplingDone:
				return
			case <-ticker.C:
				count, err := docker.runningCount(ctx, namespace)
				if err != nil {
					errorsFound <- fmt.Errorf("sample running containers: %w", err)
					return
				}
				for previous := maximumRunning.Load(); int64(count) > previous && !maximumRunning.CompareAndSwap(previous, int64(count)); previous = maximumRunning.Load() {
				}
			}
		}
	}()
	close(start)
	workers.Wait()
	close(samplingDone)
	sampler.Wait()
	close(errorsFound)

	for err := range errorsFound {
		t.Error(err)
	}
	if maximumRunning.Load() > 3 {
		t.Errorf("running container cap exceeded: observed %d, configured 3", maximumRunning.Load())
	}
	if maximumRunning.Load() == 0 && successfulApplications > 0 {
		t.Error("running-container sampler never observed a runtime")
	}
	if !waitForNoNamespaceResources(docker, namespace, 30*time.Second) {
		applicationMu.Lock()
		for _, application := range applicationIDs {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = application.client.request(cleanupCtx, http.MethodDelete, applicationPath(application.id), nil, nil)
			cleanupCancel()
		}
		applicationMu.Unlock()
		t.Fatalf("managed stress resources remain after cleanup")
	}
}

func TestDockerSandboxCanceledWaiterDoesNotLeak(t *testing.T) {
	if os.Getenv(stressEnvironment) != "1" {
		t.Skipf("set %s=1 to run the capacity-waiter cancellation test", stressEnvironment)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(integrationURLEnvironment)), "/")
	token := strings.TrimSpace(os.Getenv(tokenEnvironment))
	if baseURL == "" || token == "" {
		t.Fatalf("%s and %s are required", integrationURLEnvironment, tokenEnvironment)
	}
	namespace := envOrDefault(namespaceEnvironment, "easygo-agent-it")
	docker := dockerCLI{binary: envOrDefault(dockerBinaryEnvironment, "docker")}
	sharedHTTP := &http.Client{Timeout: 11 * time.Minute}

	clients := make([]*apiClient, 4)
	applicationIDs := make([]string, 4)
	for index := range clients {
		clients[index] = &apiClient{
			baseURL:   baseURL,
			token:     token,
			sessionID: fmt.Sprintf("sandbox-cancel-%d-%s", index, randomHex(t, 8)),
			runID:     "run-" + randomHex(t, 8),
			http:      sharedHTTP,
		}
		applicationIDs[index] = clients[index].apply(t)
	}
	t.Cleanup(func() {
		for index, applicationID := range applicationIDs {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = clients[index].request(cleanupCtx, http.MethodDelete, applicationPath(applicationID), nil, nil)
			cleanupCancel()
			if !waitForNoResources(docker, applicationID, 2*time.Second) {
				_, _ = docker.forceRemoveApplication(context.Background(), applicationID)
			}
		}
	})

	for index := 0; index < 3; index++ {
		clients[index].post(t, applicationPath(applicationIDs[index])+"/create", map[string]any{}, nil)
	}

	commandErrors := make(chan error, 3)
	var commands sync.WaitGroup
	commands.Add(3)
	for index := 0; index < 3; index++ {
		go func(index int) {
			defer commands.Done()
			var result execResult
			err := clients[index].request(t.Context(), http.MethodPost, applicationPath(applicationIDs[index])+"/exec", map[string]any{
				"command":         "sleep 2",
				"cwd":             "/workspace",
				"timeout_seconds": 10,
			}, &result)
			if err != nil {
				commandErrors <- err
				return
			}
			if result.ExitCode != 0 {
				commandErrors <- fmt.Errorf("sleep exited %d: %s", result.ExitCode, result.Stderr)
			}
		}(index)
	}

	busyDeadline := time.Now().Add(5 * time.Second)
	for {
		busy := 0
		for index := 0; index < 3; index++ {
			var status applicationEnvelope
			if err := clients[index].request(t.Context(), http.MethodGet, applicationPath(applicationIDs[index]), nil, &status); err == nil && status.Application.State == "busy" {
				busy++
			}
		}
		if busy == 3 {
			break
		}
		if time.Now().After(busyDeadline) {
			t.Fatal("three capacity-filling commands did not all become busy")
		}
		time.Sleep(25 * time.Millisecond)
	}

	canceledContext, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	err := clients[3].request(canceledContext, http.MethodPost, applicationPath(applicationIDs[3])+"/create", map[string]any{}, nil)
	cancel()
	if err == nil {
		t.Fatal("fourth create unexpectedly acquired capacity before cancellation")
	}
	var controllerError *apiError
	if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &controllerError) && controllerError.Code == "request_canceled") {
		t.Fatalf("unexpected canceled waiter error: %v", err)
	}

	commands.Wait()
	close(commandErrors)
	for commandErr := range commandErrors {
		t.Error(commandErr)
	}

	clients[3].runID = "run-" + randomHex(t, 8)
	retryContext, retryCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer retryCancel()
	if err := clients[3].request(retryContext, http.MethodPost, applicationPath(applicationIDs[3])+"/create", map[string]any{}, nil); err != nil {
		t.Fatalf("retry after canceled waiter: %v", err)
	}
	if running, err := docker.runningCount(t.Context(), namespace); err != nil || running > 3 {
		t.Fatalf("running containers after retry: count=%d err=%v", running, err)
	}
	for index, applicationID := range applicationIDs {
		clients[index].delete(t, applicationPath(applicationID))
	}
	if !waitForNoNamespaceResources(docker, namespace, 15*time.Second) {
		t.Fatal("capacity waiter test left managed resources")
	}
}

func assertContainerSecurity(t *testing.T, inspected dockerInspect, namespace, applicationID string) {
	t.Helper()
	if inspected.Config.Labels[managedLabel] != "true" || inspected.Config.Labels[namespaceLabel] != namespace || inspected.Config.Labels[applicationLabel] != applicationID {
		t.Fatalf("unexpected management labels: %v", inspected.Config.Labels)
	}
	if inspected.Config.User != "1001:1001" {
		t.Fatalf("runtime supervisor user=%q, want non-root 1001:1001", inspected.Config.User)
	}
	host := inspected.HostConfig
	if !host.ReadonlyRootfs || host.NetworkMode != "none" || host.Privileged {
		t.Fatalf("unsafe rootfs/network/privileged settings: readonly=%t network=%q privileged=%t", host.ReadonlyRootfs, host.NetworkMode, host.Privileged)
	}
	if host.Memory != 2<<30 || host.MemorySwap != 2<<30 || host.NanoCPUs != 1_000_000_000 || host.PidsLimit == nil || *host.PidsLimit != 256 || host.ShmSize != 64<<20 {
		t.Fatalf("unexpected resource limits: memory=%d swap=%d nano_cpus=%d pids=%v shm=%d", host.Memory, host.MemorySwap, host.NanoCPUs, host.PidsLimit, host.ShmSize)
	}
	if len(host.CapAdd) != 0 || !containsFold(host.CapDrop, "ALL") || !containsSubstring(host.SecurityOpt, "no-new-privileges") || containsSubstring(host.SecurityOpt, "seccomp=unconfined") {
		t.Fatalf("unsafe capability/security options: cap_add=%v cap_drop=%v security_opt=%v", host.CapAdd, host.CapDrop, host.SecurityOpt)
	}
	if host.PidMode != "" && host.PidMode != "private" {
		t.Fatalf("unexpected PID mode %q", host.PidMode)
	}
	if host.IpcMode != "" && host.IpcMode != "private" {
		t.Fatalf("unexpected IPC mode %q", host.IpcMode)
	}
	if len(host.Binds) != 0 || len(host.Devices) != 0 || len(host.PortBindings) != 0 || len(inspected.NetworkSettings.Ports) != 0 {
		t.Fatalf("unexpected host exposure: binds=%v devices=%v port_bindings=%v ports=%v", host.Binds, host.Devices, host.PortBindings, inspected.NetworkSettings.Ports)
	}
	tmpOptions, ok := host.Tmpfs["/tmp"]
	if !ok || !strings.Contains(tmpOptions, "size=268435456") || !strings.Contains(tmpOptions, "nosuid") || !strings.Contains(tmpOptions, "nodev") || !strings.Contains(tmpOptions, "noexec") {
		t.Fatalf("unexpected /tmp tmpfs options: %q", tmpOptions)
	}
	workspaceVolumes := 0
	for _, mount := range inspected.Mounts {
		if strings.Contains(mount.Source, "docker.sock") {
			t.Fatalf("runtime received Docker socket: %+v", mount)
		}
		if mount.Type == "volume" && mount.Destination == "/workspace" && mount.RW {
			workspaceVolumes++
			continue
		}
		if mount.Type == "tmpfs" && mount.Destination == "/tmp" {
			continue
		}
		t.Fatalf("unexpected runtime mount: %+v", mount)
	}
	if workspaceVolumes != 1 {
		t.Fatalf("expected exactly one writable /workspace volume, mounts=%+v", inspected.Mounts)
	}
	for key, want := range map[string]string{
		"GOPROXY":            "off",
		"GOSUMDB":            "off",
		"PIP_NO_INDEX":       "1",
		"npm_config_offline": "true",
	} {
		if !slices.Contains(inspected.Config.Env, key+"="+want) {
			t.Errorf("runtime environment missing %s=%s", key, want)
		}
	}
}

func assertPersisted(t *testing.T, client *apiClient, applicationID string) {
	t.Helper()
	result := client.exec(t, applicationID, "cat /workspace/persist.txt")
	if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "persists-across-lifecycle" {
		t.Fatalf("workspace did not persist: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
}

func waitForNoResources(docker dockerCLI, applicationID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		containers := docker.resources(context.Background(), "container", applicationID)
		volumes := docker.resources(context.Background(), "volume", applicationID)
		if len(containers) == 0 && len(volumes) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForNoNamespaceResources(docker dockerCLI, namespace string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		containers, containerErr := docker.resourcesForNamespace(context.Background(), "container", namespace, false)
		volumes, volumeErr := docker.resourcesForNamespace(context.Background(), "volume", namespace, false)
		if containerErr == nil && volumeErr == nil && len(containers) == 0 && len(volumes) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func oneResource(t *testing.T, docker dockerCLI, resource, applicationID string) string {
	t.Helper()
	resources := docker.resources(t.Context(), resource, applicationID)
	if len(resources) != 1 {
		t.Fatalf("%s resources for application %q: %v", resource, applicationID, resources)
	}
	return resources[0]
}

func (client *apiClient) apply(t *testing.T) string {
	t.Helper()
	var result applicationEnvelope
	client.post(t, "/v1/applications", map[string]any{}, &result)
	if result.Application.ID == "" {
		t.Fatal("apply response omitted application.id")
	}
	return result.Application.ID
}

func (client *apiClient) exec(t *testing.T, applicationID, command string) execResult {
	t.Helper()
	var result execResult
	client.post(t, applicationPath(applicationID)+"/exec", map[string]any{
		"command":         command,
		"cwd":             "/workspace",
		"timeout_seconds": 600,
	}, &result)
	return result
}

func (client *apiClient) post(t *testing.T, path string, body any, output any) {
	t.Helper()
	if err := client.request(t.Context(), http.MethodPost, path, body, output); err != nil {
		t.Fatal(err)
	}
}

func (client *apiClient) delete(t *testing.T, path string) {
	t.Helper()
	if err := client.request(t.Context(), http.MethodDelete, path, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func (client *apiClient) request(ctx context.Context, method, path string, body any, output any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, requestBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-EasyGo-Session-ID", client.sessionID)
	request.Header.Set("X-EasyGo-Run-ID", client.runID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(responseBody, &envelope)
		message := strings.TrimSpace(envelope.Error.Message)
		if message == "" {
			message = strings.TrimSpace(string(responseBody))
		}
		return &apiError{StatusCode: response.StatusCode, Code: envelope.Error.Code, Message: message}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

type dockerCLI struct{ binary string }

func (docker dockerCLI) inspect(t *testing.T, containerID string) dockerInspect {
	t.Helper()
	output, err := docker.run(t.Context(), "inspect", containerID)
	if err != nil {
		t.Fatalf("inspect container: %v\n%s", err, output)
	}
	var inspected []dockerInspect
	if err := json.Unmarshal([]byte(output), &inspected); err != nil || len(inspected) != 1 {
		t.Fatalf("decode docker inspect: %v\n%s", err, output)
	}
	return inspected[0]
}

func (docker dockerCLI) resources(ctx context.Context, resource, labelValue string) []string {
	var args []string
	switch resource {
	case "container":
		args = []string{"ps", "-aq"}
	case "volume":
		args = []string{"volume", "ls", "-q"}
	default:
		panic("unsupported Docker resource " + strconv.Quote(resource))
	}
	args = append(args,
		"--filter", "label="+managedLabel+"=true",
		"--filter", "label="+applicationLabel+"="+labelValue,
	)
	output, err := docker.run(ctx, args...)
	if err != nil {
		return []string{"docker-error:" + err.Error() + ":" + strings.TrimSpace(output)}
	}
	return strings.Fields(output)
}

func (docker dockerCLI) resourcesForNamespace(ctx context.Context, resource, namespace string, runningOnly bool) ([]string, error) {
	var args []string
	switch resource {
	case "container":
		args = []string{"ps", "-q"}
		if !runningOnly {
			args = []string{"ps", "-aq"}
		}
	case "volume":
		args = []string{"volume", "ls", "-q"}
	default:
		return nil, fmt.Errorf("unsupported Docker resource %q", resource)
	}
	args = append(args,
		"--filter", "label="+managedLabel+"=true",
		"--filter", "label="+namespaceLabel+"="+namespace,
	)
	output, err := docker.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", resource, err, strings.TrimSpace(output))
	}
	return strings.Fields(output), nil
}

func (docker dockerCLI) runningCount(ctx context.Context, namespace string) (int, error) {
	containers, err := docker.resourcesForNamespace(ctx, "container", namespace, true)
	return len(containers), err
}

func (docker dockerCLI) run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, docker.binary, args...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (docker dockerCLI) forceRemoveApplication(ctx context.Context, applicationID string) (string, error) {
	containers := docker.resources(ctx, "container", applicationID)
	if len(containers) > 0 {
		args := append([]string{"rm", "-f"}, containers...)
		if output, err := docker.run(ctx, args...); err != nil {
			return output, err
		}
	}
	volumes := docker.resources(ctx, "volume", applicationID)
	if len(volumes) > 0 {
		args := append([]string{"volume", "rm", "-f"}, volumes...)
		return docker.run(ctx, args...)
	}
	return "", nil
}

func applicationPath(applicationID string) string {
	return "/v1/applications/" + applicationID
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func randomHex(t *testing.T, bytesCount int) string {
	t.Helper()
	data := make([]byte, bytesCount)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(data)
}

func randomHexValue(bytesCount int) string {
	data := make([]byte, bytesCount)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, expected string) bool {
	for _, value := range values {
		if strings.Contains(value, expected) {
			return true
		}
	}
	return false
}
