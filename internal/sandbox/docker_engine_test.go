package sandbox

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
)

func TestLimitedBufferTruncatesButConsumesEveryByte(t *testing.T) {
	buffer := newLimitedBuffer(4)
	first, err := buffer.Write([]byte("abc"))
	if err != nil || first != 3 {
		t.Fatalf("first write=%d, err=%v", first, err)
	}
	second, err := buffer.Write([]byte("defgh"))
	if err != nil || second != 5 {
		t.Fatalf("second write=%d, err=%v", second, err)
	}
	if buffer.String() != "abcd" || !buffer.Truncated() {
		t.Fatalf("buffer=%q truncated=%v", buffer.String(), buffer.Truncated())
	}
}

func TestCompleteUTF8PrefixShrinksOnlyAnIncompleteTrailingRune(t *testing.T) {
	prefix, valid := completeUTF8Prefix("a\xe4\xb8")
	if !valid || prefix != "a" {
		t.Fatalf("prefix=%q valid=%v", prefix, valid)
	}
	if prefix, valid := completeUTF8Prefix("\xffa"); valid || prefix != "" {
		t.Fatalf("invalid bytes accepted: prefix=%q valid=%v", prefix, valid)
	}
}

func TestDockerContainerConfigurationFixesSecurityAndResourceTemplate(t *testing.T) {
	spec := ContainerSpec{
		Name:       "sandbox-app",
		Image:      "sandbox:test",
		VolumeName: "workspace-app",
		Labels:     map[string]string{LabelManaged: "true"},
		CPUs:       1,
		Memory:     2 << 30,
		PIDsLimit:  256,
		ShmSize:    64 << 20,
		TmpSize:    256 << 20,
	}
	configuration, host, err := dockerContainerConfiguration(spec)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.Image != spec.Image || configuration.WorkingDir != "/workspace" || !configuration.NetworkDisabled {
		t.Fatalf("container config=%+v", configuration)
	}
	if configuration.User != "1001:1001" {
		t.Fatalf("PID 1 user=%q, want isolated supervisor 1001:1001", configuration.User)
	}
	for _, env := range []string{
		"TMPDIR=/workspace/.tmp",
		"GOTMPDIR=/workspace/.tmp/go",
		"GOPROXY=off",
		"GOSUMDB=off",
		"PIP_NO_INDEX=1",
		"npm_config_offline=true",
	} {
		if !contains(configuration.Env, env) {
			t.Fatalf("runtime env missing %s: %v", env, configuration.Env)
		}
	}
	if host.NetworkMode != container.NetworkMode("none") || host.IpcMode != container.IpcMode("private") {
		t.Fatalf("network=%q ipc=%q", host.NetworkMode, host.IpcMode)
	}
	if !host.ReadonlyRootfs || host.Privileged || host.PublishAllPorts || host.AutoRemove {
		t.Fatalf("unsafe host flags: %+v", host)
	}
	if len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" || len(host.CapAdd) != 0 {
		t.Fatalf("capabilities add=%v drop=%v", host.CapAdd, host.CapDrop)
	}
	if len(host.SecurityOpt) != 1 || !strings.Contains(host.SecurityOpt[0], "no-new-privileges") {
		t.Fatalf("security options=%v", host.SecurityOpt)
	}
	if host.Memory != spec.Memory || host.MemorySwap != spec.Memory || host.NanoCPUs != 1_000_000_000 || host.PidsLimit == nil || *host.PidsLimit != spec.PIDsLimit || host.ShmSize != spec.ShmSize {
		t.Fatalf("resource template=%+v", host.Resources)
	}
	if len(host.Mounts) != 1 || host.Mounts[0].Type != mount.TypeVolume || host.Mounts[0].Source != spec.VolumeName || host.Mounts[0].Target != "/workspace" || host.Mounts[0].ReadOnly || host.Mounts[0].VolumeOptions == nil || host.Mounts[0].VolumeOptions.NoCopy {
		t.Fatalf("mounts=%+v", host.Mounts)
	}
	if len(host.Binds) != 0 || len(host.Devices) != 0 || len(host.PortBindings) != 0 || len(configuration.ExposedPorts) != 0 {
		t.Fatalf("unexpected host exposure: binds=%v devices=%v ports=%v exposed=%v", host.Binds, host.Devices, host.PortBindings, configuration.ExposedPorts)
	}
	tmpfs := host.Tmpfs["/tmp"]
	if !strings.Contains(tmpfs, "noexec") || !strings.Contains(tmpfs, "nosuid") || !strings.Contains(tmpfs, "nodev") || !strings.Contains(tmpfs, "size=268435456") {
		t.Fatalf("tmpfs=%q", tmpfs)
	}
}

func TestDockerContainerConfigurationRejectsAnUnboundedCPUConversion(t *testing.T) {
	for _, cpus := range []float64{0, 0.001, math.NaN(), math.Inf(1)} {
		if _, _, err := dockerContainerConfiguration(ContainerSpec{CPUs: cpus}); err == nil {
			t.Fatalf("invalid CPU limit %v was accepted", cpus)
		}
	}
}

func TestDockerEngineStorageUsageRetriesTransientDockerSnapshotFailure(t *testing.T) {
	calls := 0
	engine := &DockerEngine{diskUsage: func(context.Context, dockerclient.DiskUsageOptions) (dockerclient.DiskUsageResult, error) {
		calls++
		if calls == 1 {
			return dockerclient.DiskUsageResult{}, errors.New("volume disappeared while Docker was measuring it")
		}
		return dockerclient.DiskUsageResult{}, nil
	}}
	usage, err := engine.StorageUsage(context.Background(), "test-namespace")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("Docker disk-usage calls=%d, want 2", calls)
	}
	if usage.TotalBytes != 0 || usage.FreeBytes <= 0 {
		t.Fatalf("usage=%+v", usage)
	}
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
