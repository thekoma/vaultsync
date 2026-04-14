package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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

// RefreshStrategy returns the strategy to use for a given resource kind.
// ArgoCD Applications use "recreate" (remove finalizer, delete, let parent re-create).
// Everything else uses "delete" (simple deletion).
func RefreshStrategy(kind string) string {
	if kind == "Application" {
		return "recreate"
	}
	return "delete"
}

// k8sRefresher implements Refresher using real Kubernetes API calls.
type k8sRefresher struct {
	client    kubernetes.Interface
	dynClient dynamic.Interface
	dryRun    bool
	logger    *slog.Logger
}

// NewRefresher creates a Refresher backed by Kubernetes API clients.
func NewRefresher(client kubernetes.Interface, dynClient dynamic.Interface, cfg Config, logger *slog.Logger) Refresher {
	return &k8sRefresher{
		client:    client,
		dynClient: dynClient,
		dryRun:    cfg.DryRun,
		logger:    logger,
	}
}

// Refresh dispatches the refresh action based on RefreshStrategy for the resource kind.
func (r *k8sRefresher) Refresh(ctx context.Context, resource WatchedResource) error {
	strategy := RefreshStrategy(resource.Kind)

	r.logger.Info("refreshing resource",
		"kind", resource.Kind,
		"namespace", resource.Namespace,
		"name", resource.Name,
		"strategy", strategy,
	)

	if r.dryRun {
		r.logger.Info("[DRY RUN] would refresh resource",
			"kind", resource.Kind,
			"namespace", resource.Namespace,
			"name", resource.Name,
			"strategy", strategy,
		)
		return nil
	}

	switch strategy {
	case "recreate":
		return r.recreateApplication(ctx, resource)
	case "delete":
		return r.deleteResource(ctx, resource)
	default:
		return fmt.Errorf("unknown refresh strategy %q for kind %q", strategy, resource.Kind)
	}
}

// deleteResource deletes a Secret or ConfigMap by kind using the typed Kubernetes client.
func (r *k8sRefresher) deleteResource(ctx context.Context, resource WatchedResource) error {
	var err error

	switch resource.Kind {
	case "Secret":
		err = r.client.CoreV1().Secrets(resource.Namespace).Delete(ctx, resource.Name, metav1.DeleteOptions{})
	case "ConfigMap":
		err = r.client.CoreV1().ConfigMaps(resource.Namespace).Delete(ctx, resource.Name, metav1.DeleteOptions{})
	default:
		return fmt.Errorf("deleteResource: unsupported kind %q", resource.Kind)
	}

	if err != nil {
		return fmt.Errorf("deleting %s %s/%s: %w", resource.Kind, resource.Namespace, resource.Name, err)
	}

	r.logger.Info("deleted resource",
		"kind", resource.Kind,
		"namespace", resource.Namespace,
		"name", resource.Name,
	)
	return nil
}

// recreateApplication handles ArgoCD Application CRs:
// 1. Remove the ArgoCD finalizer via MergePatch (set metadata.finalizers to null)
// 2. Delete the Application CR via dynamic client
// 3. Log that the parent app will re-create from git
func (r *k8sRefresher) recreateApplication(ctx context.Context, resource WatchedResource) error {
	appGVR := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}

	// Step 1: Remove the ArgoCD finalizer by setting metadata.finalizers to null.
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"finalizers": nil,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshalling finalizer patch: %w", err)
	}

	_, err = r.dynClient.Resource(appGVR).Namespace(resource.Namespace).Patch(
		ctx,
		resource.Name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("removing finalizer from Application %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	r.logger.Info("removed ArgoCD finalizer",
		"namespace", resource.Namespace,
		"name", resource.Name,
	)

	// Step 2: Delete the Application CR.
	err = r.dynClient.Resource(appGVR).Namespace(resource.Namespace).Delete(
		ctx,
		resource.Name,
		metav1.DeleteOptions{},
	)
	if err != nil {
		return fmt.Errorf("deleting Application %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	// Step 3: Log that the parent app will re-create from git.
	r.logger.Info("deleted Application CR, parent app will re-create from git",
		"namespace", resource.Namespace,
		"name", resource.Name,
	)

	return nil
}
