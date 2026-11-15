package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func discoveryFixtures() (*fake.Clientset, *dynfake.FakeDynamicClient) {
	client := fake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "s1",
			Annotations: map[string]string{"vaultsync/watch": "secret/data/litellm"}},
			Data: map[string][]byte{"k": []byte("v")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "plain"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "b", Name: "c1",
			Annotations: map[string]string{"vaultsync/watch": "x, y"}}},
	)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList"})
	return client, dyn
}

func TestInformerDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, dyn := discoveryFixtures()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	d := NewInformerDiscovery(ctx, client, dyn, "argocd", "vaultsync/watch", 0, logger).(*k8sDiscovery)
	syncCtx, c2 := context.WithTimeout(ctx, 5*time.Second)
	defer c2()
	if !cache.WaitForCacheSync(syncCtx.Done(), d.secretSynced, d.configMapSynced) {
		t.Fatal("informer caches did not sync")
	}
	got, err := d.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 resources, got %d: %+v", len(got), got)
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
	client, dyn := discoveryFixtures()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, err := NewDiscovery(client, dyn, "argocd", "vaultsync/watch", logger).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 resources, got %d", len(got))
	}
}
