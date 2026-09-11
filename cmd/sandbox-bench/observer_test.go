package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type cgroupWindowHostSource struct {
	cpuNanoseconds *atomic.Uint64
	memoryBytes    *atomic.Uint64
	pids           *atomic.Uint64
}

func (source cgroupWindowHostSource) Snapshot(context.Context) hostProcessSnapshot {
	source.cpuNanoseconds.Add(uint64(100 * time.Millisecond))
	source.memoryBytes.Store(900)
	source.pids.Store(9)
	time.Sleep(10 * time.Millisecond)
	source.memoryBytes.Store(10)
	source.pids.Store(1)
	return hostProcessSnapshot{
		At:     time.Now(),
		Method: "fake host source",
		Processes: map[int]hostProcessUsage{
			1: {CPUTime: time.Duration(source.cpuNanoseconds.Load()), RSSBytes: 1},
		},
	}
}

func TestObserveExecCgroupWindowExcludesHostBoundarySampling(t *testing.T) {
	var cpuNanoseconds, memoryBytes, pids atomic.Uint64
	memoryBytes.Store(10)
	pids.Store(1)
	profiler := &hostProcessProfiler{
		source: cgroupWindowHostSource{
			cpuNanoseconds: &cpuNanoseconds,
			memoryBytes:    &memoryBytes,
			pids:           &pids,
		},
		samplePermit: make(chan struct{}, 1),
		spans:        make(map[*hostProfileSpan]struct{}),
	}
	observer := &dockerBenchmarkObserver{
		statsInterval: time.Millisecond,
		host:          profiler,
		findContainerOverride: func(context.Context, string) (string, error) {
			return "container-1", nil
		},
		readStatsOverride: func(context.Context, string) containerStatsSnapshot {
			return containerStatsSnapshot{
				cpuNanoseconds: cpuNanoseconds.Load(),
				memoryBytes:    memoryBytes.Load(),
				pids:           pids.Load(),
			}
		},
	}

	observed := observer.ObserveExec(context.Background(), "app-1", func() error {
		cpuNanoseconds.Add(uint64(10 * time.Millisecond))
		memoryBytes.Store(100)
		pids.Store(3)
		time.Sleep(10 * time.Millisecond)
		memoryBytes.Store(10)
		pids.Store(1)
		return nil
	})

	if observed.CPUTimeMS == nil || *observed.CPUTimeMS != 10 {
		t.Fatalf("CPU delta=%v; host boundary work leaked into the exec cgroup window", pointerValue(observed.CPUTimeMS))
	}
	if observed.PeakMemory == nil || *observed.PeakMemory != 100 {
		t.Fatalf("peak memory=%v; host boundary work leaked into the exec cgroup window", pointerValue(observed.PeakMemory))
	}
	if observed.PeakPIDs == nil || *observed.PeakPIDs != 3 {
		t.Fatalf("peak PIDs=%v; host boundary work leaked into the exec cgroup window", pointerValue(observed.PeakPIDs))
	}
}

func TestStatsAccumulatorReportsCgroupDeltasAndSampledPeaks(t *testing.T) {
	accumulator := newStatsAccumulator()
	accumulator.add(containerStatsSnapshot{cpuNanoseconds: 1_000_000, memoryBytes: 100, pids: 1, blockIOBytes: 10})
	accumulator.add(containerStatsSnapshot{cpuNanoseconds: 2_500_000, memoryBytes: 900, pids: 7, blockIOBytes: 42})
	accumulator.add(containerStatsSnapshot{cpuNanoseconds: 3_000_000, memoryBytes: 400, pids: 2, blockIOBytes: 50})

	cpu, memory, pids, blockIO, samples, missing := accumulator.results()
	if cpu == nil || *cpu != 2 || memory == nil || *memory != 900 || pids == nil || *pids != 7 || blockIO == nil || *blockIO != 40 {
		t.Fatalf("cpu=%v memory=%v pids=%v blockIO=%v", pointerValue(cpu), pointerValue(memory), pointerValue(pids), pointerValue(blockIO))
	}
	if samples != 3 || missing != "" {
		t.Fatalf("samples=%d missing=%q", samples, missing)
	}
}

func TestStatsAccumulatorDoesNotInventCPUFromOneSample(t *testing.T) {
	accumulator := newStatsAccumulator()
	accumulator.add(containerStatsSnapshot{cpuNanoseconds: 10, memoryBytes: 20, pids: 1})
	accumulator.add(containerStatsSnapshot{err: errors.New("stats unavailable")})

	cpu, memory, pids, _, samples, missing := accumulator.results()
	if cpu != nil {
		t.Fatalf("invented CPU delta %v", *cpu)
	}
	if memory == nil || *memory != 20 || pids == nil || *pids != 1 || samples != 1 {
		t.Fatalf("memory=%v pids=%v samples=%d", pointerValue(memory), pointerValue(pids), samples)
	}
	if missing == "" {
		t.Fatal("missing reason should explain unavailable CPU and failed stats")
	}
}

func TestEventDurationsRejectClockSkewRatherThanClampOrInvent(t *testing.T) {
	started := time.Unix(100, 0)
	event := capturedDockerEvent{engineTime: started.Add(-time.Second), receivedAt: started.Add(time.Millisecond)}
	duration, missing := durationBetweenEventAndHost(event, started.Add(-2*time.Second))
	if duration != nil || missing == "" {
		t.Fatalf("duration=%v missing=%q", duration, missing)
	}

	duration, missing = durationBetweenHostAndEvent(started, event)
	if duration == nil || *duration != 1 || missing != "" {
		t.Fatalf("receipt boundary duration=%v missing=%q", pointerValue(duration), missing)
	}
}

func TestSummaryKeepsMissingQueueSeparateFromFailures(t *testing.T) {
	duration := 2.0
	report := makeBenchmarkReport(benchmarkConfig{
		BaseURL: "http://127.0.0.1:8787", DockerHost: "unix:///tmp/docker.sock", Namespace: "test",
		Iterations: 1, Concurrency: []int{1}, Workloads: []string{"noop"},
	}, time.Millisecond, []benchmarkSample{
		{Concurrency: 1, Path: pathColdCreate, Phase: "queue", MissingReason: "unobservable"},
		{Concurrency: 1, Path: pathColdCreate, Phase: "create", DurationMS: &duration, Error: "boom"},
	})
	var queue, create *benchmarkSummary
	for index := range report.Summaries {
		summary := &report.Summaries[index]
		switch summary.Phase {
		case "queue":
			queue = summary
		case "create":
			create = summary
		}
	}
	if queue == nil || queue.Missing != 1 || queue.Failures != 0 || queue.DurationMS.Observed != 0 {
		t.Fatalf("queue summary=%+v", queue)
	}
	if create == nil || create.Failures != 1 || create.ErrorRate != 1 || create.DurationMS.Observed != 1 {
		t.Fatalf("create summary=%+v", create)
	}
}

func TestReportKeepsUnavailableHostMetricsMissingInsteadOfZero(t *testing.T) {
	duration := 1.0
	report := makeBenchmarkReport(benchmarkConfig{
		BaseURL: "http://127.0.0.1:8787", DockerHost: "unix:///tmp/docker.sock", Namespace: "test",
		Iterations: 1, Concurrency: []int{1}, Workloads: []string{"noop"},
	}, time.Millisecond, []benchmarkSample{{
		Concurrency: 1, Path: pathColdCreate, Phase: "create", DurationMS: &duration,
		HostObservation: "macos_ps_docker_desktop_backend_and_virtualization_vm_heuristic_samples",
		HostMissing:     "no Docker Desktop backend process was discoverable",
	}})
	if len(report.Summaries) != 1 || report.Summaries[0].HostMissing != 1 || report.Summaries[0].HostCPU.Observed != 0 || report.Summaries[0].HostRSS.Observed != 0 || report.Summaries[0].HostPIDs.Observed != 0 {
		t.Fatalf("host summary=%+v", report.Summaries)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Samples []map[string]any `json:"samples"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if _, invented := raw.Samples[0]["host_cpu_time_ms"]; invented {
		t.Fatalf("unavailable host CPU was serialized as a value: %s", encoded)
	}
	if _, invented := raw.Samples[0]["host_peak_rss_bytes"]; invented {
		t.Fatalf("unavailable host RSS was serialized as a value: %s", encoded)
	}
	if _, invented := raw.Samples[0]["host_peak_pids"]; invented {
		t.Fatalf("unavailable host PIDs were serialized as a value: %s", encoded)
	}
	if raw.Samples[0]["host_missing_reason"] == "" || raw.Samples[0]["host_observation_method"] == "" {
		t.Fatalf("missing host metadata was omitted: %s", encoded)
	}
}

func pointerValue[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}
