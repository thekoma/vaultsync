package main

import (
	"context"
	"fmt"
	"log/slog"
)

// Controller orchestrates the reconciliation loop: poll Vault, detect changes,
// discover annotated resources, and refresh affected resources.
type Controller struct {
	vault     VaultWatcher
	state     StateStore
	discovery Discovery
	refresher Refresher
	logger    *slog.Logger
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

// Reconcile performs one reconciliation cycle:
//  1. Poll Vault for current versions.
//  2. Load stored state.
//  3. Detect changes.
//  4. If changes, discover annotated resources, build path→resource map,
//     and refresh affected resources (deduplicated).
//  5. Save updated state.
func (c *Controller) Reconcile(ctx context.Context) error {
	// Step 1: Poll Vault for current versions.
	currentVersions, err := c.vault.GetAllVersions(ctx)
	if err != nil {
		return fmt.Errorf("polling vault versions: %w", err)
	}
	c.logger.Info("polled vault versions", "paths", len(currentVersions))

	// Step 2: Load stored state.
	storedVersions, err := c.state.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading stored state: %w", err)
	}
	c.logger.Info("loaded stored state", "paths", len(storedVersions))

	// Step 3: Detect changes.
	changed := DetectChanges(storedVersions, currentVersions)

	// Step 4: If no changes, save current versions and return.
	if len(changed) == 0 {
		c.logger.Info("no vault secret changes detected")
		if err := c.state.Save(ctx, currentVersions); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
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

	// Step 5: Discover annotated resources.
	resources, err := c.discovery.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovering resources: %w", err)
	}
	c.logger.Info("discovered annotated resources", "count", len(resources))

	// Step 6: Build path→resource map.
	pathMap := BuildPathToResourceMap(resources)

	// Step 7: For each changed path, refresh affected resources (deduplicated).
	refreshed := make(map[string]bool)
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

			// Step 8: Log errors but continue.
			if err := c.refresher.Refresh(ctx, resource); err != nil {
				c.logger.Error("failed to refresh resource",
					"kind", resource.Kind,
					"namespace", resource.Namespace,
					"name", resource.Name,
					"error", err,
				)
				continue
			}
			c.logger.Info("refreshed resource",
				"kind", resource.Kind,
				"namespace", resource.Namespace,
				"name", resource.Name,
			)
		}
	}

	// Step 9: Save updated state with current versions.
	if err := c.state.Save(ctx, currentVersions); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}

	return nil
}
