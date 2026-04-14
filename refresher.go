package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Refresher triggers a refresh action on a Kubernetes resource
// so that it picks up changes from Vault.
type Refresher interface {
	Refresh(ctx context.Context, resource WatchedResource) error
}

// k8sRefresher implements Refresher using real Kubernetes API calls.
type k8sRefresher struct {
	client            kubernetes.Interface
	dynClient         dynamic.Interface
	triggerAnnotation string
	dryRun            bool
	logger            *slog.Logger
	now               func() time.Time // injectable clock for testing
}

// NewRefresher creates a Refresher backed by Kubernetes API clients.
func NewRefresher(client kubernetes.Interface, dynClient dynamic.Interface, cfg Config, logger *slog.Logger) Refresher {
	return &k8sRefresher{
		client:            client,
		dynClient:         dynClient,
		triggerAnnotation: cfg.TriggerAnnotation,
		dryRun:            cfg.DryRun,
		logger:            logger,
		now:               time.Now,
	}
}

// triggerPatch builds the JSON merge-patch payload for the trigger annotation.
func triggerPatch(annotationKey string, timestamp string) ([]byte, error) {
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				annotationKey: timestamp,
			},
		},
	}
	return json.Marshal(patch)
}

// Refresh patches the resource with a vaultsync/trigger annotation set to the
// current timestamp. ArgoCD detects this annotation as drift (it is not in the
// git manifest) and re-applies the resource, causing Bank-Vaults to re-inject
// the latest secret values.
func (r *k8sRefresher) Refresh(ctx context.Context, resource WatchedResource) error {
	r.logger.Info("refreshing resource",
		"kind", resource.Kind,
		"namespace", resource.Namespace,
		"name", resource.Name,
	)

	if r.dryRun {
		r.logger.Info("[DRY RUN] would patch trigger annotation",
			"kind", resource.Kind,
			"namespace", resource.Namespace,
			"name", resource.Name,
		)
		return nil
	}

	timestamp := r.now().UTC().Format(time.RFC3339Nano)
	patchBytes, err := triggerPatch(r.triggerAnnotation, timestamp)
	if err != nil {
		return fmt.Errorf("marshalling trigger patch: %w", err)
	}

	switch resource.Kind {
	case "Secret":
		_, err = r.client.CoreV1().Secrets(resource.Namespace).Patch(
			ctx, resource.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
		)
	case "ConfigMap":
		_, err = r.client.CoreV1().ConfigMaps(resource.Namespace).Patch(
			ctx, resource.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
		)
	case "Application":
		appGVR := schema.GroupVersionResource{
			Group:    "argoproj.io",
			Version:  "v1alpha1",
			Resource: "applications",
		}
		_, err = r.dynClient.Resource(appGVR).Namespace(resource.Namespace).Patch(
			ctx, resource.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
		)
	default:
		return fmt.Errorf("unsupported kind %q", resource.Kind)
	}

	if err != nil {
		return fmt.Errorf("patching %s %s/%s: %w", resource.Kind, resource.Namespace, resource.Name, err)
	}

	r.logger.Info("patched trigger annotation",
		"kind", resource.Kind,
		"namespace", resource.Namespace,
		"name", resource.Name,
		"timestamp", timestamp,
	)
	return nil
}
