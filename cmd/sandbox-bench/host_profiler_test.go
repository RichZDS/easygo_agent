package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockingHostSnapshotSource struct{}

func (blockingHostSnapshotSource) Snapshot(ctx context.Context) hostProcessSnapshot {
	<-ctx.Done()
	return hostProcessSnapshot{At: time.Now(), MissingReason: ctx.Err().Error()}
}

func TestPSHostSnapshotSourceDiscoversOnlyDockerHostProcesses(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		output string
		want   int
	}{
		{
			name: "macOS Docker Desktop",
			goos: "darwin",
			output: "101 1 1024 0:01.25 /Applications/Docker.app/Contents/MacOS/com.docker.backend -watchdog\n" +
				"102 1 2048 0:00.50 /Applications/Docker.app/Contents/MacOS/com.docker.virtualization\n" +
				"104 1 2097152 0:04.50 com.apple.Virtualization.VirtualMachine\n" +
				"103 1 9999 9:59.99 /usr/local/bin/sandbox-bench\n",
			want: 3,
		},
		{
			name: "Linux Engine",
			goos: "linux",
			output: "201 1 4096 1:02.00 /usr/bin/dockerd --host=fd://\n" +
				"202 1 3072 0:30.00 /usr/bin/containerd\n" +
				"203 202 512 0:00.25 /usr/bin/containerd-shim-runc-v2 -namespace moby\n" +
				"204 1 9999 5:00.00 /usr/bin/python3 worker.py\n",
			want: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			source := &psHostSnapshotSource{
				goos: test.goos,
				now:  func() time.Time { return now },
				run: func(context.Context, string, ...string) ([]byte, error) {
					return []byte(test.output), nil
				},
			}
			snapshot := source.Snapshot(context.Background())
			if snapshot.At != now || len(snapshot.Processes) != test.want || snapshot.MissingReason != "" {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			if _, included := snapshot.Processes[103]; included {
				t.Fatal("unrelated benchmark process was included")
			}
			if _, included := snapshot.Processes[204]; included {
				t.Fatal("unrelated Linux process was included")
			}
		})
	}
}

func TestHostProfileReportsCPUDeltaAndSampledRSSPIDPeaks(t *testing.T) {
	start := time.Unix(100, 0)
	trace := hostProfileTrace{Samples: []hostProcessSnapshot{
		{At: start, Method: "fake ps", Processes: map[int]hostProcessUsage{1: {CPUTime: 100 * time.Millisecond, RSSBytes: 100}}},
		{At: start.Add(time.Millisecond), Method: "fake ps", Processes: map[int]hostProcessUsage{1: {CPUTime: 130 * time.Millisecond, RSSBytes: 200}, 2: {CPUTime: 5 * time.Millisecond, RSSBytes: 50}}},
		{At: start.Add(2 * time.Millisecond), Method: "fake ps", Processes: map[int]hostProcessUsage{1: {CPUTime: 160 * time.Millisecond, RSSBytes: 150}, 2: {CPUTime: 15 * time.Millisecond, RSSBytes: 20}}},
	}}
	observation := trace.Observe(start, start.Add(2*time.Millisecond))
	if observation.CPUTimeMS == nil || *observation.CPUTimeMS != 75 {
		t.Fatalf("CPU delta=%v", pointerValue(observation.CPUTimeMS))
	}
	if observation.PeakRSSBytes == nil || *observation.PeakRSSBytes != 250 {
		t.Fatalf("peak RSS=%v", pointerValue(observation.PeakRSSBytes))
	}
	if observation.PeakPIDs == nil || *observation.PeakPIDs != 2 || observation.Samples != 3 {
		t.Fatalf("PIDs=%v samples=%d", pointerValue(observation.PeakPIDs), observation.Samples)
	}
	if observation.Method != "fake ps" || observation.Missing != "" {
		t.Fatalf("metadata=%+v", observation)
	}
}

func TestHostProfileNeverInventsZeroWhenProcessesCannotBeDiscovered(t *testing.T) {
	start := time.Unix(100, 0)
	trace := hostProfileTrace{Samples: []hostProcessSnapshot{{
		At: start, Method: "macos_ps_docker_desktop_backend", MissingReason: "no Docker Desktop backend process discovered",
	}}}
	observation := trace.Observe(start, start.Add(time.Millisecond))
	if observation.CPUTimeMS != nil || observation.PeakRSSBytes != nil || observation.PeakPIDs != nil {
		t.Fatalf("invented unavailable host metrics: %+v", observation)
	}
	if observation.Method == "" || observation.Missing == "" {
		t.Fatalf("missing observation metadata: %+v", observation)
	}
}

func TestPSHostSnapshotSourceReportsCommandFailureAsMissing(t *testing.T) {
	source := &psHostSnapshotSource{
		goos: "darwin",
		now:  time.Now,
		run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("ps unavailable")
		},
	}
	snapshot := source.Snapshot(context.Background())
	if len(snapshot.Processes) != 0 || snapshot.MissingReason == "" || snapshot.Method == "" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestParsePSCPUTimeAllowsOverflowingMinutesInTwoFieldFormat(t *testing.T) {
	tests := map[string]time.Duration{
		"00:01.25":   1250 * time.Millisecond,
		"67:30.03":   67*time.Minute + 30030*time.Millisecond,
		"2:03:04.50": 2*time.Hour + 3*time.Minute + 4500*time.Millisecond,
		"1-02:03:04": 26*time.Hour + 3*time.Minute + 4*time.Second,
	}
	for input, want := range tests {
		got, err := parsePSCPUTime(input)
		if err != nil || got != want {
			t.Errorf("parsePSCPUTime(%q)=%s, %v; want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"1:60:00", "1:00:60", "-1:00", "invalid"} {
		if _, err := parsePSCPUTime(input); err == nil {
			t.Errorf("parsePSCPUTime(%q) unexpectedly succeeded", input)
		}
	}
}

func TestHostProfilerSamplePermitHonorsCallerCancellation(t *testing.T) {
	profiler := &hostProcessProfiler{
		source:       blockingHostSnapshotSource{},
		samplePermit: make(chan struct{}, 1),
		spans:        make(map[*hostProfileSpan]struct{}),
	}
	// Simulate a background sample that currently owns the single-flight permit.
	profiler.samplePermit <- struct{}{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	profiler.sample(ctx)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("sample waited %s after its caller was canceled", elapsed)
	}
	<-profiler.samplePermit
}
