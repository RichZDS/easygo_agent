package sandbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// BootstrapManager opens the controller's exclusive state store and restores
// its Manager. Once the store is open, a restore failure means this process is
// the only controller that can own the namespace. In that case all managed
// containers are removed with an independent cleanup deadline so interrupted
// compute cannot survive a corrupt record. Named workspace volumes are
// deliberately left intact for operator recovery.
//
// An OpenBoltStore failure never reaches emergency cleanup: in particular, a
// lock timeout can mean another healthy controller is still serving requests.
func BootstrapManager(_ context.Context, cfg Config, engine Engine, clock Clock) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sandbox config: %w", err)
	}
	if engine == nil {
		return nil, errors.New("sandbox engine is required")
	}
	store, err := OpenBoltStore(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	manager, restoreErr := NewManager(cfg, engine, store, clock)
	if restoreErr == nil {
		return manager, nil
	}

	cleanupErr := withCleanupTimeout(func(cleanupContext context.Context) error {
		return removeManagedContainersForRecovery(cleanupContext, engine, cfg.Namespace)
	})
	closeErr := store.Close()
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("emergency cleanup after sandbox state restore failure: %w", cleanupErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close sandbox state after restore failure: %w", closeErr)
	}
	return nil, errors.Join(restoreErr, cleanupErr, closeErr)
}

func removeManagedContainersForRecovery(ctx context.Context, engine Engine, namespace string) error {
	resources, err := engine.ListManaged(ctx, namespace)
	if err != nil {
		return fmt.Errorf("list managed Docker resources: %w", err)
	}
	sort.Slice(resources.Containers, func(i, j int) bool {
		return resources.Containers[i].ID < resources.Containers[j].ID
	})
	var result error
	for _, container := range resources.Containers {
		// DockerEngine.ListManaged applies these filters server-side. Recheck the
		// labels at the destructive boundary so a faulty Engine implementation
		// cannot widen emergency cleanup beyond the configured namespace.
		if container.Labels[LabelManaged] != "true" || container.Labels[LabelNamespace] != namespace {
			result = errors.Join(result, fmt.Errorf("refusing emergency removal of container %q outside namespace %q", container.ID, namespace))
			continue
		}
		if err := engine.RemoveContainer(ctx, container.ID); err != nil {
			result = errors.Join(result, fmt.Errorf("remove managed container %q: %w", container.ID, err))
		}
	}
	return result
}
