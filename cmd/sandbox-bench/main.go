// Command sandbox-bench measures the controller lifecycle through the HTTP
// API, local Docker Engine events/stats, and read-only host process samples.
// It never starts Docker Desktop and it destroys every application it creates.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"easygo-agent/internal/sandboxapi"

	"github.com/google/uuid"
	dockerclient "github.com/moby/moby/client"
)

const (
	benchmarkTokenEnvironment      = "SANDBOX_CONTROLLER_TOKEN"
	benchmarkDockerHostEnvironment = "DOCKER_SOCKET_PATH"
	benchmarkNamespaceEnvironment  = "EASYGO_SANDBOX_NAMESPACE"

	pathColdCreate              = "cold_create"
	pathActiveReuse             = "active_reuse"
	pathStopStart               = "stop_start"
	pathContainerLossRecreate   = "container_loss_recreate"
	pathLifecycle               = "lifecycle"
	pathCleanup                 = "cleanup"
	defaultStatsSampleInterval  = 25 * time.Millisecond
	defaultEventCollectionGrace = 2 * time.Second
)

var benchmarkNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

type benchmarkConfig struct {
	BaseURL     string
	DockerHost  string
	Namespace   string
	Warmups     int
	Iterations  int
	Concurrency []int
	Workloads   []string
	OutputPath  string
}

// benchmarkSample is deliberately denormalized. A result consumer can retain
// the raw observation (including why a value is absent) without reverse
// engineering a summary.
type benchmarkSample struct {
	SampleID         string   `json:"sample_id"`
	Iteration        int      `json:"iteration"`
	Concurrency      int      `json:"concurrency"`
	Path             string   `json:"path"`
	Workload         string   `json:"workload,omitempty"`
	Phase            string   `json:"phase"`
	DurationMS       *float64 `json:"duration_ms"`
	Error            string   `json:"error,omitempty"`
	Observation      string   `json:"observation_method"`
	MissingReason    string   `json:"missing_reason,omitempty"`
	ContainerID      string   `json:"container_id,omitempty"`
	CPUTimeMS        *float64 `json:"cpu_time_ms,omitempty"`
	PeakMemoryBytes  *uint64  `json:"peak_memory_bytes,omitempty"`
	PeakPIDs         *uint64  `json:"peak_pids,omitempty"`
	BlockIOBytes     *uint64  `json:"block_io_bytes,omitempty"`
	StatsSamples     int      `json:"stats_samples,omitempty"`
	StatsObservation string   `json:"stats_observation_method,omitempty"`
	StatsMissing     string   `json:"stats_missing_reason,omitempty"`
	HostCPUTimeMS    *float64 `json:"host_cpu_time_ms,omitempty"`
	HostPeakRSSBytes *uint64  `json:"host_peak_rss_bytes,omitempty"`
	HostPeakPIDs     *uint64  `json:"host_peak_pids,omitempty"`
	HostStatsSamples int      `json:"host_stats_samples,omitempty"`
	HostObservation  string   `json:"host_observation_method"`
	HostMissing      string   `json:"host_missing_reason,omitempty"`
}

type distributionSummary struct {
	Observed int      `json:"observed"`
	Mean     *float64 `json:"mean"`
	P50      *float64 `json:"p50"`
	P95      *float64 `json:"p95"`
	P99      *float64 `json:"p99"`
	Max      *float64 `json:"max"`
}

type benchmarkSummary struct {
	Concurrency int                 `json:"concurrency"`
	Path        string              `json:"path"`
	Workload    string              `json:"workload,omitempty"`
	Phase       string              `json:"phase"`
	Samples     int                 `json:"samples"`
	Failures    int                 `json:"failures"`
	Missing     int                 `json:"missing"`
	ErrorRate   float64             `json:"error_rate"`
	DurationMS  distributionSummary `json:"duration_ms"`
	CPUTimeMS   distributionSummary `json:"cpu_time_ms"`
	MemoryBytes distributionSummary `json:"peak_memory_bytes"`
	PIDs        distributionSummary `json:"peak_pids"`
	BlockIO     distributionSummary `json:"block_io_bytes"`
	HostMissing int                 `json:"host_missing"`
	HostCPU     distributionSummary `json:"host_cpu_time_ms"`
	HostRSS     distributionSummary `json:"host_peak_rss_bytes"`
	HostPIDs    distributionSummary `json:"host_peak_pids"`
}

type benchmarkReport struct {
	SchemaVersion int                `json:"schema_version"`
	GeneratedAt   time.Time          `json:"generated_at"`
	BaseURL       string             `json:"base_url"`
	DockerHost    string             `json:"docker_host"`
	Namespace     string             `json:"namespace"`
	Warmups       int                `json:"warmups"`
	Iterations    int                `json:"iterations"`
	Concurrency   []int              `json:"concurrency"`
	Workloads     []string           `json:"workloads"`
	Paths         []string           `json:"paths"`
	ElapsedMS     float64            `json:"elapsed_ms"`
	TotalFailure  int                `json:"total_failures"`
	Limitations   []string           `json:"limitations"`
	Samples       []benchmarkSample  `json:"samples"`
	Summaries     []benchmarkSummary `json:"summaries"`
}

type controllerClient struct {
	api    sandboxapi.Client
	engine benchmarkObserver
}

type applicationEnvelope struct {
	Application struct {
		ID            string `json:"id"`
		ApplicationID string `json:"application_id"`
	} `json:"application"`
	ID              string  `json:"id"`
	ApplicationID   string  `json:"application_id"`
	QueueDurationMS float64 `json:"queue_duration_ms"`
}

// phaseObservation is returned by the Docker-side observer. Missing is not an
// error: some phase boundaries simply do not exist outside the Controller.
type phaseObservation struct {
	Phase         string
	DurationMS    *float64
	Error         string
	Observation   string
	MissingReason string
	ContainerID   string
	Host          hostObservation
}

type hostObservation struct {
	CPUTimeMS    *float64
	PeakRSSBytes *uint64
	PeakPIDs     *uint64
	Samples      int
	Method       string
	Missing      string
}

type observedCall struct {
	Phases       []phaseObservation
	RequestError error
	CPUTimeMS    *float64
	PeakMemory   *uint64
	PeakPIDs     *uint64
	BlockIOBytes *uint64
	StatsSamples int
	StatsMethod  string
	StatsMissing string
}

type hostObservedCall struct {
	RequestError error
	DurationMS   float64
	Host         hostObservation
}

// benchmarkObserver is intentionally much narrower than a generic Docker
// client. The production implementation can only observe or remove the one
// labelled sandbox container that belongs to the current application.
type benchmarkObserver interface {
	ObserveHostPhase(context.Context, func() error) hostObservedCall
	ObserveActivation(context.Context, string, bool, func() (float64, error)) observedCall
	ObserveStop(context.Context, string, func() error) observedCall
	ObserveExec(context.Context, string, func() error) observedCall
	RemoveApplicationContainer(context.Context, string) observedCall
	Close() error
}

type benchmarkObserverFactory func(context.Context, string, string) (benchmarkObserver, error)

var benchmarkCommands = map[string]string{
	"noop":   "true",
	"python": "mkdir -p .easygo-bench/python && printf 'print(\"easygo-python-ok\")\\n' > .easygo-bench/python/main.py && python3 -m py_compile .easygo-bench/python/main.py && python3 .easygo-bench/python/main.py",
	"go":     "mkdir -p .easygo-bench/go && printf 'module easygo-bench\\n\\ngo 1.25\\n' > .easygo-bench/go/go.mod && printf 'package main\\nimport \"fmt\"\\nfunc main(){fmt.Println(\"easygo-go-ok\")}\\n' > .easygo-bench/go/main.go && (cd .easygo-bench/go && go build -o app . && ./app)",
	"node":   "node -e 'console.log(\"easygo-node-ok\")'",
	"cpp":    "mkdir -p .easygo-bench/cpp && printf '#include <iostream>\\nint main(){std::cout << \"easygo-cpp-ok\\\\n\";}\\n' > .easygo-bench/cpp/main.cpp && c++ -O0 -o .easygo-bench/cpp/app .easygo-bench/cpp/main.cpp && .easygo-bench/cpp/app",
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "sandbox benchmark failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, output io.Writer) error {
	return runWithObserverFactory(ctx, args, getenv, output, newDockerBenchmarkObserver)
}

func runWithObserverFactory(ctx context.Context, args []string, getenv func(string) string, output io.Writer, factory benchmarkObserverFactory) error {
	cfg, err := parseBenchmarkConfig(args, getenv)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(getenv(benchmarkTokenEnvironment))
	if token == "" {
		return fmt.Errorf("%s is required", benchmarkTokenEnvironment)
	}
	observer, err := factory(ctx, cfg.DockerHost, cfg.Namespace)
	if err != nil {
		return fmt.Errorf("connect local Docker Engine observer: %w", err)
	}
	defer observer.Close()
	client := &controllerClient{
		api: sandboxapi.Client{
			BaseURL:      cfg.BaseURL,
			Token:        token,
			HTTP:         &http.Client{Timeout: 11 * time.Minute},
			MaxBodyBytes: sandboxapi.DefaultMaxBody,
		},
		engine: observer,
	}

	for warmup := 0; warmup < cfg.Warmups; warmup++ {
		warmupSamples := client.benchmarkLifecycle(ctx, 1, -(warmup + 1), cfg.Workloads)
		for _, sample := range warmupSamples {
			if sample.Error != "" {
				return fmt.Errorf("warmup %d %s/%s failed: %s", warmup+1, sample.Path, sample.Phase, sample.Error)
			}
		}
	}

	started := time.Now()
	var samplesMu sync.Mutex
	var samples []benchmarkSample
	for _, concurrency := range cfg.Concurrency {
		jobs := make(chan int)
		var workers sync.WaitGroup
		for worker := 0; worker < concurrency; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for iteration := range jobs {
					jobSamples := client.benchmarkLifecycle(ctx, concurrency, iteration, cfg.Workloads)
					samplesMu.Lock()
					samples = append(samples, jobSamples...)
					samplesMu.Unlock()
				}
			}()
		}
		for iteration := 0; iteration < cfg.Iterations; iteration++ {
			select {
			case jobs <- iteration:
			case <-ctx.Done():
				close(jobs)
				workers.Wait()
				return ctx.Err()
			}
		}
		close(jobs)
		workers.Wait()
	}

	report := makeBenchmarkReport(cfg, time.Since(started), samples)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	encoded = append(encoded, '\n')
	if cfg.OutputPath != "" {
		if err := os.WriteFile(cfg.OutputPath, encoded, 0o600); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if report.TotalFailure > 0 {
		return fmt.Errorf("%d benchmark phase observations failed", report.TotalFailure)
	}
	return nil
}

func parseBenchmarkConfig(args []string, getenv func(string) string) (benchmarkConfig, error) {
	set := flag.NewFlagSet("sandbox-bench", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	baseURL := set.String("base-url", "http://127.0.0.1:8787", "sandbox controller base URL")
	dockerHost := set.String("docker-host", environmentDefault(getenv, benchmarkDockerHostEnvironment, "", dockerclient.DefaultDockerHost), "local Docker Engine unix socket URL or absolute socket path")
	namespace := set.String("namespace", environmentDefault(getenv, benchmarkNamespaceEnvironment, "", "easygo-agent"), "managed sandbox Docker label namespace")
	warmups := set.Int("warmups", 10, "unrecorded full-lifecycle warmups")
	iterations := set.Int("iterations", 100, "total lifecycle samples for each concurrency")
	concurrencyRaw := set.String("concurrency", "1,3,6", "comma-separated worker counts")
	workloadsRaw := set.String("workloads", "noop,python,go,node,cpp", "comma-separated offline workloads")
	outputPath := set.String("output", "", "optional JSON report path")
	if err := set.Parse(args); err != nil {
		return benchmarkConfig{}, err
	}
	parsedURL, err := url.ParseRequestURI(strings.TrimRight(strings.TrimSpace(*baseURL), "/"))
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return benchmarkConfig{}, errors.New("base-url must be an absolute http or https URL")
	}
	normalizedDockerHost, err := normalizeLocalDockerHost(*dockerHost)
	if err != nil {
		return benchmarkConfig{}, err
	}
	normalizedNamespace := strings.TrimSpace(*namespace)
	if !benchmarkNamespacePattern.MatchString(normalizedNamespace) {
		return benchmarkConfig{}, errors.New("namespace must contain 1 to 63 letters, digits, dots, underscores, or hyphens")
	}
	if *iterations < 1 || *iterations > 10_000 {
		return benchmarkConfig{}, errors.New("iterations must be between 1 and 10000")
	}
	if *warmups < 0 || *warmups > 1_000 {
		return benchmarkConfig{}, errors.New("warmups must be between 0 and 1000")
	}
	concurrency, err := parsePositiveList(*concurrencyRaw, 64)
	if err != nil {
		return benchmarkConfig{}, fmt.Errorf("concurrency: %w", err)
	}
	workloads, err := parseWorkloads(*workloadsRaw)
	if err != nil {
		return benchmarkConfig{}, err
	}
	return benchmarkConfig{
		BaseURL:     strings.TrimRight(parsedURL.String(), "/"),
		DockerHost:  normalizedDockerHost,
		Namespace:   normalizedNamespace,
		Warmups:     *warmups,
		Iterations:  *iterations,
		Concurrency: concurrency,
		Workloads:   workloads,
		OutputPath:  strings.TrimSpace(*outputPath),
	}, nil
}

func environmentDefault(getenv func(string) string, primary, secondary, fallback string) string {
	for _, name := range []string{primary, secondary} {
		if name != "" {
			if value := strings.TrimSpace(getenv(name)); value != "" {
				return value
			}
		}
	}
	return fallback
}

func normalizeLocalDockerHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "/") {
		raw = "unix://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "unix" || parsed.Path == "" {
		return "", errors.New("docker-host must be a local unix socket URL or absolute socket path")
	}
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("docker-host must identify only a local unix socket")
	}
	return "unix://" + parsed.Path, nil
}

func parsePositiveList(raw string, maximum int) ([]int, error) {
	seen := make(map[int]struct{})
	var values []int
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 1 || value > maximum {
			return nil, fmt.Errorf("values must be integers between 1 and %d", maximum)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, errors.New("at least one value is required")
	}
	return values, nil
}

func parseWorkloads(raw string) ([]string, error) {
	seen := make(map[string]struct{})
	var values []string
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if _, ok := benchmarkCommands[value]; !ok {
			return nil, fmt.Errorf("unknown workload %q", value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, errors.New("at least one workload is required")
	}
	return values, nil
}

func (client *controllerClient) benchmarkLifecycle(ctx context.Context, concurrency, iteration int, workloads []string) (samples []benchmarkSample) {
	sampleID := uuid.NewString()
	sessionID := uuid.NewString()
	runID := uuid.NewString()
	var applicationID string
	destroyed := false
	defer func() {
		if applicationID != "" && !destroyed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			sample := client.measureHTTP(cleanupCtx, sampleID, iteration, concurrency, pathCleanup, "", "destroy", func() error {
				return client.request(cleanupCtx, http.MethodDelete, "/v1/applications/"+url.PathEscape(applicationID), sessionID, runID, nil, nil)
			})
			samples = append(samples, sample)
		}
	}()

	var application applicationEnvelope
	applySample := client.measureHTTP(ctx, sampleID, iteration, concurrency, pathLifecycle, "", "apply", func() error {
		return client.request(ctx, http.MethodPost, "/v1/applications", sessionID, runID, map[string]any{}, &application)
	})
	samples = append(samples, applySample)
	if applySample.Error != "" {
		return samples
	}
	applicationID = application.applicationID()
	if applicationID == "" {
		samples = append(samples, benchmarkSample{
			SampleID: sampleID, Iteration: iteration, Concurrency: concurrency,
			Path: pathLifecycle, Phase: "apply", Error: "controller response omitted application_id",
			Observation: "controller_http_response", MissingReason: "application_id_missing",
			HostObservation: "not_observed_duplicate_response_validation_sample",
			HostMissing:     "host metrics are attached to the preceding apply HTTP sample",
		})
		return samples
	}
	path := "/v1/applications/" + url.PathEscape(applicationID)

	cold := client.engine.ObserveActivation(ctx, applicationID, true, func() (float64, error) {
		var response applicationEnvelope
		err := client.request(ctx, http.MethodPost, path+"/create", sessionID, runID, map[string]any{}, &response)
		return response.QueueDurationMS, err
	})
	samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathColdCreate, "", cold)
	if cold.RequestError != nil {
		return samples
	}

	for _, workload := range workloads {
		observation := client.observeWorkload(ctx, applicationID, path, sessionID, runID, workload)
		samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathActiveReuse, workload, observation)
		if observation.RequestError != nil {
			return samples
		}
	}

	stopped := client.engine.ObserveStop(ctx, applicationID, func() error {
		return client.request(ctx, http.MethodPost, path+"/release", sessionID, runID, map[string]any{}, &applicationEnvelope{})
	})
	samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathStopStart, "", stopped)
	if stopped.RequestError != nil {
		return samples
	}
	runID = uuid.NewString()
	resumed := client.engine.ObserveActivation(ctx, applicationID, false, func() (float64, error) {
		var response applicationEnvelope
		err := client.request(ctx, http.MethodPost, path+"/create", sessionID, runID, map[string]any{}, &response)
		return response.QueueDurationMS, err
	})
	samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathStopStart, "", resumed)
	if resumed.RequestError != nil {
		return samples
	}
	for _, workload := range workloads {
		observation := client.observeWorkload(ctx, applicationID, path, sessionID, runID, workload)
		samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathStopStart, workload, observation)
		if observation.RequestError != nil {
			return samples
		}
	}

	stopped = client.engine.ObserveStop(ctx, applicationID, func() error {
		return client.request(ctx, http.MethodPost, path+"/release", sessionID, runID, map[string]any{}, &applicationEnvelope{})
	})
	samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathContainerLossRecreate, "", stopped)
	if stopped.RequestError != nil {
		return samples
	}
	removed := client.engine.RemoveApplicationContainer(ctx, applicationID)
	samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathContainerLossRecreate, "", removed)
	if removed.RequestError != nil {
		return samples
	}
	runID = uuid.NewString()
	recreated := client.engine.ObserveActivation(ctx, applicationID, true, func() (float64, error) {
		var response applicationEnvelope
		err := client.request(ctx, http.MethodPost, path+"/create", sessionID, runID, map[string]any{}, &response)
		return response.QueueDurationMS, err
	})
	samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathContainerLossRecreate, "", recreated)
	if recreated.RequestError != nil {
		return samples
	}
	for _, workload := range workloads {
		observation := client.observeWorkload(ctx, applicationID, path, sessionID, runID, workload)
		samples = appendObservedSamples(samples, sampleID, iteration, concurrency, pathContainerLossRecreate, workload, observation)
		if observation.RequestError != nil {
			return samples
		}
	}

	destroySample := client.measureHTTP(ctx, sampleID, iteration, concurrency, pathLifecycle, "", "destroy", func() error {
		return client.request(ctx, http.MethodDelete, path, sessionID, runID, nil, nil)
	})
	samples = append(samples, destroySample)
	if destroySample.Error == "" {
		destroyed = true
	}
	return samples
}

func (client *controllerClient) observeWorkload(ctx context.Context, applicationID, path, sessionID, runID, workload string) observedCall {
	return client.engine.ObserveExec(ctx, applicationID, func() error {
		var result struct {
			ExitCode int `json:"exit_code"`
		}
		if err := client.request(ctx, http.MethodPost, path+"/exec", sessionID, runID, map[string]any{
			"command":         benchmarkCommands[workload],
			"cwd":             "/workspace",
			"timeout_seconds": 600,
		}, &result); err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("exit code %d", result.ExitCode)
		}
		return nil
	})
}

func appendObservedSamples(samples []benchmarkSample, sampleID string, iteration, concurrency int, path, workload string, call observedCall) []benchmarkSample {
	for _, phase := range call.Phases {
		host := normalizedHostObservation(phase.Host)
		sample := benchmarkSample{
			SampleID: sampleID, Iteration: iteration, Concurrency: concurrency,
			Path: path, Workload: workload, Phase: phase.Phase,
			DurationMS: phase.DurationMS, Error: phase.Error, Observation: phase.Observation,
			MissingReason: phase.MissingReason, ContainerID: phase.ContainerID,
			HostCPUTimeMS: host.CPUTimeMS, HostPeakRSSBytes: host.PeakRSSBytes,
			HostPeakPIDs: host.PeakPIDs, HostStatsSamples: host.Samples,
			HostObservation: host.Method, HostMissing: host.Missing,
		}
		if phase.Phase == "exec" {
			sample.CPUTimeMS = call.CPUTimeMS
			sample.PeakMemoryBytes = call.PeakMemory
			sample.PeakPIDs = call.PeakPIDs
			sample.BlockIOBytes = call.BlockIOBytes
			sample.StatsSamples = call.StatsSamples
			sample.StatsObservation = call.StatsMethod
			sample.StatsMissing = call.StatsMissing
		}
		samples = append(samples, sample)
	}
	return samples
}

func (client *controllerClient) measureHTTP(ctx context.Context, sampleID string, iteration, concurrency int, path, workload, phase string, call func() error) benchmarkSample {
	observed := client.engine.ObserveHostPhase(ctx, call)
	duration := observed.DurationMS
	host := normalizedHostObservation(observed.Host)
	sample := benchmarkSample{
		SampleID: sampleID, Iteration: iteration, Concurrency: concurrency,
		Path: path, Workload: workload, Phase: phase, DurationMS: &duration,
		Observation:   "controller_http_round_trip",
		HostCPUTimeMS: host.CPUTimeMS, HostPeakRSSBytes: host.PeakRSSBytes,
		HostPeakPIDs: host.PeakPIDs, HostStatsSamples: host.Samples,
		HostObservation: host.Method, HostMissing: host.Missing,
	}
	if observed.RequestError != nil {
		sample.Error = observed.RequestError.Error()
	}
	return sample
}

func normalizedHostObservation(observation hostObservation) hostObservation {
	if observation.Method == "" {
		observation.Method = "host_process_samples_unavailable"
	}
	if observation.CPUTimeMS == nil && observation.PeakRSSBytes == nil && observation.PeakPIDs == nil && observation.Missing == "" {
		observation.Missing = "host profiler returned no observation"
	}
	return observation
}

func (client *controllerClient) request(ctx context.Context, method, path, sessionID, runID string, body any, output any) error {
	return client.api.Do(ctx, method, path, sessionID, runID, body, output)
}

func (application applicationEnvelope) applicationID() string {
	for _, candidate := range []string{application.Application.ApplicationID, application.Application.ID, application.ApplicationID, application.ID} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func makeBenchmarkReport(cfg benchmarkConfig, elapsed time.Duration, samples []benchmarkSample) benchmarkReport {
	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].Concurrency != samples[j].Concurrency {
			return samples[i].Concurrency < samples[j].Concurrency
		}
		if samples[i].Iteration != samples[j].Iteration {
			return samples[i].Iteration < samples[j].Iteration
		}
		if samples[i].SampleID != samples[j].SampleID {
			return samples[i].SampleID < samples[j].SampleID
		}
		if samples[i].Path != samples[j].Path {
			return samples[i].Path < samples[j].Path
		}
		if samples[i].Workload != samples[j].Workload {
			return samples[i].Workload < samples[j].Workload
		}
		return samples[i].Phase < samples[j].Phase
	})
	type key struct {
		concurrency int
		path        string
		workload    string
		phase       string
	}
	groups := make(map[key][]benchmarkSample)
	failures := 0
	for _, sample := range samples {
		groupKey := key{sample.Concurrency, sample.Path, sample.Workload, sample.Phase}
		groups[groupKey] = append(groups[groupKey], sample)
		if sample.Error != "" {
			failures++
		}
	}
	var summaries []benchmarkSummary
	for groupKey, group := range groups {
		groupFailures := 0
		missing := 0
		var durations, cpu, memory, pids, blockIO, hostCPU, hostRSS, hostPIDs []float64
		hostMissing := 0
		for _, sample := range group {
			if sample.Error != "" {
				groupFailures++
			}
			if sample.DurationMS == nil {
				missing++
			} else {
				durations = append(durations, *sample.DurationMS)
			}
			if sample.CPUTimeMS != nil {
				cpu = append(cpu, *sample.CPUTimeMS)
			}
			if sample.PeakMemoryBytes != nil {
				memory = append(memory, float64(*sample.PeakMemoryBytes))
			}
			if sample.PeakPIDs != nil {
				pids = append(pids, float64(*sample.PeakPIDs))
			}
			if sample.BlockIOBytes != nil {
				blockIO = append(blockIO, float64(*sample.BlockIOBytes))
			}
			if sample.HostCPUTimeMS != nil {
				hostCPU = append(hostCPU, *sample.HostCPUTimeMS)
			}
			if sample.HostPeakRSSBytes != nil {
				hostRSS = append(hostRSS, float64(*sample.HostPeakRSSBytes))
			}
			if sample.HostPeakPIDs != nil {
				hostPIDs = append(hostPIDs, float64(*sample.HostPeakPIDs))
			}
			if sample.HostCPUTimeMS == nil || sample.HostPeakRSSBytes == nil || sample.HostPeakPIDs == nil {
				hostMissing++
			}
		}
		summaries = append(summaries, benchmarkSummary{
			Concurrency: groupKey.concurrency,
			Path:        groupKey.path,
			Workload:    groupKey.workload,
			Phase:       groupKey.phase,
			Samples:     len(group),
			Failures:    groupFailures,
			Missing:     missing,
			ErrorRate:   rounded(float64(groupFailures) / float64(len(group))),
			DurationMS:  summarizeDistribution(durations),
			CPUTimeMS:   summarizeDistribution(cpu),
			MemoryBytes: summarizeDistribution(memory),
			PIDs:        summarizeDistribution(pids),
			BlockIO:     summarizeDistribution(blockIO),
			HostMissing: hostMissing,
			HostCPU:     summarizeDistribution(hostCPU),
			HostRSS:     summarizeDistribution(hostRSS),
			HostPIDs:    summarizeDistribution(hostPIDs),
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Concurrency != summaries[j].Concurrency {
			return summaries[i].Concurrency < summaries[j].Concurrency
		}
		if summaries[i].Path != summaries[j].Path {
			return summaries[i].Path < summaries[j].Path
		}
		if summaries[i].Workload != summaries[j].Workload {
			return summaries[i].Workload < summaries[j].Workload
		}
		return summaries[i].Phase < summaries[j].Phase
	})
	return benchmarkReport{
		SchemaVersion: 4,
		GeneratedAt:   time.Now().UTC(),
		BaseURL:       cfg.BaseURL,
		DockerHost:    cfg.DockerHost,
		Namespace:     cfg.Namespace,
		Warmups:       cfg.Warmups,
		Iterations:    cfg.Iterations,
		Concurrency:   append([]int(nil), cfg.Concurrency...),
		Workloads:     append([]string(nil), cfg.Workloads...),
		Paths:         []string{pathActiveReuse, pathStopStart, pathContainerLossRecreate},
		ElapsedMS:     milliseconds(elapsed),
		TotalFailure:  failures,
		Limitations: []string{
			"queue latency is measured inside the Controller from capacity-admission enqueue until a running slot is granted, including any LRU stop required to free that slot",
			"cold create is an upper bound from HTTP request start to local receipt of the Docker create event and includes pre-admission checks plus event-delivery time; queue is also reported separately from the Controller",
			"apply and destroy durations cover only their Controller HTTP calls; synchronous host-profiler boundary sampling is excluded",
			"a release after capacity LRU already hibernated its container has no Docker stop event and is reported as a controller HTTP fallback; observation_method distinguishes it from an observed stop/die event",
			"Docker daemon/Desktop backend host CPU time, RSS, and matched PID count are sampled from the local host process table for lifecycle phases; unavailable discovery is omitted with host_missing_reason, never encoded as zero",
			"macOS host discovery includes com.apple.Virtualization.VirtualMachine as a heuristic for the Docker Desktop VM; another Apple virtualization workload may be included when it runs concurrently",
			"host samples use the nearest observations bracketing each phase boundary; phases shorter than the 25ms interval can share overlapping sample windows, so phase host CPU values must not be added together",
			"host process metrics are machine-wide Docker backend activity; concurrent benchmark phases and unrelated Docker workloads cannot be attributed to an individual application",
			"CPU time is a container-cgroup delta and includes small supervisor/controller-cleanup overhead during the exec window",
			"peak memory and peak PIDs are maxima of sampled cgroup stats and may undercount spikes shorter than the sampling interval",
		},
		Samples:   samples,
		Summaries: summaries,
	}
}

func summarizeDistribution(values []float64) distributionSummary {
	if len(values) == 0 {
		return distributionSummary{}
	}
	sort.Float64s(values)
	var sum float64
	for _, value := range values {
		sum += value
	}
	mean := rounded(sum / float64(len(values)))
	p50 := rounded(percentile(values, 0.50))
	p95 := rounded(percentile(values, 0.95))
	p99 := rounded(percentile(values, 0.99))
	maxValue := rounded(values[len(values)-1])
	return distributionSummary{Observed: len(values), Mean: &mean, P50: &p50, P95: &p95, P99: &p99, Max: &maxValue}
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * quantile)
	return sorted[index]
}

func milliseconds(duration time.Duration) float64 {
	return rounded(float64(duration) / float64(time.Millisecond))
}

func rounded(value float64) float64 {
	parsed, _ := strconv.ParseFloat(fmt.Sprintf("%.3f", value), 64)
	return parsed
}
