package sandbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Reap enforces idle hibernation and the absolute hard deadline. A hard-expired
// busy application is canceled first and then synchronously destroyed.
func (manager *Manager) Reap(ctx context.Context) error {
	now := manager.clock.Now()
	manager.mu.Lock()
	for id, application := range manager.applications {
		if !now.Before(application.HardExpiresAt) {
			if cancel := manager.execCancels[id]; cancel != nil {
				cancel()
			}
		}
	}
	manager.mu.Unlock()
	// Hard deadlines are safety-critical. Deliver cancellation before any
	// namespace-wide Docker enumeration, which can be slow when the daemon is
	// under pressure. Resource deletion remains compensating and retryable.
	result := manager.reapManagedOrphans(ctx)

	// Re-snapshot after reconciliation: it can adopt an interrupted running
	// container and transition an application to Starting, which this same reap
	// pass must then stop instead of deferring to a later interval.
	now = manager.clock.Now()
	manager.mu.Lock()
	var expired, idle, interrupted, destroying []string
	for id, application := range manager.applications {
		if !now.Before(application.HardExpiresAt) {
			expired = append(expired, id)
			if cancel := manager.execCancels[id]; cancel != nil {
				cancel()
			}
			continue
		}
		if application.State == StateDestroying || application.State == StateDestroyed {
			destroying = append(destroying, id)
			continue
		}
		if (application.State == StateActive || application.State == StateWarmIdle) && !application.IdleExpiresAt.IsZero() && !now.Before(application.IdleExpiresAt) {
			idle = append(idle, id)
			continue
		}
		if application.State == StateStarting || application.State == StateHibernating || application.State == StateBusy && manager.execCancels[id] == nil {
			interrupted = append(interrupted, id)
		}
	}
	manager.mu.Unlock()
	sort.Strings(expired)
	sort.Strings(idle)
	sort.Strings(interrupted)
	sort.Strings(destroying)
	for _, id := range expired {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		if application == nil || operation == nil {
			continue
		}
		if err := manager.lockOperation(ctx, operation); err != nil {
			result = errors.Join(result, err)
			continue
		}
		manager.mu.Lock()
		application = manager.applications[id]
		stillExpired := application != nil && !manager.clock.Now().Before(application.HardExpiresAt)
		manager.mu.Unlock()
		if stillExpired {
			result = errors.Join(result, manager.destroyWhileLockedOperation(ctx, application))
		}
		manager.unlockOperation(operation)
	}
	for _, id := range idle {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		if application == nil || operation == nil || !operation.TryLock() {
			continue
		}
		manager.mu.Lock()
		application = manager.applications[id]
		stillIdle := application != nil && (application.State == StateActive || application.State == StateWarmIdle) && !application.IdleExpiresAt.IsZero() && !manager.clock.Now().Before(application.IdleExpiresAt)
		manager.mu.Unlock()
		if stillIdle {
			result = errors.Join(result, manager.hibernate(ctx, application))
		}
		manager.unlockOperation(operation)
	}
	for _, id := range interrupted {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		if application == nil || operation == nil || !operation.TryLock() {
			continue
		}
		manager.mu.Lock()
		application = manager.applications[id]
		stillInterrupted := application != nil && (application.State == StateStarting || application.State == StateHibernating || application.State == StateBusy && manager.execCancels[id] == nil)
		manager.mu.Unlock()
		if stillInterrupted {
			result = errors.Join(result, manager.hibernate(ctx, application))
		}
		manager.unlockOperation(operation)
	}
	for _, id := range destroying {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		if application == nil || operation == nil || !operation.TryLock() {
			continue
		}
		manager.mu.Lock()
		application = manager.applications[id]
		stillDestroying := application != nil && (application.State == StateDestroying || application.State == StateDestroyed)
		manager.mu.Unlock()
		if stillDestroying {
			result = errors.Join(result, manager.destroyWhileLockedOperation(ctx, application))
		}
		manager.unlockOperation(operation)
	}
	return result
}

// RunReaper runs until ctx is canceled. Errors are returned so the controller
// can fail loudly instead of silently abandoning TTL enforcement.
func (manager *Manager) RunReaper(ctx context.Context) error {
	overdueAttempted := false
	for {
		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return nil
		}
		delay := manager.nextReapDelayLocked()
		changed := manager.changed
		manager.mu.Unlock()
		if delay <= 0 && !overdueAttempted {
			if err := manager.Reap(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			overdueAttempted = true
			continue
		}
		if delay <= 0 {
			// A due idle workspace can be temporarily locked by a request that is
			// already doing Docker I/O. Unlock/state transitions signal changed, so
			// this is only a bounded fallback—not a high-rate Docker polling loop.
			delay = min(manager.cfg.ReaperInterval, time.Second)
		} else {
			overdueAttempted = false
		}
		timer := manager.clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-changed:
			// Application creation, renewal, or a state transition may introduce
			// an earlier deadline. Recompute instead of waiting on a stale ticker.
			timer.Stop()
			overdueAttempted = false
			continue
		case <-timer.Channel():
			if err := manager.Reap(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			overdueAttempted = true
		}
	}
}

func (manager *Manager) nextReapDelayLocked() time.Duration {
	now := manager.clock.Now()
	delay := manager.cfg.ReaperInterval
	for _, application := range manager.applications {
		if application.State == StateDestroyed {
			continue
		}
		remaining := application.HardExpiresAt.Sub(now)
		if remaining < delay {
			delay = remaining
		}
		if (application.State == StateActive || application.State == StateWarmIdle) && !application.IdleExpiresAt.IsZero() {
			remaining = application.IdleExpiresAt.Sub(now)
			if remaining < delay {
				delay = remaining
			}
		}
	}
	return delay
}

// Reconcile makes bbolt state and Docker labels agree after a controller
// restart. Every inherited running container is stopped before requests are
// accepted, so an interrupted exec can never survive a restart.
func (manager *Manager) Reconcile(ctx context.Context) error {
	resources, err := manager.engine.ListManaged(ctx, manager.cfg.Namespace)
	if err != nil {
		return internalError("list managed Docker resources", err)
	}
	volumesByApplication := make(map[string][]ManagedVolume)
	volumesByName := make(map[string]ManagedVolume)
	for _, volume := range resources.Volumes {
		volumesByApplication[volume.Labels[applicationLabel]] = append(volumesByApplication[volume.Labels[applicationLabel]], volume)
		volumesByName[volume.Name] = volume
	}
	// Containers are disposable compute state. Removing every inherited one
	// guarantees the next activation uses the current prevalidated image and
	// security template; only the named workspace volume is recovered.
	for _, inherited := range resources.Containers {
		if err := manager.engine.RemoveContainer(ctx, inherited.ID); err != nil {
			return internalError("remove inherited sandbox container", err)
		}
	}

	manager.mu.Lock()
	ids := make([]string, 0, len(manager.applications))
	known := make(map[string]struct{}, len(manager.applications))
	for id := range manager.applications {
		ids = append(ids, id)
		known[id] = struct{}{}
	}
	manager.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		operation.Lock()
		expectedVolumeLabels := manager.labelsFor(application, "volume")
		if candidate, exists := volumesByName[application.VolumeName]; exists && !sameOwnershipLabels(candidate.Labels, expectedVolumeLabels) {
			operation.Unlock()
			return internalError("workspace volume has unexpected ownership labels; refusing destructive recovery", nil)
		}
		if application.State == StateDestroying || application.State == StateDestroyed || !manager.clock.Now().Before(application.HardExpiresAt) {
			if err := manager.destroyWhileLockedOperation(ctx, application); err != nil {
				operation.Unlock()
				return err
			}
			operation.Unlock()
			delete(known, id)
			continue
		}

		volumes := volumesByApplication[id]
		keptVolume := false
		for _, candidate := range volumes {
			if candidate.Name == application.VolumeName && sameOwnershipLabels(candidate.Labels, expectedVolumeLabels) && !keptVolume {
				keptVolume = true
				continue
			}
			if err := manager.engine.RemoveVolume(ctx, candidate.Name); err != nil {
				operation.Unlock()
				return internalError("remove duplicate workspace volume", err)
			}
		}
		if !keptVolume {
			if _, err := manager.engine.CreateVolume(ctx, application.VolumeName, expectedVolumeLabels); err != nil {
				operation.Unlock()
				return internalError("restore missing workspace volume", err)
			}
			application.WorkspacePreserved = false
		}
		application.ContainerID = ""
		if len(application.SeenRunIDs) > 0 {
			application.State = StateHibernated
		} else {
			application.State = StateApplied
		}
		application.IdleExpiresAt = time.Time{}
		manager.mu.Lock()
		persistErr := manager.persistLocked(application)
		manager.signalLocked()
		manager.mu.Unlock()
		operation.Unlock()
		if persistErr != nil {
			return persistErr
		}
	}

	for id, volumes := range volumesByApplication {
		if _, exists := known[id]; exists {
			continue
		}
		for _, volume := range volumes {
			if err := manager.engine.RemoveVolume(ctx, volume.Name); err != nil {
				return internalError("remove orphan workspace volume", err)
			}
		}
	}
	// Image validation is deliberately last: compensating cleanup must still run
	// when an operator has not yet loaded the replacement runtime image.
	if err := manager.engine.ValidateRuntimeImage(ctx, manager.cfg.Image); err != nil {
		return internalError("validate prebuilt sandbox runtime image", err)
	}
	return nil
}

// Cleanup removes every resource in this namespace, including orphan labels.
// It is intended for tests and explicit operator cleanup, never normal shutdown.
func (manager *Manager) Cleanup(ctx context.Context) error {
	manager.mu.Lock()
	ids := make([]string, 0, len(manager.applications))
	for id := range manager.applications {
		ids = append(ids, id)
		if cancel := manager.execCancels[id]; cancel != nil {
			cancel()
		}
	}
	manager.mu.Unlock()
	var result error
	for _, id := range ids {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		if application == nil || operation == nil {
			continue
		}
		if err := manager.lockOperation(ctx, operation); err != nil {
			return errors.Join(result, err)
		}
		result = errors.Join(result, manager.destroyWhileLockedOperation(ctx, application))
		manager.unlockOperation(operation)
	}
	resources, err := manager.engine.ListManaged(ctx, manager.cfg.Namespace)
	if err != nil {
		return errors.Join(result, internalError("list resources during cleanup", err))
	}
	for _, container := range resources.Containers {
		result = errors.Join(result, manager.engine.RemoveContainer(ctx, container.ID))
	}
	for _, volume := range resources.Volumes {
		result = errors.Join(result, manager.engine.RemoveVolume(ctx, volume.Name))
	}
	if result != nil {
		return fmt.Errorf("cleanup sandbox resources: %w", result)
	}
	return nil
}

// HibernateAll is the graceful-shutdown path: commands are canceled and
// compute stops, while every named workspace remains recoverable.
func (manager *Manager) HibernateAll(ctx context.Context) error {
	manager.mu.Lock()
	ids := make([]string, 0, len(manager.applications))
	for id := range manager.applications {
		ids = append(ids, id)
		if cancel := manager.execCancels[id]; cancel != nil {
			cancel()
		}
	}
	manager.mu.Unlock()
	sort.Strings(ids)
	var result error
	for _, id := range ids {
		manager.mu.Lock()
		application := manager.applications[id]
		operation := manager.operationMu[id]
		manager.mu.Unlock()
		if application == nil || operation == nil {
			continue
		}
		if err := manager.lockOperation(ctx, operation); err != nil {
			return errors.Join(result, err)
		}
		manager.mu.Lock()
		application = manager.applications[id]
		manager.mu.Unlock()
		if application != nil && application.State != StateDestroyed && application.State != StateDestroying {
			result = errors.Join(result, manager.hibernate(ctx, application))
		}
		manager.unlockOperation(operation)
	}
	return result
}

// lockOperation waits without making graceful shutdown hostage to an
// uncooperative Engine call. unlockOperation signals changed after releasing
// the mutex, which avoids a missed wake-up between TryLock and the select.
func (manager *Manager) lockOperation(ctx context.Context, operation *sync.Mutex) error {
	for {
		manager.mu.Lock()
		changed := manager.changed
		manager.mu.Unlock()
		if operation.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// reapManagedOrphans is the runtime counterpart of startup reconciliation.
// It removes resources that have no application record and adopts an exactly
// owned, deterministically named container left by an ambiguous create. An
// application operation lock always wins: resources in active use are skipped.
func (manager *Manager) reapManagedOrphans(ctx context.Context) error {
	resources, err := manager.engine.ListManaged(ctx, manager.cfg.Namespace)
	if err != nil {
		return internalError("list managed Docker resources during reap", err)
	}
	sort.Slice(resources.Containers, func(i, j int) bool { return resources.Containers[i].ID < resources.Containers[j].ID })
	var result error
	for _, resource := range resources.Containers {
		applicationID := resource.Labels[applicationLabel]
		manager.mu.Lock()
		application := manager.applications[applicationID]
		operation := manager.operationMu[applicationID]
		manager.mu.Unlock()
		if application == nil || operation == nil {
			if removeErr := manager.engine.RemoveContainer(ctx, resource.ID); removeErr != nil {
				// Do not proceed to volume deletion when a container that may mount
				// it could not be removed.
				return errors.Join(result, internalError("remove orphan sandbox container", removeErr))
			}
			continue
		}
		if !operation.TryLock() {
			continue
		}
		manager.mu.Lock()
		application = manager.applications[applicationID]
		manager.mu.Unlock()
		if application == nil {
			manager.unlockOperation(operation)
			if removeErr := manager.engine.RemoveContainer(ctx, resource.ID); removeErr != nil {
				return errors.Join(result, internalError("remove orphan sandbox container", removeErr))
			}
			continue
		}
		expectedLabels := manager.labelsFor(application, "container")
		expectedName := containerName(manager.cfg.Namespace, application.ID)
		if resource.Name != expectedName || !sameOwnershipLabels(resource.Labels, expectedLabels) {
			manager.unlockOperation(operation)
			if sameOwnershipLabels(resource.Labels, expectedLabels) {
				if removeErr := manager.engine.RemoveContainer(ctx, resource.ID); removeErr != nil {
					return errors.Join(result, internalError("remove duplicate sandbox container", removeErr))
				}
			} else {
				result = errors.Join(result, internalError("managed container has unexpected ownership labels; refusing destructive recovery", nil))
			}
			continue
		}

		manager.mu.Lock()
		recordedID := application.ContainerID
		manager.mu.Unlock()
		adopt := recordedID == ""
		if recordedID != "" && recordedID != resource.ID {
			recorded, inspectErr := manager.engine.InspectContainer(ctx, recordedID)
			if inspectErr != nil {
				manager.unlockOperation(operation)
				result = errors.Join(result, internalError("inspect recorded sandbox container during reap", inspectErr))
				continue
			}
			if manager.expectedContainer(application, recorded) {
				manager.unlockOperation(operation)
				if removeErr := manager.engine.RemoveContainer(ctx, resource.ID); removeErr != nil {
					return errors.Join(result, internalError("remove duplicate sandbox container", removeErr))
				}
				continue
			}
			adopt = true
		}
		manager.mu.Lock()
		state := application.State
		physicalStateMismatch := state != StateDestroying && state != StateDestroyed && (resource.Running && !isRunningState(state) || !resource.Running && isRunningState(state) && state != StateStarting)
		manager.mu.Unlock()
		if adopt || physicalStateMismatch {
			manager.mu.Lock()
			application.ContainerID = resource.ID
			if application.State != StateDestroying && application.State != StateDestroyed {
				if resource.Running {
					// A running container paired with a non-running durable state is
					// treated as an interrupted/ambiguous start. Keeping Starting until
					// the interrupted pass removes it prevents capacity under-counting.
					application.State = StateStarting
				} else if isRunningState(application.State) && application.State != StateStarting {
					if len(application.SeenRunIDs) == 0 {
						application.State = StateApplied
					} else {
						application.State = StateHibernated
					}
				}
			}
			persistErr := manager.persistLocked(application)
			manager.signalLocked()
			manager.mu.Unlock()
			result = errors.Join(result, persistErr)
		}
		manager.unlockOperation(operation)
	}

	manager.mu.Lock()
	applications := make(map[string]Application, len(manager.applications))
	for id, application := range manager.applications {
		applications[id] = cloneApplication(*application)
	}
	manager.mu.Unlock()
	for _, resource := range resources.Volumes {
		application, exists := applications[resource.Labels[applicationLabel]]
		if !exists {
			result = errors.Join(result, manager.engine.RemoveVolume(ctx, resource.Name))
			continue
		}
		expectedLabels := manager.labelsFor(&application, "volume")
		if resource.Name == application.VolumeName {
			if !sameOwnershipLabels(resource.Labels, expectedLabels) {
				result = errors.Join(result, internalError("workspace volume has unexpected ownership labels; refusing destructive recovery", nil))
			}
			continue
		}
		if sameOwnershipLabels(resource.Labels, expectedLabels) {
			result = errors.Join(result, manager.engine.RemoveVolume(ctx, resource.Name))
		} else {
			result = errors.Join(result, internalError("managed volume has unexpected ownership labels; refusing destructive recovery", nil))
		}
	}
	return result
}

func (manager *Manager) Close() error {
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return nil
	}
	manager.closed = true
	close(manager.changed)
	for _, cancel := range manager.execCancels {
		cancel()
	}
	manager.mu.Unlock()
	return manager.store.Close()
}
