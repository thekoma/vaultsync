package main

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// fakeDiscovery is an in-memory implementation of Discovery for testing.
type fakeDiscovery struct {
	resources []WatchedResource
}

func newFakeDiscovery(resources []WatchedResource) *fakeDiscovery {
	return &fakeDiscovery{resources: resources}
}

func (f *fakeDiscovery) Discover(_ context.Context) ([]WatchedResource, error) {
	out := make([]WatchedResource, len(f.resources))
	copy(out, f.resources)
	return out, nil
}

// Compile-time check: fakeDiscovery must satisfy Discovery.
var _ Discovery = (*fakeDiscovery)(nil)

func TestParseWatchAnnotation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "single path with prefix",
			input: "secret/data/litellm",
			want:  []string{"litellm"},
		},
		{
			name:  "multiple paths with prefix",
			input: "secret/data/wasabi-backup,secret/data/airvpn/qbittorrent",
			want:  []string{"wasabi-backup", "airvpn/qbittorrent"},
		},
		{
			name:  "single path without prefix",
			input: "litellm",
			want:  []string{"litellm"},
		},
		{
			name:  "paths with whitespace",
			input: " secret/data/litellm , secret/data/wasabi-backup ",
			want:  []string{"litellm", "wasabi-backup"},
		},
		{
			name:  "mixed prefix and no prefix",
			input: "secret/data/litellm,wasabi-backup",
			want:  []string{"litellm", "wasabi-backup"},
		},
		{
			name:  "nested path without prefix",
			input: "airvpn/qbittorrent",
			want:  []string{"airvpn/qbittorrent"},
		},
		{
			name:  "empty string",
			input: "",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWatchAnnotation(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseWatchAnnotation(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildPathToResourceMap(t *testing.T) {
	resources := []WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "default",
			Name:       "db-creds",
			VaultPaths: []string{"litellm", "wasabi-backup"},
		},
		{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Namespace:  "default",
			Name:       "app-config",
			VaultPaths: []string{"litellm"},
		},
		{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Namespace:  "argocd",
			Name:       "wasabi-app",
			VaultPaths: []string{"wasabi-backup"},
		},
	}

	got := BuildPathToResourceMap(resources)

	// Verify "litellm" maps to 2 resources.
	if len(got["litellm"]) != 2 {
		t.Fatalf("pathMap[litellm] has %d resources, want 2", len(got["litellm"]))
	}
	litellmNames := []string{got["litellm"][0].Name, got["litellm"][1].Name}
	sort.Strings(litellmNames)
	wantNames := []string{"app-config", "db-creds"}
	if !reflect.DeepEqual(litellmNames, wantNames) {
		t.Errorf("pathMap[litellm] names = %v, want %v", litellmNames, wantNames)
	}

	// Verify "wasabi-backup" maps to 2 resources.
	if len(got["wasabi-backup"]) != 2 {
		t.Fatalf("pathMap[wasabi-backup] has %d resources, want 2", len(got["wasabi-backup"]))
	}
	wasabiNames := []string{got["wasabi-backup"][0].Name, got["wasabi-backup"][1].Name}
	sort.Strings(wasabiNames)
	wantWasabi := []string{"db-creds", "wasabi-app"}
	if !reflect.DeepEqual(wasabiNames, wantWasabi) {
		t.Errorf("pathMap[wasabi-backup] names = %v, want %v", wasabiNames, wantWasabi)
	}

	// Verify no unexpected keys.
	if len(got) != 2 {
		t.Errorf("pathMap has %d keys, want 2", len(got))
	}
}

func TestDiscoveryInterface(t *testing.T) {
	expected := []WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "default",
			Name:       "my-secret",
			VaultPaths: []string{"litellm"},
		},
		{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Namespace:  "argocd",
			Name:       "my-app",
			VaultPaths: []string{"wasabi-backup", "airvpn/qbittorrent"},
		},
	}

	var d Discovery = newFakeDiscovery(expected)
	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	if len(got) != len(expected) {
		t.Fatalf("Discover() returned %d resources, want %d", len(got), len(expected))
	}

	for i, want := range expected {
		if got[i].APIVersion != want.APIVersion {
			t.Errorf("resources[%d].APIVersion = %q, want %q", i, got[i].APIVersion, want.APIVersion)
		}
		if got[i].Kind != want.Kind {
			t.Errorf("resources[%d].Kind = %q, want %q", i, got[i].Kind, want.Kind)
		}
		if got[i].Namespace != want.Namespace {
			t.Errorf("resources[%d].Namespace = %q, want %q", i, got[i].Namespace, want.Namespace)
		}
		if got[i].Name != want.Name {
			t.Errorf("resources[%d].Name = %q, want %q", i, got[i].Name, want.Name)
		}
		if !reflect.DeepEqual(got[i].VaultPaths, want.VaultPaths) {
			t.Errorf("resources[%d].VaultPaths = %v, want %v", i, got[i].VaultPaths, want.VaultPaths)
		}
	}
}
