package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const hostSnapshotTimeout = 500 * time.Millisecond

type hostProcessUsage struct {
	CPUTime  time.Duration
	RSSBytes uint64
}

type hostProcessSnapshot struct {
	At            time.Time
	Processes     map[int]hostProcessUsage
	Method        string
	MissingReason string
}

type hostSnapshotSource interface {
	Snapshot(context.Context) hostProcessSnapshot
}

type psCommandRunner func(context.Context, string, ...string) ([]byte, error)

// psHostSnapshotSource deliberately uses the host process table rather than
// Docker container stats. It never invokes Docker Desktop or daemon lifecycle
// commands. On hosts where the daemon processes are outside our PID namespace,
// discovery is reported as missing instead of manufacturing zeroes.
type psHostSnapshotSource struct {
	goos string
	now  func() time.Time
	run  psCommandRunner
}

func newPSHostSnapshotSource() *psHostSnapshotSource {
	return &psHostSnapshotSource{
		goos: runtime.GOOS,
		now:  time.Now,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

func (source *psHostSnapshotSource) Snapshot(ctx context.Context) hostProcessSnapshot {
	method := hostProcessObservationMethod(source.goos)
	snapshot := hostProcessSnapshot{At: source.now(), Processes: make(map[int]hostProcessUsage), Method: method}
	if method == "unsupported_host_process_profiler" {
		snapshot.MissingReason = fmt.Sprintf("host process profiling is unsupported on %s", source.goos)
		return snapshot
	}
	output, err := source.run(ctx, "ps", "-axo", "pid=,ppid=,rss=,time=,command=")
	snapshot.At = source.now()
	if err != nil {
		snapshot.MissingReason = "read host process table: " + err.Error()
		return snapshot
	}
	parseErrors := parseHostProcesses(source.goos, output, snapshot.Processes)
	if len(snapshot.Processes) == 0 {
		if source.goos == "darwin" {
			snapshot.MissingReason = "no Docker Desktop backend process was discoverable in the current PID namespace"
		} else {
			snapshot.MissingReason = "no dockerd/containerd process was discoverable in the current PID namespace"
		}
	}
	if len(parseErrors) > 0 {
		if snapshot.MissingReason != "" {
			snapshot.MissingReason += "; "
		}
		snapshot.MissingReason += "some matching processes could not be parsed: " + strings.Join(uniqueStrings(parseErrors), "; ")
	}
	return snapshot
}

func hostProcessObservationMethod(goos string) string {
	switch goos {
	case "darwin":
		return "macos_ps_docker_desktop_backend_and_virtualization_vm_heuristic_samples"
	case "linux":
		return "linux_ps_dockerd_containerd_samples"
	default:
		return "unsupported_host_process_profiler"
	}
}

func parseHostProcesses(goos string, output []byte, processes map[int]hostProcessUsage) []string {
	var parseErrors []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		command := strings.Join(fields[4:], " ")
		if !isDockerHostProcess(goos, command) {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		rssKiB, rssErr := strconv.ParseUint(fields[2], 10, 64)
		cpuTime, cpuErr := parsePSCPUTime(fields[3])
		if pidErr != nil || pid <= 0 || rssErr != nil || rssKiB > math.MaxUint64/1024 || cpuErr != nil {
			parseErrors = append(parseErrors, strings.TrimSpace(scanner.Text()))
			continue
		}
		processes[pid] = hostProcessUsage{CPUTime: cpuTime, RSSBytes: rssKiB * 1024}
	}
	if err := scanner.Err(); err != nil {
		parseErrors = append(parseErrors, err.Error())
	}
	return parseErrors
}

func isDockerHostProcess(goos, command string) bool {
	lower := strings.ToLower(command)
	if goos == "darwin" {
		for _, marker := range []string{
			"com.docker.backend",
			"com.docker.virtualization",
			"com.docker.vmm",
			"com.docker.hyperkit",
			"com.docker.driver.",
			"com.apple.virtualization.virtualmachine",
			"/vpnkit",
		} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
		return false
	}
	if goos != "linux" {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	name := strings.Trim(filepath.Base(fields[0]), "[]")
	return name == "dockerd" || name == "containerd" || name == "docker-proxy" || strings.HasPrefix(name, "containerd-shim")
}

func parsePSCPUTime(raw string) (time.Duration, error) {
	var days int64
	clock := raw
	if before, after, found := strings.Cut(raw, "-"); found {
		value, err := strconv.ParseInt(before, 10, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("invalid ps CPU days %q", raw)
		}
		days, clock = value, after
	}
	parts := strings.Split(clock, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid ps CPU time %q", raw)
	}
	minutesMayOverflow := len(parts) == 2
	var hours, minutes int64
	var secondsRaw string
	if len(parts) == 3 {
		var err error
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid ps CPU time %q", raw)
		}
		minutes, secondsRaw = parseInt64(parts[1]), parts[2]
	} else {
		minutes, secondsRaw = parseInt64(parts[0]), parts[1]
	}
	seconds, err := strconv.ParseFloat(secondsRaw, 64)
	// macOS ps uses [[dd-]hh:]mm:ss and leaves the leading hour field out
	// until needed. Consequently the two-field form can legitimately report
	// more than 59 minutes (for example 67:30.03). In the three-field form the
	// middle field is a conventional minute-of-hour and must remain below 60.
	if hours < 0 || minutes < 0 || !minutesMayOverflow && minutes >= 60 || err != nil || seconds < 0 || seconds >= 60 {
		return 0, fmt.Errorf("invalid ps CPU time %q", raw)
	}
	totalSeconds := float64(days*24*60*60+hours*60*60+minutes*60) + seconds
	if totalSeconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("ps CPU time overflows duration: %q", raw)
	}
	return time.Duration(totalSeconds * float64(time.Second)), nil
}

func parseInt64(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1
	}
	return value
}

type hostProcessProfiler struct {
	ctx      context.Context
	cancel   context.CancelFunc
	source   hostSnapshotSource
	interval time.Duration

	samplePermit chan struct{}
	mu           sync.Mutex
	latest       hostProcessSnapshot
	spans        map[*hostProfileSpan]struct{}
	closed       bool
	wg           sync.WaitGroup
}

type hostProfileSpan struct {
	profiler *hostProcessProfiler
	samples  []hostProcessSnapshot
	finished bool
}

type hostProfileTrace struct {
	Samples []hostProcessSnapshot
}

func newHostProcessProfiler(parent context.Context, interval time.Duration, source hostSnapshotSource) *hostProcessProfiler {
	if interval <= 0 {
		interval = defaultStatsSampleInterval
	}
	ctx, cancel := context.WithCancel(parent)
	profiler := &hostProcessProfiler{
		ctx: ctx, cancel: cancel, source: source, interval: interval,
		spans: make(map[*hostProfileSpan]struct{}), samplePermit: make(chan struct{}, 1),
	}
	profiler.sampleWithTimeout(ctx)
	profiler.wg.Add(1)
	go profiler.run()
	return profiler
}

func (profiler *hostProcessProfiler) run() {
	defer profiler.wg.Done()
	timer := time.NewTimer(profiler.interval)
	defer timer.Stop()
	for {
		select {
		case <-profiler.ctx.Done():
			return
		case <-timer.C:
			profiler.mu.Lock()
			active := len(profiler.spans) > 0
			profiler.mu.Unlock()
			if active {
				profiler.sampleWithTimeout(profiler.ctx)
			}
			// Schedule from completion, not from the prior nominal tick. `ps` can
			// take longer than the sample interval; a Ticker would otherwise stay
			// permanently ready and starve phase-boundary samples.
			timer.Reset(profiler.interval)
		}
	}
}

func (profiler *hostProcessProfiler) Begin(ctx context.Context) *hostProfileSpan {
	profiler.sampleWithTimeout(ctx)
	span := &hostProfileSpan{profiler: profiler}
	profiler.mu.Lock()
	if !profiler.latest.At.IsZero() {
		span.samples = append(span.samples, cloneHostSnapshot(profiler.latest))
	}
	if !profiler.closed {
		profiler.spans[span] = struct{}{}
	}
	profiler.mu.Unlock()
	return span
}

func (span *hostProfileSpan) Finish() hostProfileTrace {
	if span == nil || span.profiler == nil {
		return hostProfileTrace{}
	}
	profiler := span.profiler
	profiler.sampleWithTimeout(context.Background())
	profiler.mu.Lock()
	if !span.finished {
		delete(profiler.spans, span)
		span.finished = true
	}
	samples := make([]hostProcessSnapshot, len(span.samples))
	for index := range span.samples {
		samples[index] = cloneHostSnapshot(span.samples[index])
	}
	profiler.mu.Unlock()
	return hostProfileTrace{Samples: samples}
}

func (profiler *hostProcessProfiler) sampleWithTimeout(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, hostSnapshotTimeout)
	defer cancel()
	profiler.sample(ctx)
}

func (profiler *hostProcessProfiler) sample(ctx context.Context) {
	select {
	case profiler.samplePermit <- struct{}{}:
		defer func() { <-profiler.samplePermit }()
	case <-ctx.Done():
		return
	}
	if profiler.source == nil {
		return
	}
	snapshot := profiler.source.Snapshot(ctx)
	profiler.mu.Lock()
	if profiler.closed {
		profiler.mu.Unlock()
		return
	}
	profiler.latest = cloneHostSnapshot(snapshot)
	for span := range profiler.spans {
		span.samples = append(span.samples, cloneHostSnapshot(snapshot))
	}
	profiler.mu.Unlock()
}

func (profiler *hostProcessProfiler) Close() error {
	profiler.mu.Lock()
	if profiler.closed {
		profiler.mu.Unlock()
		return nil
	}
	profiler.closed = true
	profiler.cancel()
	profiler.mu.Unlock()
	profiler.wg.Wait()
	return nil
}

func cloneHostSnapshot(snapshot hostProcessSnapshot) hostProcessSnapshot {
	copy := snapshot
	copy.Processes = make(map[int]hostProcessUsage, len(snapshot.Processes))
	for pid, usage := range snapshot.Processes {
		copy.Processes[pid] = usage
	}
	return copy
}

func (trace hostProfileTrace) Observe(start, end time.Time) hostObservation {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return missingHostObservation("host phase boundaries were unavailable or inconsistent")
	}
	samples := trace.windowSamples(start, end)
	methods := make([]string, 0, len(samples))
	missing := make([]string, 0, len(samples)+1)
	valid := make([]hostProcessSnapshot, 0, len(samples))
	for _, snapshot := range samples {
		if snapshot.Method != "" {
			methods = append(methods, snapshot.Method)
		}
		if snapshot.MissingReason != "" {
			missing = append(missing, snapshot.MissingReason)
		}
		if len(snapshot.Processes) > 0 && snapshot.MissingReason == "" {
			valid = append(valid, snapshot)
		}
	}
	method := strings.Join(uniqueStrings(methods), "; ")
	if method == "" {
		method = "host_process_samples_unavailable"
	}
	if len(valid) == 0 {
		if len(missing) == 0 {
			missing = append(missing, "no Docker host process samples were available for this phase")
		}
		return hostObservation{Method: method, Missing: strings.Join(uniqueStrings(missing), "; ")}
	}

	var peakRSS, peakPIDs uint64
	for _, snapshot := range valid {
		var rss uint64
		for _, process := range snapshot.Processes {
			if rss > math.MaxUint64-process.RSSBytes {
				rss = math.MaxUint64
				break
			}
			rss += process.RSSBytes
		}
		if rss > peakRSS {
			peakRSS = rss
		}
		if count := uint64(len(snapshot.Processes)); count > peakPIDs {
			peakPIDs = count
		}
	}
	observation := hostObservation{
		PeakRSSBytes: &peakRSS,
		PeakPIDs:     &peakPIDs,
		Samples:      len(valid),
		Method:       method,
	}
	if len(valid) < 2 {
		missing = append(missing, "host CPU delta requires at least two Docker process samples")
		observation.Missing = strings.Join(uniqueStrings(missing), "; ")
		return observation
	}

	var cpuDelta time.Duration
	cpuReliable := true
	for index := 1; index < len(valid); index++ {
		previous := valid[index-1].Processes
		current := valid[index].Processes
		for pid, usage := range current {
			before, existed := previous[pid]
			if !existed {
				cpuDelta += usage.CPUTime
				continue
			}
			if usage.CPUTime < before.CPUTime {
				missing = append(missing, fmt.Sprintf("host CPU counter moved backwards for PID %d", pid))
				cpuReliable = false
				continue
			}
			cpuDelta += usage.CPUTime - before.CPUTime
		}
		for pid := range previous {
			if _, exists := current[pid]; !exists {
				missing = append(missing, fmt.Sprintf("Docker host PID %d exited between samples; CPU may be undercounted", pid))
				cpuReliable = false
			}
		}
	}
	if cpuReliable {
		cpuMS := rounded(float64(cpuDelta) / float64(time.Millisecond))
		observation.CPUTimeMS = &cpuMS
	}
	observation.Missing = strings.Join(uniqueStrings(missing), "; ")
	return observation
}

func (trace hostProfileTrace) windowSamples(start, end time.Time) []hostProcessSnapshot {
	sorted := make([]hostProcessSnapshot, len(trace.Samples))
	copy(sorted, trace.Samples)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	var before *hostProcessSnapshot
	var after *hostProcessSnapshot
	var result []hostProcessSnapshot
	for index := range sorted {
		snapshot := sorted[index]
		if !snapshot.At.After(start) {
			copy := snapshot
			before = &copy
		}
		if !snapshot.At.Before(start) && !snapshot.At.After(end) {
			result = append(result, snapshot)
		}
		if snapshot.At.After(end) && after == nil {
			copy := snapshot
			after = &copy
		}
	}
	if before != nil && (len(result) == 0 || !result[0].At.Equal(before.At)) {
		result = append([]hostProcessSnapshot{*before}, result...)
	}
	if after != nil && (len(result) == 0 || !result[len(result)-1].At.Equal(after.At)) {
		result = append(result, *after)
	}
	return result
}

func missingHostObservation(reason string) hostObservation {
	return hostObservation{Method: "host_process_samples_unavailable", Missing: reason}
}
