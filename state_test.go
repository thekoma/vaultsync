package main

import (
	"context"
	"log/slog"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

// fakeStateStore is an in-memory implementation of StateStore for testing.
type fakeStateStore struct {
	versions map[string]int
	saved    bool
}

func newFakeStateStore() *fakeStateStore {
	return &fakeStateStore{versions: make(map[string]int)}
}

func (f *fakeStateStore) Load(_ context.Context) (map[string]int, error) {
	// Return a copy to avoid aliasing.
	out := make(map[string]int, len(f.versions))
	for k, v := range f.versions {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStateStore) Save(_ context.Context, versions map[string]int) error {
	f.versions = make(map[string]int, len(versions))
	for k, v := range versions {
		f.versions[k] = v
	}
	f.saved = true
	return nil
}

// Compile-time check: fakeStateStore must satisfy StateStore.
var _ StateStore = (*fakeStateStore)(nil)

func TestStateStoreInterface(t *testing.T) {
	var store StateStore = newFakeStateStore()
	ctx := context.Background()

	// Load from empty store should return empty map, no error.
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() on empty store error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load() on empty store returned %d entries, want 0", len(got))
	}

	// Save some versions.
	want := map[string]int{
		"app/db":    3,
		"app/redis": 1,
		"infra/tls": 5,
	}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load back and verify roundtrip.
	got, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() after Save error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Load() returned %d entries, want %d", len(got), len(want))
	}
	for path, wantVer := range want {
		gotVer, ok := got[path]
		if !ok {
			t.Errorf("Load() missing path %q", path)
			continue
		}
		if gotVer != wantVer {
			t.Errorf("Load()[%q] = %d, want %d", path, gotVer, wantVer)
		}
	}
}

func TestDetectChanges(t *testing.T) {
	old := map[string]int{
		"litellm": 2,
		"wasabi":  7,
	}
	current := map[string]int{
		"litellm": 3,
		"wasabi":  7,
		"new":     1,
	}

	changed := DetectChanges(old, current)

	// Expect litellm (version increased) and new (new path).
	if len(changed) != 2 {
		t.Fatalf("DetectChanges() returned %d entries, want 2", len(changed))
	}

	if v, ok := changed["litellm"]; !ok || v != 3 {
		t.Errorf("DetectChanges()[litellm] = %d, ok=%v; want 3, true", v, ok)
	}
	if v, ok := changed["new"]; !ok || v != 1 {
		t.Errorf("DetectChanges()[new] = %d, ok=%v; want 1, true", v, ok)
	}

	// wasabi should NOT be in the changed map.
	if _, ok := changed["wasabi"]; ok {
		t.Error("DetectChanges() should not include wasabi (unchanged)")
	}
}

func TestDetectChanges_EmptyOld(t *testing.T) {
	old := map[string]int{}
	current := map[string]int{
		"litellm": 3,
		"wasabi":  7,
	}

	changed := DetectChanges(old, current)

	// Everything in current is new.
	if len(changed) != 2 {
		t.Fatalf("DetectChanges() returned %d entries, want 2", len(changed))
	}
}

func TestDetectChanges_EmptyCurrent(t *testing.T) {
	old := map[string]int{
		"litellm": 2,
		"wasabi":  7,
	}
	current := map[string]int{}

	changed := DetectChanges(old, current)

	// Nothing in current means no changes.
	if len(changed) != 0 {
		t.Fatalf("DetectChanges() returned %d entries, want 0", len(changed))
	}
}

func TestDetectChanges_BothEmpty(t *testing.T) {
	changed := DetectChanges(map[string]int{}, map[string]int{})
	if len(changed) != 0 {
		t.Fatalf("DetectChanges() returned %d entries, want 0", len(changed))
	}
}

func TestConfigMapStateStore_Roundtrip(t *testing.T) {
	client := fake.NewSimpleClientset()
	cfg := Config{
		StateNamespace: "test-ns",
		StateConfigMap: "test-state",
	}
	logger := slog.Default()
	store := NewStateStore(client, cfg, logger)
	ctx := context.Background()

	// Load from non-existent ConfigMap should return empty map.
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() on missing ConfigMap error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load() on missing ConfigMap returned %d entries, want 0", len(got))
	}

	// Save creates the ConfigMap.
	want := map[string]int{
		"app/db":    3,
		"app/redis": 1,
	}
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save() create error = %v", err)
	}

	// Load back.
	got, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() after create error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Load() returned %d entries, want %d", len(got), len(want))
	}
	for path, wantVer := range want {
		if got[path] != wantVer {
			t.Errorf("Load()[%q] = %d, want %d", path, got[path], wantVer)
		}
	}

	// Save again (update path).
	want["app/db"] = 4
	want["infra/tls"] = 2
	if err := store.Save(ctx, want); err != nil {
		t.Fatalf("Save() update error = %v", err)
	}

	got, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() after update error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Load() returned %d entries, want %d", len(got), len(want))
	}
	for path, wantVer := range want {
		if got[path] != wantVer {
			t.Errorf("Load()[%q] = %d, want %d", path, got[path], wantVer)
		}
	}
}
