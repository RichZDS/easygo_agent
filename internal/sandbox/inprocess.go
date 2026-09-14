package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NewInProcessHandler returns the real HTTP handler over an in-process fake
// Engine and store. Agent tools and bench tests use this so they share the
// controller contract without importing Docker.
func NewInProcessHandler(token string) (http.Handler, error) {
	cfg := Config{
		Namespace:           "test",
		ListenAddress:       "127.0.0.1:8787",
		AuthToken:           token,
		StatePath:           "/tmp/easygo-inprocess-state.db",
		DockerHost:          "unix:///var/run/docker.sock",
		Image:               "sandbox:test",
		MaxRunning:          3,
		MaxApplications:     30,
		MaxStarting:         2,
		WaitTimeout:         30 * time.Second,
		BaseIdleTTL:         time.Minute,
		HardTTL:             5 * time.Hour,
		ReaperInterval:      10 * time.Second,
		CPUs:                1,
		MemoryBytes:         2 << 30,
		PIDsLimit:           256,
		ShmSizeBytes:        64 << 20,
		TmpSizeBytes:        256 << 20,
		WorkspaceSoftLimit:  1 << 30,
		TotalWorkspaceLimit: 20 << 30,
		MinFreeDisk:         10 << 30,
		MaxOutputBytes:      64 << 10,
		CommandTimeout:      2 * time.Minute,
		MaxCommandTimeout:   10 * time.Minute,
	}
	manager, err := NewManager(cfg, newInProcessEngine(), newInProcessStore(), realClock{})
	if err != nil {
		return nil, err
	}
	return NewHTTPHandler(token, manager)
}

type inProcessEngine struct {
	mu         sync.Mutex
	volumes    map[string]map[string]string
	containers map[string]inProcessContainer
	next       int
}

type inProcessContainer struct {
	spec    ContainerSpec
	running bool
}

func newInProcessEngine() *inProcessEngine {
	return &inProcessEngine{volumes: map[string]map[string]string{}, containers: map[string]inProcessContainer{}}
}

func (*inProcessEngine) ValidateRuntimeImage(context.Context, string) error { return nil }

func (engine *inProcessEngine) CreateVolume(_ context.Context, name string, labels map[string]string) (bool, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if _, exists := engine.volumes[name]; exists {
		return false, nil
	}
	engine.volumes[name] = cloneLabels(labels)
	return true, nil
}

func (engine *inProcessEngine) InspectVolume(_ context.Context, name string) (VolumeInfo, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	labels, exists := engine.volumes[name]
	return VolumeInfo{Exists: exists, Labels: cloneLabels(labels)}, nil
}

func (engine *inProcessEngine) CreateContainer(_ context.Context, spec ContainerSpec) (string, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.next++
	id := fmt.Sprintf("%064x", engine.next)
	engine.containers[id] = inProcessContainer{spec: spec}
	return id, nil
}

func (engine *inProcessEngine) InspectContainer(_ context.Context, reference string) (ContainerInfo, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if container, ok := engine.containers[reference]; ok {
		return ContainerInfo{ID: reference, Name: container.spec.Name, Exists: true, Running: container.running, Labels: cloneLabels(container.spec.Labels)}, nil
	}
	for id, container := range engine.containers {
		if container.spec.Name == reference {
			return ContainerInfo{ID: id, Name: container.spec.Name, Exists: true, Running: container.running, Labels: cloneLabels(container.spec.Labels)}, nil
		}
	}
	return ContainerInfo{}, nil
}

func (engine *inProcessEngine) StartContainer(_ context.Context, id string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	container, ok := engine.containers[id]
	if !ok {
		return fmt.Errorf("container absent")
	}
	container.running = true
	engine.containers[id] = container
	return nil
}

func (engine *inProcessEngine) StopContainer(_ context.Context, id string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	container, ok := engine.containers[id]
	if !ok {
		return nil
	}
	container.running = false
	engine.containers[id] = container
	return nil
}

func (engine *inProcessEngine) RemoveContainer(_ context.Context, id string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	delete(engine.containers, id)
	return nil
}

func (engine *inProcessEngine) RemoveVolume(_ context.Context, name string) error {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	delete(engine.volumes, name)
	return nil
}

func (engine *inProcessEngine) Exec(_ context.Context, _ string, request EngineExecRequest) (EngineExecResult, error) {
	joined := strings.Join(request.Command, " ")
	switch {
	case strings.Contains(joined, "easygo-write"):
		return EngineExecResult{ExitCode: 0, Stdout: "12\n"}, nil
	case strings.Contains(joined, "easygo-read"):
		return EngineExecResult{ExitCode: 0, Stdout: "12\npackage main"}, nil
	case request.User == "0:0":
		return EngineExecResult{ExitCode: 0}, nil
	default:
		return EngineExecResult{ExitCode: 0, Stdout: "ok\n"}, nil
	}
}

func (engine *inProcessEngine) ListManaged(_ context.Context, _ string) (ManagedResources, error) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	var resources ManagedResources
	for id, container := range engine.containers {
		resources.Containers = append(resources.Containers, ManagedContainer{ID: id, Name: container.spec.Name, Running: container.running, Labels: cloneLabels(container.spec.Labels)})
	}
	for name, labels := range engine.volumes {
		resources.Volumes = append(resources.Volumes, ManagedVolume{Name: name, Labels: cloneLabels(labels)})
	}
	return resources, nil
}

func (engine *inProcessEngine) StorageUsage(context.Context, string) (StorageUsage, error) {
	return StorageUsage{FreeBytes: 1 << 50, WorkspaceBytes: map[string]int64{}}, nil
}

type inProcessStore struct {
	mu    sync.Mutex
	items map[string]Application
}

func newInProcessStore() *inProcessStore { return &inProcessStore{items: map[string]Application{}} }

func (store *inProcessStore) List() ([]Application, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Application, 0, len(store.items))
	for _, item := range store.items {
		result = append(result, cloneApplication(item))
	}
	return result, nil
}

func (store *inProcessStore) Put(application Application) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items[application.ID] = cloneApplication(application)
	return nil
}

func (store *inProcessStore) Delete(applicationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.items, applicationID)
	return nil
}

func (store *inProcessStore) Close() error { return nil }
