package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Controller orchestrates the reconciliation loop: poll Vault, detect changes,
// discover annotated resources, and refresh affected resources.
type Controller struct {
	vault     VaultWatcher
	state     StateStore
	discovery Discovery
	refresher Refresher
	logger    *slog.Logger

	// Health tracking (guarded by mu).
	mu                      sync.Mutex
	lastSuccessfulReconcile time.Time
	ready                   bool
}

// NewController creates a Controller with the given dependencies.
func NewController(vault VaultWatcher, state StateStore, discovery Discovery, refresher Refresher, logger *slog.Logger) *Controller {
	return &Controller{
		vault:     vault,
		state:     state,
		discovery: discovery,
		refresher: refresher,
		logger:    logger,
	}
}

// IsAlive returns true if the last successful reconciliation was within the given threshold.
func (c *Controller) IsAlive(threshold time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSuccessfulReconcile.IsZero() {
		// Not yet reconciled — treat as alive during startup.
		return true
	}
	return time.Since(c.lastSuccessfulReconcile) < threshold
}

// IsReady returns true if the controller has completed at least one successful reconciliation.
func (c *Controller) IsReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

// Reconcile performs one reconciliation cycle:
//  1. Discover annotated resources to find which Vault paths are watched.
//  2. Poll Vault only for those watched paths (not all secrets).
//  3. Load stored state and detect changes.
//  4. If changes, refresh affected resources (deduplicated).
//  5. Save updated state, skipping paths with failed refreshes.
func (c *Controller) Reconcile(ctx context.Context) error {
	// Step 1: Discover annotated resources first to know which paths to poll.
	resources, err := c.discovery.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovering resources: %w", err)
	}
	c.logger.Info("discovered annotated resources", "count", len(resources))

	// Step 2: Extract unique vault paths from discovered resources.
	pathMap := BuildPathToResourceMap(resources)
	watchedPaths := make([]string, 0, len(pathMap))
	for p := range pathMap {
		watchedPaths = append(watchedPaths, p)
	}

	if len(watchedPaths) == 0 {
		c.logger.Info("no watched vault paths found, nothing to do")
		c.markSuccess()
		return nil
	}

	// Step 3: Poll Vault only for watched paths.
	currentVersions, err := c.vault.GetVersionsForPaths(ctx, watchedPaths)
	if err != nil {
		return fmt.Errorf("polling vault versions: %w", err)
	}
	c.logger.Info("polled vault versions", "paths", len(currentVersions))

	// Step 4: Load stored state.
	storedVersions, err := c.state.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading stored state: %w", err)
	}
	c.logger.Info("loaded stored state", "paths", len(storedVersions))

	// Step 5: Detect changes.
	changed := DetectChanges(storedVersions, currentVersions)

	// Step 6: If no changes, save current versions and return.
	if len(changed) == 0 {
		c.logger.Info("no vault secret changes detected")
		if err := c.state.Save(ctx, currentVersions); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
		c.markSuccess()
		return nil
	}

	// Log detected changes.
	for path, ver := range changed {
		oldVer := storedVersions[path]
		c.logger.Info("vault secret changed",
			"path", path,
			"oldVersion", oldVer,
			"newVersion", ver,
		)
	}

	// Step 7: For each changed path, refresh affected resources (deduplicated).
	// Track paths where at least one resource refresh failed.
	refreshed := make(map[string]bool)
	failedPaths := make(map[string]bool)
	for path := range changed {
		affected, ok := pathMap[path]
		if !ok {
			c.logger.Debug("no resources watching changed path", "path", path)
			continue
		}

		for _, resource := range affected {
			key := fmt.Sprintf("%s/%s/%s", resource.Kind, resource.Namespace, resource.Name)
			if refreshed[key] {
				c.logger.Debug("skipping already-refreshed resource",
					"key", key,
					"path", path,
				)
				continue
			}
			refreshed[key] = true

			if err := c.refresher.Refresh(ctx, resource); err != nil {
				c.logger.Error("failed to refresh resource",
					"kind", resource.Kind,
					"namespace", resource.Namespace,
					"name", resource.Name,
					"error", err,
				)
				failedPaths[path] = true
				continue
			}
			c.logger.Info("refreshed resource",
				"kind", resource.Kind,
				"namespace", resource.Namespace,
				"name", resource.Name,
			)
		}
	}

	// Step 8: Merge current versions into stored state, skipping failed paths
	// so they are retried on the next cycle.
	mergedVersions := make(map[string]int, len(currentVersions))
	for k, v := range storedVersions {
		mergedVersions[k] = v
	}
	for k, v := range currentVersions {
		if failedPaths[k] {
			c.logger.Warn("skipping state update for failed path",
				"path", k,
				"version", v,
			)
			continue
		}
		mergedVersions[k] = v
	}

	if err := c.state.Save(ctx, mergedVersions); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	c.markSuccess()
	return nil
}

// markSuccess records a successful reconciliation for health checks.
func (c *Controller) markSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSuccessfulReconcile = time.Now()
	c.ready = true
}
