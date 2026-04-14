package main

import (
	"context"
	"fmt"
	"sort"
	"testing"
)

// fakeVaultWatcher is an in-memory implementation of VaultWatcher for testing.
type fakeVaultWatcher struct {
	// secrets maps secret paths to their current version.
	secrets map[string]int
}

func newFakeVaultWatcher(secrets map[string]int) *fakeVaultWatcher {
	return &fakeVaultWatcher{secrets: secrets}
}

func (f *fakeVaultWatcher) ListSecretPaths(ctx context.Context) ([]string, error) {
	paths := make([]string, 0, len(f.secrets))
	for p := range f.secrets {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

func (f *fakeVaultWatcher) GetSecretVersion(ctx context.Context, path string) (int, error) {
	v, ok := f.secrets[path]
	if !ok {
		return 0, fmt.Errorf("secret not found: %s", path)
	}
	return v, nil
}

func (f *fakeVaultWatcher) GetAllVersions(ctx context.Context) (map[string]int, error) {
	result := make(map[string]int, len(f.secrets))
	for p, v := range f.secrets {
		result[p] = v
	}
	return result, nil
}

// Compile-time check: fakeVaultWatcher must satisfy VaultWatcher.
var _ VaultWatcher = (*fakeVaultWatcher)(nil)

func TestVaultWatcherInterface_ListPaths(t *testing.T) {
	secrets := map[string]int{
		"app/db":    3,
		"app/redis": 1,
		"infra/tls": 5,
	}
	var w VaultWatcher = newFakeVaultWatcher(secrets)

	paths, err := w.ListSecretPaths(context.Background())
	if err != nil {
		t.Fatalf("ListSecretPaths() error = %v", err)
	}

	if len(paths) != 3 {
		t.Fatalf("ListSecretPaths() returned %d paths, want 3", len(paths))
	}

	// The fake returns sorted paths.
	want := []string{"app/db", "app/redis", "infra/tls"}
	for i, p := range paths {
		if p != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, p, want[i])
		}
	}
}

func TestVaultWatcherInterface_GetVersion(t *testing.T) {
	secrets := map[string]int{
		"app/db":    3,
		"app/redis": 1,
	}
	var w VaultWatcher = newFakeVaultWatcher(secrets)

	v, err := w.GetSecretVersion(context.Background(), "app/db")
	if err != nil {
		t.Fatalf("GetSecretVersion(app/db) error = %v", err)
	}
	if v != 3 {
		t.Errorf("GetSecretVersion(app/db) = %d, want 3", v)
	}

	_, err = w.GetSecretVersion(context.Background(), "nonexistent")
	if err == nil {
		t.Error("GetSecretVersion(nonexistent) expected error, got nil")
	}
}

func TestVaultWatcherInterface_GetAllVersions(t *testing.T) {
	secrets := map[string]int{
		"app/db":    3,
		"app/redis": 1,
		"infra/tls": 5,
	}
	var w VaultWatcher = newFakeVaultWatcher(secrets)

	versions, err := w.GetAllVersions(context.Background())
	if err != nil {
		t.Fatalf("GetAllVersions() error = %v", err)
	}

	if len(versions) != 3 {
		t.Fatalf("GetAllVersions() returned %d entries, want 3", len(versions))
	}

	for path, wantVersion := range secrets {
		got, ok := versions[path]
		if !ok {
			t.Errorf("GetAllVersions() missing path %q", path)
			continue
		}
		if got != wantVersion {
			t.Errorf("GetAllVersions()[%q] = %d, want %d", path, got, wantVersion)
		}
	}
}

func TestVaultWatcherInterface_EmptySecrets(t *testing.T) {
	var w VaultWatcher = newFakeVaultWatcher(map[string]int{})

	paths, err := w.ListSecretPaths(context.Background())
	if err != nil {
		t.Fatalf("ListSecretPaths() error = %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("ListSecretPaths() returned %d paths, want 0", len(paths))
	}

	versions, err := w.GetAllVersions(context.Background())
	if err != nil {
		t.Fatalf("GetAllVersions() error = %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("GetAllVersions() returned %d entries, want 0", len(versions))
	}
}
