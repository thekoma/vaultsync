package main

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/tools/cache"
)

func application(namespace, name string, annotations map[string]string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "argoproj.io/v1alpha1", Kind: "Application"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: annotations},
	}
}

func discoveryFixtures() (*fake.Clientset, *metadatafake.FakeMetadataClient) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "s1",
			Annotations: map[string]string{"vaultsync/watch": "secret/data/litellm"}},
			Data: map[string][]byte{"k": []byte("v")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "plain"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "b", Name: "c1",
			Annotations: map[string]string{"vaultsync/watch": "x, y"}}},
	)
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		panic(err)
	}
	meta := metadatafake.NewSimpleMetadataClient(scheme,
		application("argocd", "app1", map[string]string{"vaultsync/watch": "secret/data/qbittorrent"}),
		application("argocd", "plain-app", nil),
		application("other", "app-elsewhere", map[string]string{"vaultsync/watch": "secret/data/ignored"}),
	)
	return client, meta
}

func TestDiscoverApplicationsFromMetadata(t *testing.T) {
	client, meta := discoveryFixtures()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	got, err := NewDiscovery(client, meta, "argocd", "vaultsync/watch", logger).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var apps []WatchedResource
	for _, r := range got {
		if r.Kind == "Application" {
			apps = append(apps, r)
		}
	}
	want := WatchedResource{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Namespace:  "argocd",
		Name:       "app1",
		VaultPaths: []string{"qbittorrent"},
	}
	if len(apps) != 1 || !reflect.DeepEqual(apps[0], want) {
		t.Fatalf("want only %+v, got %+v", want, apps)
	}
}

func TestInformerDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, meta := discoveryFixtures()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	d := NewInformerDiscovery(ctx, client, meta, "argocd", "vaultsync/watch", 0, logger).(*k8sDiscovery)
	syncCtx, c2 := context.WithTimeout(ctx, 5*time.Second)
	defer c2()
	if !cache.WaitForCacheSync(syncCtx.Done(), d.secretSynced, d.configMapSynced) {
		t.Fatal("informer caches did not sync")
	}
	got, err := d.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 resources, got %d: %+v", len(got), got)
	}
	s, err := d.secretLister.Secrets("a").Get("s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Data != nil {
		t.Error("secret data should be stripped from the informer cache")
	}
}

func TestListFallbackDiscovery(t *testing.T) {
	client, meta := discoveryFixtures()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, err := NewDiscovery(client, meta, "argocd", "vaultsync/watch", logger).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 resources, got %d", len(got))
	}
}
