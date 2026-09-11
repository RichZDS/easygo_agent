package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"syscall"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
)

const (
	storageUsageRetryAttempts  = 4
	storageUsageRetryBaseDelay = 25 * time.Millisecond
)

type dockerDiskUsageFunc func(context.Context, dockerclient.DiskUsageOptions) (dockerclient.DiskUsageResult, error)

// DockerEngine adapts the current split Moby client/api modules to Engine.
// It deliberately exposes no generic create or exec options to HTTP callers.
type DockerEngine struct {
	client    *dockerclient.Client
	diskUsage dockerDiskUsageFunc
}

func NewDockerEngine(host string) (*DockerEngine, error) {
	client, err := dockerclient.New(dockerclient.WithHost(host), dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker Engine client: %w", err)
	}
	return &DockerEngine{client: client, diskUsage: client.DiskUsage}, nil
}

func (engine *DockerEngine) Close() error { return engine.client.Close() }

func (engine *DockerEngine) ValidateRuntimeImage(ctx context.Context, image string) error {
	result, err := engine.client.ImageInspect(ctx, image)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("prebuilt runtime image %q is not present; automatic pulls are disabled", image)
		}
		return err
	}
	if result.Config == nil || result.Config.Labels[LabelRuntimeImage] != "true" {
		return fmt.Errorf("image %q is missing required label %s=true", image, LabelRuntimeImage)
	}
	return nil
}

func (engine *DockerEngine) CreateVolume(ctx context.Context, name string, labels map[string]string) (bool, error) {
	inspection, err := engine.client.VolumeInspect(ctx, name, dockerclient.VolumeInspectOptions{})
	if err == nil {
		if !sameOwnershipLabels(inspection.Volume.Labels, labels) {
			return false, fmt.Errorf("volume %q already exists with unexpected ownership labels", name)
		}
		return false, nil
	}
	if !cerrdefs.IsNotFound(err) {
		return false, err
	}
	if _, err := engine.client.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{Name: name, Driver: "local", Labels: labels}); err != nil {
		return false, err
	}
	inspection, err = engine.client.VolumeInspect(ctx, name, dockerclient.VolumeInspectOptions{})
	if err != nil {
		return false, err
	}
	if !sameOwnershipLabels(inspection.Volume.Labels, labels) {
		return false, fmt.Errorf("volume %q was created with unexpected ownership labels", name)
	}
	return true, nil
}

func (engine *DockerEngine) InspectVolume(ctx context.Context, name string) (VolumeInfo, error) {
	result, err := engine.client.VolumeInspect(ctx, name, dockerclient.VolumeInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return VolumeInfo{}, nil
		}
		return VolumeInfo{}, err
	}
	return VolumeInfo{Exists: true, Labels: cloneLabels(result.Volume.Labels)}, nil
}

func (engine *DockerEngine) CreateContainer(ctx context.Context, spec ContainerSpec) (string, error) {
	configuration, hostConfiguration, err := dockerContainerConfiguration(spec)
	if err != nil {
		return "", err
	}
	result, err := engine.client.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:     configuration,
		HostConfig: hostConfiguration,
		Name:       spec.Name,
	})
	if err != nil {
		// A daemon/proxy can return an ID together with a transport error. Keep it
		// so the Manager can reconcile the ambiguous create without duplicating a
		// deterministically named container.
		return result.ID, err
	}
	return result.ID, nil
}

func dockerContainerConfiguration(spec ContainerSpec) (*container.Config, *container.HostConfig, error) {
	if math.IsNaN(spec.CPUs) || math.IsInf(spec.CPUs, 0) || spec.CPUs < minimumCPUs || spec.CPUs > maximumCPUs {
		return nil, nil, fmt.Errorf("sandbox CPU limit must be finite and between %.2f and %.0f", minimumCPUs, maximumCPUs)
	}
	pidsLimit := spec.PIDsLimit
	stopTimeout := 2
	configuration := &container.Config{
		Image:           spec.Image,
		User:            "1001:1001",
		WorkingDir:      "/workspace",
		NetworkDisabled: true,
		Env:             runtimeOfflineEnvironment(),
		Labels:          cloneLabels(spec.Labels),
		StopTimeout:     &stopTimeout,
	}
	hostConfiguration := &container.HostConfig{
		NetworkMode:     container.NetworkMode("none"),
		IpcMode:         container.IpcMode("private"),
		LogConfig:       container.LogConfig{Type: "none"},
		ReadonlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		SecurityOpt:     []string{"no-new-privileges=true"},
		Privileged:      false,
		PublishAllPorts: false,
		AutoRemove:      false,
		ShmSize:         spec.ShmSize,
		Resources: container.Resources{
			NanoCPUs:   int64(spec.CPUs * 1_000_000_000),
			Memory:     spec.Memory,
			MemorySwap: spec.Memory,
			PidsLimit:  &pidsLimit,
		},
		Mounts: []mount.Mount{{
			Type:          mount.TypeVolume,
			Source:        spec.VolumeName,
			Target:        "/workspace",
			ReadOnly:      false,
			VolumeOptions: &mount.VolumeOptions{NoCopy: false},
		}},
		Tmpfs: map[string]string{
			"/tmp": fmt.Sprintf("rw,nosuid,nodev,noexec,size=%d,mode=1777", spec.TmpSize),
		},
	}
	return configuration, hostConfiguration, nil
}

func runtimeOfflineEnvironment() []string {
	return []string{
		"TMPDIR=/workspace/.tmp",
		"GOTMPDIR=/workspace/.tmp/go",
		"GOPROXY=off",
		"GOSUMDB=off",
		"PIP_NO_INDEX=1",
		"npm_config_offline=true",
	}
}

func (engine *DockerEngine) InspectContainer(ctx context.Context, containerID string) (ContainerInfo, error) {
	result, err := engine.client.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return ContainerInfo{}, nil
		}
		return ContainerInfo{}, err
	}
	info := ContainerInfo{ID: result.Container.ID, Name: strings.TrimPrefix(result.Container.Name, "/"), Exists: true}
	if result.Container.State != nil {
		info.Running = result.Container.State.Running
	}
	if result.Container.Config != nil {
		info.Labels = cloneLabels(result.Container.Config.Labels)
	}
	return info, nil
}

func (engine *DockerEngine) StartContainer(ctx context.Context, containerID string) error {
	_, err := engine.client.ContainerStart(ctx, containerID, dockerclient.ContainerStartOptions{})
	if cerrdefs.IsNotModified(err) {
		return nil
	}
	return err
}

func (engine *DockerEngine) StopContainer(ctx context.Context, containerID string) error {
	timeout := 2
	_, err := engine.client.ContainerStop(ctx, containerID, dockerclient.ContainerStopOptions{Timeout: &timeout})
	if cerrdefs.IsNotFound(err) || cerrdefs.IsNotModified(err) {
		return nil
	}
	return err
}

func (engine *DockerEngine) RemoveContainer(ctx context.Context, containerID string) error {
	inspection, err := engine.InspectContainer(ctx, containerID)
	if err != nil {
		return err
	}
	if !inspection.Exists {
		return nil
	}
	if inspection.Labels[LabelManaged] != "true" {
		return errors.New("refusing to remove container without EasyGo managed label")
	}
	_, err = engine.client.ContainerRemove(ctx, containerID, dockerclient.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (engine *DockerEngine) RemoveVolume(ctx context.Context, volumeName string) error {
	inspection, err := engine.InspectVolume(ctx, volumeName)
	if err != nil {
		return err
	}
	if !inspection.Exists {
		return nil
	}
	if inspection.Labels[LabelManaged] != "true" {
		return errors.New("refusing to remove volume without EasyGo managed label")
	}
	_, err = engine.client.VolumeRemove(ctx, volumeName, dockerclient.VolumeRemoveOptions{Force: true})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

func (engine *DockerEngine) Exec(ctx context.Context, containerID string, request EngineExecRequest) (EngineExecResult, error) {
	created, err := engine.client.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{
		User:         request.User,
		Privileged:   false,
		TTY:          false,
		AttachStdin:  request.Stdin != "",
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   request.WorkingDir,
		Cmd:          append([]string(nil), request.Command...),
	})
	if err != nil {
		return EngineExecResult{}, err
	}
	attached, err := engine.client.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{TTY: false})
	if err != nil {
		return EngineExecResult{}, err
	}
	defer attached.Close()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			attached.Close()
		case <-closed:
		}
	}()
	writeDone := make(chan error, 1)
	go func() {
		if request.Stdin != "" {
			if _, err := io.WriteString(attached.Conn, request.Stdin); err != nil {
				writeDone <- fmt.Errorf("write exec stdin: %w", err)
				return
			}
		}
		if err := attached.CloseWrite(); err != nil {
			writeDone <- fmt.Errorf("close exec stdin: %w", err)
			return
		}
		writeDone <- nil
	}()
	stdout := newLimitedBuffer(request.StdoutLimit)
	stderr := newLimitedBuffer(request.StderrLimit)
	if _, err := stdcopy.StdCopy(stdout, stderr, attached.Reader); err != nil {
		if ctx.Err() != nil {
			return EngineExecResult{}, ctx.Err()
		}
		return EngineExecResult{}, fmt.Errorf("read exec output: %w", err)
	}
	if writeErr := <-writeDone; writeErr != nil && ctx.Err() == nil {
		return EngineExecResult{}, writeErr
	}
	for {
		inspection, err := engine.client.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
		if err != nil {
			return EngineExecResult{}, err
		}
		if !inspection.Running {
			return EngineExecResult{
				ExitCode:        inspection.ExitCode,
				Stdout:          stdout.String(),
				Stderr:          stderr.String(),
				StdoutTruncated: stdout.Truncated(),
				StderrTruncated: stderr.Truncated(),
			}, nil
		}
		select {
		case <-ctx.Done():
			return EngineExecResult{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (engine *DockerEngine) ListManaged(ctx context.Context, namespace string) (ManagedResources, error) {
	filters := make(dockerclient.Filters).
		Add("label", LabelManaged+"=true").
		Add("label", LabelNamespace+"="+namespace)
	containers, err := engine.client.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return ManagedResources{}, err
	}
	volumes, err := engine.client.VolumeList(ctx, dockerclient.VolumeListOptions{Filters: filters})
	if err != nil {
		return ManagedResources{}, err
	}
	result := ManagedResources{
		Containers: make([]ManagedContainer, 0, len(containers.Items)),
		Volumes:    make([]ManagedVolume, 0, len(volumes.Items)),
	}
	for _, item := range containers.Items {
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		result.Containers = append(result.Containers, ManagedContainer{ID: item.ID, Name: name, Running: item.State == container.StateRunning, Labels: cloneLabels(item.Labels)})
	}
	for _, item := range volumes.Items {
		result.Volumes = append(result.Volumes, ManagedVolume{Name: item.Name, Labels: cloneLabels(item.Labels)})
	}
	return result, nil
}

func (engine *DockerEngine) StorageUsage(ctx context.Context, namespace string) (StorageUsage, error) {
	diskUsage := engine.diskUsage
	if diskUsage == nil && engine.client != nil {
		diskUsage = engine.client.DiskUsage
	}
	if diskUsage == nil {
		return StorageUsage{}, errors.New("Docker disk-usage client is unavailable")
	}
	var result StorageUsage
	var lastErr error
	for attempt := 0; attempt < storageUsageRetryAttempts; attempt++ {
		usage, err := diskUsage(ctx, dockerclient.DiskUsageOptions{Volumes: true, Verbose: true})
		result = StorageUsage{WorkspaceBytes: make(map[string]int64), FreeBytes: -1}
		if err == nil {
			for _, volume := range usage.Volumes.Items {
				if volume.Labels[LabelManaged] != "true" || volume.Labels[LabelNamespace] != namespace {
					continue
				}
				if volume.UsageData == nil || volume.UsageData.Size < 0 {
					err = fmt.Errorf("Docker did not report usage for managed volume %q", volume.Name)
					break
				}
				result.WorkspaceBytes[volume.Name] = volume.UsageData.Size
				if result.TotalBytes <= math.MaxInt64-volume.UsageData.Size {
					result.TotalBytes += volume.UsageData.Size
				} else {
					result.TotalBytes = math.MaxInt64
				}
			}
		}
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		if ctx.Err() != nil {
			return StorageUsage{}, ctx.Err()
		}
		if attempt+1 < storageUsageRetryAttempts {
			delay := storageUsageRetryBaseDelay << attempt
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return StorageUsage{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr != nil {
		return StorageUsage{}, fmt.Errorf("Docker volume usage remained unavailable after %d attempts: %w", storageUsageRetryAttempts, lastErr)
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return StorageUsage{}, fmt.Errorf("measure controller filesystem free space: %w", err)
	}
	available := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize == 0 || available <= uint64(math.MaxInt64)/blockSize {
		result.FreeBytes = int64(available * blockSize)
	} else {
		result.FreeBytes = math.MaxInt64
	}
	return result, nil
}

func sameOwnershipLabels(actual, expected map[string]string) bool {
	for _, key := range []string{LabelManaged, LabelNamespace, LabelApplicationID, LabelResource, LabelHardExpiresAt} {
		if actual[key] != expected[key] {
			return false
		}
	}
	return true
}

type limitedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	if limit < 0 {
		limit = 0
	}
	return &limitedBuffer{data: make([]byte, 0, limit), limit: limit}
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - len(buffer.data)
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		buffer.data = append(buffer.data, data[:remaining]...)
	}
	if len(data) > remaining {
		buffer.truncated = true
	}
	return written, nil
}

func (buffer *limitedBuffer) String() string  { return string(buffer.data) }
func (buffer *limitedBuffer) Truncated() bool { return buffer.truncated }

var _ Engine = (*DockerEngine)(nil)
