package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	dockerclient "github.com/moby/moby/client"
)

const (
	labelManaged       = "io.easygo.sandbox.managed"
	labelNamespace     = "io.easygo.sandbox.namespace"
	labelApplicationID = "io.easygo.sandbox.application_id"
	labelResource      = "io.easygo.sandbox.resource"
)

type dockerBenchmarkObserver struct {
	client        *dockerclient.Client
	namespace     string
	statsInterval time.Duration
	eventGrace    time.Duration
	host          *hostProcessProfiler

	findContainerOverride func(context.Context, string) (string, error)
	readStatsOverride     func(context.Context, string) containerStatsSnapshot
}

func newDockerBenchmarkObserver(ctx context.Context, host, namespace string) (benchmarkObserver, error) {
	client, err := dockerclient.New(dockerclient.WithHost(host), dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	if _, err := client.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true}); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping %s: %w (the benchmark never starts Docker Desktop)", host, err)
	}
	return &dockerBenchmarkObserver{
		client:        client,
		namespace:     namespace,
		statsInterval: defaultStatsSampleInterval,
		eventGrace:    defaultEventCollectionGrace,
		host:          newHostProcessProfiler(ctx, defaultStatsSampleInterval, newPSHostSnapshotSource()),
	}, nil
}

func (observer *dockerBenchmarkObserver) Close() error {
	var hostErr error
	if observer.host != nil {
		hostErr = observer.host.Close()
	}
	return errors.Join(hostErr, observer.client.Close())
}

func (observer *dockerBenchmarkObserver) ObserveHostPhase(ctx context.Context, call func() error) hostObservedCall {
	span := observer.beginHostProfile(ctx)
	started := time.Now()
	requestErr := call()
	finished := time.Now()
	trace := span.Finish()
	return hostObservedCall{
		RequestError: requestErr,
		DurationMS:   milliseconds(finished.Sub(started)),
		Host:         trace.Observe(started, finished),
	}
}

func (observer *dockerBenchmarkObserver) beginHostProfile(ctx context.Context) *hostProfileSpan {
	if observer.host == nil {
		return nil
	}
	return observer.host.Begin(ctx)
}

type capturedDockerEvent struct {
	action      string
	containerID string
	engineTime  time.Time
	receivedAt  time.Time
}

type dockerEventCapture struct {
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}

	mu     sync.Mutex
	events []capturedDockerEvent
	err    error
}

func (observer *dockerBenchmarkObserver) captureEvents(ctx context.Context, applicationID string) *dockerEventCapture {
	captureCtx, cancel := context.WithCancel(ctx)
	capture := &dockerEventCapture{cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	filters := make(dockerclient.Filters).
		Add("type", "container").
		Add("label", labelManaged+"=true").
		Add("label", labelNamespace+"="+observer.namespace).
		Add("label", labelApplicationID+"="+applicationID).
		Add("label", labelResource+"=container")
	result := observer.client.Events(captureCtx, dockerclient.EventsListOptions{Filters: filters})
	go func() {
		defer close(capture.done)
		messages := result.Messages
		errorsChannel := result.Err
		for messages != nil || errorsChannel != nil {
			select {
			case message, ok := <-messages:
				if !ok {
					messages = nil
					continue
				}
				capture.addEvent(message)
			case err, ok := <-errorsChannel:
				if !ok {
					errorsChannel = nil
					continue
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					capture.setError(err)
				}
			case <-captureCtx.Done():
				return
			}
		}
	}()
	return capture
}

func (capture *dockerEventCapture) addEvent(message events.Message) {
	receivedAt := time.Now()
	var engineTime time.Time
	if message.TimeNano > 0 {
		engineTime = time.Unix(0, message.TimeNano)
	} else if message.Time > 0 {
		engineTime = time.Unix(message.Time, 0)
	}
	capture.mu.Lock()
	capture.events = append(capture.events, capturedDockerEvent{
		action: string(message.Action), containerID: message.Actor.ID,
		engineTime: engineTime, receivedAt: receivedAt,
	})
	capture.mu.Unlock()
	select {
	case capture.wake <- struct{}{}:
	default:
	}
}

func (capture *dockerEventCapture) setError(err error) {
	capture.mu.Lock()
	if capture.err == nil {
		capture.err = err
	}
	capture.mu.Unlock()
	select {
	case capture.wake <- struct{}{}:
	default:
	}
}

func (capture *dockerEventCapture) close() {
	capture.cancel()
	<-capture.done
}

func (capture *dockerEventCapture) waitFor(ctx context.Context, timeout time.Duration, actions ...string) (capturedDockerEvent, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		capture.mu.Lock()
		for _, action := range actions {
			for _, event := range capture.events {
				if event.action == action {
					capture.mu.Unlock()
					return event, nil
				}
			}
		}
		captureErr := capture.err
		capture.mu.Unlock()
		if captureErr != nil {
			return capturedDockerEvent{}, captureErr
		}
		select {
		case <-ctx.Done():
			return capturedDockerEvent{}, ctx.Err()
		case <-deadline.C:
			return capturedDockerEvent{}, fmt.Errorf("Docker events %s not observed within %s", strings.Join(actions, "/"), timeout)
		case <-capture.wake:
		}
	}
}

func (observer *dockerBenchmarkObserver) ObserveActivation(ctx context.Context, applicationID string, expectCreate bool, call func() (float64, error)) observedCall {
	capture := observer.captureEvents(ctx, applicationID)
	defer capture.close()
	hostSpan := observer.beginHostProfile(ctx)
	requestStarted := time.Now()
	queueDurationMS, requestErr := call()
	requestFinished := time.Now()
	result := observedCall{RequestError: requestErr}
	queueDurationMS = rounded(queueDurationMS)
	queuePhase := phaseObservation{
		Phase:       "queue",
		DurationMS:  &queueDurationMS,
		Observation: "controller_admission_entry_to_slot_grant",
		Host:        missingHostObservation("queue is a controller admission phase, not a distinct Docker host lifecycle phase"),
	}
	if requestErr != nil {
		queuePhase.DurationMS = nil
		queuePhase.Error = requestErr.Error()
		queuePhase.Observation = "controller_request_failed_before_queue_measurement"
		queuePhase.MissingReason = requestErr.Error()
	}
	result.Phases = append(result.Phases, queuePhase)
	if requestErr != nil {
		trace := hostSpan.Finish()
		return activationRequestFailure(result, requestErr, expectCreate, trace, requestStarted, requestFinished)
	}

	var createEvent capturedDockerEvent
	var createErr error
	if expectCreate {
		createEvent, createErr = capture.waitFor(ctx, observer.eventGrace, string(events.ActionCreate))
		result.Phases = append(result.Phases, eventPhaseFromRequest(
			"create", requestStarted, createEvent, createErr,
			"http_request_start_to_docker_create_event_receipt_upper_bound_including_queue_and_event_delivery",
		))
	} else {
		result.Phases = append(result.Phases, phaseObservation{
			Phase: "create", Observation: "not_applicable_existing_container",
			MissingReason: "stop/start resumes the existing container and does not create one",
		})
	}
	startEvent, startErr := capture.waitFor(ctx, observer.eventGrace, string(events.ActionStart))
	startPhase := phaseObservation{Phase: "start", ContainerID: startEvent.containerID}
	if startErr != nil {
		startPhase.Error = startErr.Error()
		startPhase.Observation = "docker_start_event_missing"
		startPhase.MissingReason = startErr.Error()
	} else if expectCreate && createErr == nil {
		startPhase.DurationMS, startPhase.MissingReason = durationBetweenEventTimes(createEvent, startEvent)
		if !createEvent.engineTime.IsZero() && !startEvent.engineTime.IsZero() {
			startPhase.Observation = "docker_create_event_to_start_event_time_nano"
		} else {
			startPhase.Observation = "docker_create_event_to_start_event_receipt_delta_fallback"
		}
	} else {
		startPhase.DurationMS, startPhase.MissingReason = durationBetweenHostAndEvent(requestStarted, startEvent)
		startPhase.Observation = "http_request_start_to_docker_start_event_receipt_upper_bound_including_event_delivery"
	}
	result.Phases = append(result.Phases, startPhase)
	readyPhase := phaseObservation{Phase: "ready", ContainerID: startEvent.containerID, Observation: "docker_start_event_time_nano_to_controller_http_response"}
	if startErr != nil {
		readyPhase.Error = startErr.Error()
		readyPhase.MissingReason = startErr.Error()
	} else {
		readyPhase.DurationMS, readyPhase.MissingReason = durationBetweenEventAndHost(startEvent, requestFinished)
	}
	result.Phases = append(result.Phases, readyPhase)
	trace := hostSpan.Finish()
	if expectCreate && createErr == nil {
		result.Phases[1].Host = trace.Observe(requestStarted, createEvent.receivedAt)
	} else {
		result.Phases[1].Host = missingHostObservation("container create phase was not applicable or its Docker event boundary was unavailable")
	}
	if startErr == nil {
		startBoundary := requestStarted
		if expectCreate && createErr == nil {
			startBoundary = createEvent.receivedAt
		}
		result.Phases[2].Host = trace.Observe(startBoundary, startEvent.receivedAt)
		result.Phases[3].Host = trace.Observe(startEvent.receivedAt, requestFinished)
	} else {
		result.Phases[2].Host = missingHostObservation("Docker start event boundary was unavailable: " + startErr.Error())
		result.Phases[3].Host = missingHostObservation("Docker start event boundary was unavailable: " + startErr.Error())
	}
	return result
}

func activationRequestFailure(result observedCall, requestErr error, expectCreate bool, trace hostProfileTrace, started, finished time.Time) observedCall {
	observedFailure := trace.Observe(started, finished)
	if expectCreate {
		result.Phases = append(result.Phases, phaseObservation{Phase: "create", Error: requestErr.Error(), Observation: "controller_request_failed", Host: observedFailure})
	} else {
		result.Phases = append(result.Phases, phaseObservation{
			Phase: "create", Error: requestErr.Error(), Observation: "not_applicable_existing_container",
			MissingReason: "stop/start resumes the existing container and does not create one",
			Host:          missingHostObservation("container create phase was not applicable"),
		})
	}
	startHost := observedFailure
	if expectCreate {
		startHost = missingHostObservation("failed activation did not expose separate create/start host boundaries")
	}
	result.Phases = append(result.Phases,
		phaseObservation{Phase: "start", Error: requestErr.Error(), Observation: "controller_request_failed", Host: startHost},
		phaseObservation{Phase: "ready", Error: requestErr.Error(), Observation: "controller_request_failed", Host: missingHostObservation("failed activation did not expose a ready host boundary")},
	)
	return result
}

func (observer *dockerBenchmarkObserver) ObserveStop(ctx context.Context, applicationID string, call func() error) observedCall {
	capture := observer.captureEvents(ctx, applicationID)
	defer capture.close()
	hostSpan := observer.beginHostProfile(ctx)
	started := time.Now()
	requestErr := call()
	finished := time.Now()
	result := observedCall{RequestError: requestErr}
	phase := phaseObservation{Phase: "stop"}
	if requestErr != nil {
		phase.Error = requestErr.Error()
		phase.Observation = "controller_request_failed"
		phase.Host = hostSpan.Finish().Observe(started, finished)
		result.Phases = []phaseObservation{phase}
		return result
	}
	stopEvent, eventErr := capture.waitFor(ctx, observer.eventGrace, string(events.ActionStop), string(events.ActionDie))
	if eventErr == nil {
		phase.DurationMS, phase.MissingReason = durationBetweenHostAndEvent(started, stopEvent)
		phase.Observation = "http_request_start_to_docker_" + stopEvent.action + "_event_receipt_upper_bound"
		phase.ContainerID = stopEvent.containerID
	} else {
		// The HTTP round trip is a truthful fallback because release does not
		// return until Docker's stop call has completed.
		duration := milliseconds(finished.Sub(started))
		phase.DurationMS = &duration
		phase.Observation = "controller_release_http_round_trip_fallback"
		phase.MissingReason = "Docker stop/die event unavailable: " + eventErr.Error()
	}
	trace := hostSpan.Finish()
	hostFinished := finished
	if eventErr == nil {
		hostFinished = stopEvent.receivedAt
	}
	phase.Host = trace.Observe(started, hostFinished)
	result.Phases = []phaseObservation{phase}
	return result
}

func (observer *dockerBenchmarkObserver) ObserveExec(ctx context.Context, applicationID string, call func() error) observedCall {
	containerID, findErr := observer.findApplicationContainer(ctx, applicationID)
	// Host-boundary samples run outside the cgroup window so profiler Snapshot
	// work cannot leak into exec CPU/memory/PID peaks.
	hostSpan := observer.beginHostProfile(ctx)
	started := time.Now()
	collector := newStatsAccumulator()
	if findErr == nil {
		collector.add(observer.readStats(ctx, containerID))
	}
	pollCtx, cancelPoll := context.WithCancel(ctx)
	var poller sync.WaitGroup
	if findErr == nil {
		poller.Add(1)
		go func() {
			defer poller.Done()
			ticker := time.NewTicker(observer.statsInterval)
			defer ticker.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-ticker.C:
					snapshot := observer.readStats(pollCtx, containerID)
					if pollCtx.Err() != nil {
						return
					}
					collector.add(snapshot)
				}
			}
		}()
	}
	requestErr := call()
	finished := time.Now()
	cancelPoll()
	poller.Wait()
	if findErr == nil {
		postCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		collector.add(observer.readStats(postCtx, containerID))
		cancel()
	}
	hostTrace := hostSpan.Finish()
	duration := milliseconds(finished.Sub(started))
	phase := phaseObservation{
		Phase: "exec", DurationMS: &duration, Observation: "controller_exec_http_round_trip", ContainerID: containerID,
		Host: hostTrace.Observe(started, finished),
	}
	if requestErr != nil {
		phase.Error = requestErr.Error()
	}
	result := observedCall{Phases: []phaseObservation{phase}, RequestError: requestErr}
	if findErr != nil {
		result.StatsMissing = findErr.Error()
		return result
	}
	result.CPUTimeMS, result.PeakMemory, result.PeakPIDs, result.BlockIOBytes, result.StatsSamples, result.StatsMissing = collector.results()
	result.StatsMethod = "Docker ContainerStats cgroup samples at 25ms plus pre/post samples; CPU and block I/O are cumulative deltas"
	return result
}

func (observer *dockerBenchmarkObserver) RemoveApplicationContainer(ctx context.Context, applicationID string) observedCall {
	containerID, err := observer.findApplicationContainer(ctx, applicationID)
	if err != nil {
		return observedCall{
			RequestError: err,
			Phases: []phaseObservation{{
				Phase: "remove", Error: err.Error(), Observation: "docker_label_lookup_failed",
				Host: missingHostObservation("container lookup failed before the remove host phase began"),
			}},
		}
	}
	capture := observer.captureEvents(ctx, applicationID)
	defer capture.close()
	hostSpan := observer.beginHostProfile(ctx)
	started := time.Now()
	_, removeErr := observer.client.ContainerRemove(ctx, containerID, dockerclient.ContainerRemoveOptions{Force: false, RemoveVolumes: false})
	finished := time.Now()
	phase := phaseObservation{Phase: "remove", ContainerID: containerID}
	if removeErr != nil {
		phase.Error = removeErr.Error()
		phase.Observation = "docker_container_remove_api_failed"
		phase.Host = hostSpan.Finish().Observe(started, finished)
		return observedCall{Phases: []phaseObservation{phase}, RequestError: removeErr}
	}
	destroyEvent, eventErr := capture.waitFor(ctx, observer.eventGrace, string(events.ActionDestroy))
	if eventErr == nil {
		phase.DurationMS, phase.MissingReason = durationBetweenHostAndEvent(started, destroyEvent)
		phase.Observation = "docker_remove_request_start_to_destroy_event_receipt_upper_bound"
	} else {
		duration := milliseconds(finished.Sub(started))
		phase.DurationMS = &duration
		phase.Observation = "docker_container_remove_api_round_trip_fallback"
		phase.MissingReason = "Docker destroy event unavailable: " + eventErr.Error()
	}
	trace := hostSpan.Finish()
	hostFinished := finished
	if eventErr == nil {
		hostFinished = destroyEvent.receivedAt
	}
	phase.Host = trace.Observe(started, hostFinished)
	return observedCall{Phases: []phaseObservation{phase}}
}

func (observer *dockerBenchmarkObserver) findApplicationContainer(ctx context.Context, applicationID string) (string, error) {
	if observer.findContainerOverride != nil {
		return observer.findContainerOverride(ctx, applicationID)
	}
	filters := make(dockerclient.Filters).
		Add("label", labelManaged+"=true").
		Add("label", labelNamespace+"="+observer.namespace).
		Add("label", labelApplicationID+"="+applicationID).
		Add("label", labelResource+"=container")
	listed, err := observer.client.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return "", fmt.Errorf("list application container: %w", err)
	}
	if len(listed.Items) != 1 {
		return "", fmt.Errorf("expected exactly one labelled container for application %s, found %d", applicationID, len(listed.Items))
	}
	candidate := listed.Items[0]
	if candidate.Labels[labelManaged] != "true" || candidate.Labels[labelNamespace] != observer.namespace || candidate.Labels[labelApplicationID] != applicationID || candidate.Labels[labelResource] != "container" {
		return "", errors.New("refusing container whose ownership labels do not exactly match")
	}
	return candidate.ID, nil
}

func eventPhaseFromRequest(phase string, started time.Time, event capturedDockerEvent, eventErr error, method string) phaseObservation {
	result := phaseObservation{Phase: phase, Observation: method, ContainerID: event.containerID}
	if eventErr != nil {
		result.Error = eventErr.Error()
		result.MissingReason = eventErr.Error()
		return result
	}
	result.DurationMS, result.MissingReason = durationBetweenHostAndEvent(started, event)
	return result
}

func durationBetweenEventTimes(first, second capturedDockerEvent) (*float64, string) {
	if !first.engineTime.IsZero() && !second.engineTime.IsZero() {
		return nonNegativeDuration(second.engineTime.Sub(first.engineTime))
	}
	if first.receivedAt.IsZero() || second.receivedAt.IsZero() {
		return nil, "Docker events omitted both a common Engine timestamp and a local receipt boundary"
	}
	return nonNegativeDuration(second.receivedAt.Sub(first.receivedAt))
}

func durationBetweenHostAndEvent(started time.Time, event capturedDockerEvent) (*float64, string) {
	if event.receivedAt.IsZero() {
		return nil, "Docker event receipt boundary was unavailable"
	}
	return nonNegativeDuration(event.receivedAt.Sub(started))
}

func durationBetweenEventAndHost(event capturedDockerEvent, finished time.Time) (*float64, string) {
	if event.engineTime.IsZero() {
		return nil, "Docker event omitted timeNano; ready boundary cannot be compared with the HTTP response"
	}
	return nonNegativeDuration(finished.Sub(event.engineTime))
}

func nonNegativeDuration(duration time.Duration) (*float64, string) {
	if duration < 0 {
		return nil, "host and Docker Engine event clocks were inconsistent; value omitted"
	}
	value := milliseconds(duration)
	return &value, ""
}

type containerStatsSnapshot struct {
	cpuNanoseconds uint64
	memoryBytes    uint64
	pids           uint64
	blockIOBytes   uint64
	err            error
}

func (observer *dockerBenchmarkObserver) readStats(ctx context.Context, containerID string) containerStatsSnapshot {
	if observer.readStatsOverride != nil {
		return observer.readStatsOverride(ctx, containerID)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	response, err := observer.client.ContainerStats(requestCtx, containerID, dockerclient.ContainerStatsOptions{Stream: false})
	if err != nil {
		return containerStatsSnapshot{err: err}
	}
	defer response.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return containerStatsSnapshot{err: err}
	}
	return containerStatsSnapshot{
		cpuNanoseconds: stats.CPUStats.CPUUsage.TotalUsage,
		memoryBytes:    stats.MemoryStats.Usage,
		pids:           stats.PidsStats.Current,
		blockIOBytes:   totalBlockIO(stats.BlkioStats.IoServiceBytesRecursive),
	}
}

func totalBlockIO(entries []container.BlkioStatEntry) uint64 {
	var total uint64
	for _, entry := range entries {
		if strings.EqualFold(entry.Op, "read") || strings.EqualFold(entry.Op, "write") {
			if ^uint64(0)-total < entry.Value {
				return ^uint64(0)
			}
			total += entry.Value
		}
	}
	return total
}

type statsAccumulator struct {
	mu        sync.Mutex
	snapshots []containerStatsSnapshot
	errors    []string
}

func newStatsAccumulator() *statsAccumulator { return &statsAccumulator{} }

func (accumulator *statsAccumulator) add(snapshot containerStatsSnapshot) {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if snapshot.err != nil {
		accumulator.errors = append(accumulator.errors, snapshot.err.Error())
		return
	}
	accumulator.snapshots = append(accumulator.snapshots, snapshot)
}

func (accumulator *statsAccumulator) results() (*float64, *uint64, *uint64, *uint64, int, string) {
	accumulator.mu.Lock()
	defer accumulator.mu.Unlock()
	if len(accumulator.snapshots) == 0 {
		return nil, nil, nil, nil, 0, "no Docker stats samples were available: " + strings.Join(uniqueStrings(accumulator.errors), "; ")
	}
	baseline := accumulator.snapshots[0]
	maxCPU := baseline.cpuNanoseconds
	maxMemory := baseline.memoryBytes
	maxPIDs := baseline.pids
	maxBlockIO := baseline.blockIOBytes
	for _, snapshot := range accumulator.snapshots[1:] {
		if snapshot.cpuNanoseconds > maxCPU {
			maxCPU = snapshot.cpuNanoseconds
		}
		if snapshot.memoryBytes > maxMemory {
			maxMemory = snapshot.memoryBytes
		}
		if snapshot.pids > maxPIDs {
			maxPIDs = snapshot.pids
		}
		if snapshot.blockIOBytes > maxBlockIO {
			maxBlockIO = snapshot.blockIOBytes
		}
	}
	peakMemory := maxMemory
	peakPIDs := maxPIDs
	var cpuMS *float64
	var blockIO *uint64
	missing := ""
	if len(accumulator.snapshots) >= 2 && maxCPU >= baseline.cpuNanoseconds {
		value := rounded(float64(maxCPU-baseline.cpuNanoseconds) / float64(time.Millisecond))
		cpuMS = &value
	} else {
		missing = "CPU delta needs at least two ordered Docker stats samples"
	}
	if len(accumulator.snapshots) >= 2 && maxBlockIO >= baseline.blockIOBytes {
		value := maxBlockIO - baseline.blockIOBytes
		blockIO = &value
	}
	if len(accumulator.errors) > 0 {
		if missing != "" {
			missing += "; "
		}
		missing += "some stats samples failed: " + strings.Join(uniqueStrings(accumulator.errors), "; ")
	}
	return cpuMS, &peakMemory, &peakPIDs, blockIO, len(accumulator.snapshots), missing
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
