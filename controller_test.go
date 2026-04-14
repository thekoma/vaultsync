package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"testing"
	"time"
)

func TestReconcile(t *testing.T) {
	// Vault has litellm at version 3 (changed from 2), wasabi-backup at 7, airvpn/qbittorrent at 4.
	vault := newFakeVaultWatcher(map[string]int{
		"litellm":             3,
		"wasabi-backup":       7,
		"airvpn/qbittorrent":  4,
	})

	// State has litellm at version 2, wasabi-backup at 7, airvpn/qbittorrent at 4.
	state := newFakeStateStore()
	state.versions = map[string]int{
		"litellm":             2,
		"wasabi-backup":       7,
		"airvpn/qbittorrent":  4,
	}

	// 3 resources:
	// - litellm-secret watches litellm
	// - volsync-config watches wasabi-backup
	// - qbittorrent app watches wasabi-backup + airvpn/qbittorrent
	discovery := newFakeDiscovery([]WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "litellm",
			Name:       "litellm-secret",
			VaultPaths: []string{"litellm"},
		},
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Namespace:  "volsync",
			Name:       "volsync-config",
			VaultPaths: []string{"wasabi-backup"},
		},
		{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Namespace:  "argocd",
			Name:       "qbittorrent",
			VaultPaths: []string{"wasabi-backup", "airvpn/qbittorrent"},
		},
	})

	refresher := &fakeRefresher{}
	logger := slog.Default()

	ctrl := NewController(vault, state, discovery, refresher, logger)
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Only litellm-secret should be refreshed (only litellm changed).
	if len(refresher.refreshed) != 1 {
		t.Fatalf("refreshed %d resources, want 1; got %+v", len(refresher.refreshed), refresher.refreshed)
	}
	if refresher.refreshed[0].Name != "litellm-secret" {
		t.Errorf("refreshed[0].Name = %q, want %q", refresher.refreshed[0].Name, "litellm-secret")
	}

	// State should be saved with updated versions.
	if !state.saved {
		t.Error("state was not saved")
	}
	if state.versions["litellm"] != 3 {
		t.Errorf("state[litellm] = %d, want 3", state.versions["litellm"])
	}
	if state.versions["wasabi-backup"] != 7 {
		t.Errorf("state[wasabi-backup] = %d, want 7", state.versions["wasabi-backup"])
	}
	if state.versions["airvpn/qbittorrent"] != 4 {
		t.Errorf("state[airvpn/qbittorrent] = %d, want 4", state.versions["airvpn/qbittorrent"])
	}
}

func TestReconcileMultipleResourcesForSamePath(t *testing.T) {
	// wasabi-backup changed from 7 to 8.
	vault := newFakeVaultWatcher(map[string]int{
		"wasabi-backup": 8,
	})

	state := newFakeStateStore()
	state.versions = map[string]int{
		"wasabi-backup": 7,
	}

	// 3 resources all watching wasabi-backup.
	discovery := newFakeDiscovery([]WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "volsync",
			Name:       "volsync-secret",
			VaultPaths: []string{"wasabi-backup"},
		},
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Namespace:  "volsync",
			Name:       "volsync-config",
			VaultPaths: []string{"wasabi-backup"},
		},
		{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Namespace:  "argocd",
			Name:       "wasabi-app",
			VaultPaths: []string{"wasabi-backup"},
		},
	})

	refresher := &fakeRefresher{}
	logger := slog.Default()

	ctrl := NewController(vault, state, discovery, refresher, logger)
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// All 3 resources should be refreshed.
	if len(refresher.refreshed) != 3 {
		t.Fatalf("refreshed %d resources, want 3; got %+v", len(refresher.refreshed), refresher.refreshed)
	}

	names := make([]string, len(refresher.refreshed))
	for i, r := range refresher.refreshed {
		names[i] = r.Name
	}
	sort.Strings(names)
	wantNames := []string{"volsync-config", "volsync-secret", "wasabi-app"}
	for i, want := range wantNames {
		if names[i] != want {
			t.Errorf("refreshed names[%d] = %q, want %q", i, names[i], want)
		}
	}

	// State should be saved with version 8.
	if !state.saved {
		t.Error("state was not saved")
	}
	if state.versions["wasabi-backup"] != 8 {
		t.Errorf("state[wasabi-backup] = %d, want 8", state.versions["wasabi-backup"])
	}
}

func TestReconcileNoChanges(t *testing.T) {
	// litellm at version 3 in both vault and state — no change.
	vault := newFakeVaultWatcher(map[string]int{
		"litellm": 3,
	})

	state := newFakeStateStore()
	state.versions = map[string]int{
		"litellm": 3,
	}

	// Even though there's a resource watching litellm, it should not be refreshed.
	discovery := newFakeDiscovery([]WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "litellm",
			Name:       "litellm-secret",
			VaultPaths: []string{"litellm"},
		},
	})

	refresher := &fakeRefresher{}
	logger := slog.Default()

	ctrl := NewController(vault, state, discovery, refresher, logger)
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// No resources should be refreshed.
	if len(refresher.refreshed) != 0 {
		t.Fatalf("refreshed %d resources, want 0; got %+v", len(refresher.refreshed), refresher.refreshed)
	}

	// State should still be saved with current versions.
	if !state.saved {
		t.Error("state was not saved")
	}
	if state.versions["litellm"] != 3 {
		t.Errorf("state[litellm] = %d, want 3", state.versions["litellm"])
	}
}

func TestReconcileDeduplication(t *testing.T) {
	// Both paths changed.
	vault := newFakeVaultWatcher(map[string]int{
		"wasabi-backup":      8,
		"airvpn/qbittorrent": 5,
	})

	state := newFakeStateStore()
	state.versions = map[string]int{
		"wasabi-backup":      7,
		"airvpn/qbittorrent": 4,
	}

	// Single resource watches both changed paths — should be refreshed only once.
	discovery := newFakeDiscovery([]WatchedResource{
		{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Namespace:  "argocd",
			Name:       "qbittorrent",
			VaultPaths: []string{"wasabi-backup", "airvpn/qbittorrent"},
		},
	})

	refresher := &fakeRefresher{}
	logger := slog.Default()

	ctrl := NewController(vault, state, discovery, refresher, logger)
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Should be refreshed exactly once despite two paths matching.
	if len(refresher.refreshed) != 1 {
		t.Fatalf("refreshed %d resources, want 1 (dedup); got %+v", len(refresher.refreshed), refresher.refreshed)
	}
	if refresher.refreshed[0].Name != "qbittorrent" {
		t.Errorf("refreshed[0].Name = %q, want %q", refresher.refreshed[0].Name, "qbittorrent")
	}
}

func TestReconcileRefreshErrorContinues(t *testing.T) {
	// wasabi-backup changed from 7 to 8, litellm unchanged.
	vault := newFakeVaultWatcher(map[string]int{
		"wasabi-backup": 8,
		"litellm":       3,
	})

	state := newFakeStateStore()
	state.versions = map[string]int{
		"wasabi-backup": 7,
		"litellm":       3,
	}

	// Resource watches wasabi-backup, but refresher will return an error.
	discovery := newFakeDiscovery([]WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "volsync",
			Name:       "volsync-secret",
			VaultPaths: []string{"wasabi-backup"},
		},
	})

	refresher := &fakeRefresher{err: fmt.Errorf("simulated refresh error")}
	logger := slog.Default()

	ctrl := NewController(vault, state, discovery, refresher, logger)
	err := ctrl.Reconcile(context.Background())

	// Reconcile should not return an error — it logs and continues.
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want nil (errors are logged, not returned)", err)
	}

	// State should still be saved despite refresh failure.
	if !state.saved {
		t.Error("state was not saved despite refresh error")
	}

	// wasabi-backup should retain old version 7 because its refresh failed.
	if state.versions["wasabi-backup"] != 7 {
		t.Errorf("state[wasabi-backup] = %d, want 7 (should not advance on failure)", state.versions["wasabi-backup"])
	}

	// litellm should remain at 3 (unchanged).
	if state.versions["litellm"] != 3 {
		t.Errorf("state[litellm] = %d, want 3", state.versions["litellm"])
	}
}

func TestControllerHealthTracking(t *testing.T) {
	vault := newFakeVaultWatcher(map[string]int{
		"litellm": 3,
	})
	state := newFakeStateStore()
	state.versions = map[string]int{
		"litellm": 3,
	}
	discovery := newFakeDiscovery([]WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "litellm",
			Name:       "litellm-secret",
			VaultPaths: []string{"litellm"},
		},
	})
	refresher := &fakeRefresher{}
	logger := slog.Default()

	ctrl := NewController(vault, state, discovery, refresher, logger)

	// Before first reconciliation: not ready, but alive (startup grace).
	if ctrl.IsReady() {
		t.Error("IsReady() = true before first reconcile, want false")
	}
	if !ctrl.IsAlive(3 * time.Minute) {
		t.Error("IsAlive() = false before first reconcile, want true (startup grace)")
	}

	// Run a successful reconciliation.
	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// After successful reconciliation: ready and alive.
	if !ctrl.IsReady() {
		t.Error("IsReady() = false after successful reconcile, want true")
	}
	if !ctrl.IsAlive(3 * time.Minute) {
		t.Error("IsAlive() = false after successful reconcile, want true")
	}

	// Alive check with a very small threshold should fail.
	if ctrl.IsAlive(0) {
		t.Error("IsAlive(0) = true, want false (threshold expired)")
	}
}
