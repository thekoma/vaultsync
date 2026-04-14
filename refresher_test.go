package main

import (
	"context"
	"reflect"
	"testing"
)

// fakeRefresher is an in-memory implementation of Refresher for testing.
type fakeRefresher struct {
	refreshed []WatchedResource
	err       error
}

func (f *fakeRefresher) Refresh(_ context.Context, resource WatchedResource) error {
	if f.err != nil {
		return f.err
	}
	f.refreshed = append(f.refreshed, resource)
	return nil
}

// Compile-time check: fakeRefresher must satisfy Refresher.
var _ Refresher = (*fakeRefresher)(nil)

func TestRefresherInterface(t *testing.T) {
	resources := []WatchedResource{
		{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  "default",
			Name:       "db-creds",
			VaultPaths: []string{"litellm"},
		},
		{
			APIVersion: "argoproj.io/v1alpha1",
			Kind:       "Application",
			Namespace:  "argocd",
			Name:       "my-app",
			VaultPaths: []string{"wasabi-backup"},
		},
	}

	var r Refresher = &fakeRefresher{}
	for _, res := range resources {
		if err := r.Refresh(context.Background(), res); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
	}

	fake := r.(*fakeRefresher)
	if len(fake.refreshed) != len(resources) {
		t.Fatalf("refreshed %d resources, want %d", len(fake.refreshed), len(resources))
	}

	for i, want := range resources {
		if !reflect.DeepEqual(fake.refreshed[i], want) {
			t.Errorf("refreshed[%d] = %+v, want %+v", i, fake.refreshed[i], want)
		}
	}
}
