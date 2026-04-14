package main

import (
	"context"
	"log/slog"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const watchAnnotation = "vaultsync/watch"

// WatchedResource represents a Kubernetes resource that watches one or more Vault secret paths.
type WatchedResource struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	VaultPaths []string
}

// Discovery finds Kubernetes resources annotated with the vaultsync/watch annotation.
type Discovery interface {
	// Discover scans for annotated resources and returns a list of watched resources.
	Discover(ctx context.Context) ([]WatchedResource, error)
}

// k8sDiscovery implements Discovery by scanning Secrets, ConfigMaps, and ArgoCD Applications.
type k8sDiscovery struct {
	client          kubernetes.Interface
	dynClient       dynamic.Interface
	argoCDNamespace string
	logger          *slog.Logger
}

// NewDiscovery creates a Discovery that scans Kubernetes resources for the vaultsync/watch annotation.
func NewDiscovery(client kubernetes.Interface, dynClient dynamic.Interface, argoCDNamespace string, logger *slog.Logger) Discovery {
	return &k8sDiscovery{
		client:          client,
		dynClient:       dynClient,
		argoCDNamespace: argoCDNamespace,
		logger:          logger,
	}
}

// Discover scans Secrets, ConfigMaps (all namespaces) and ArgoCD Applications (argocd namespace)
// for the vaultsync/watch annotation.
func (d *k8sDiscovery) Discover(ctx context.Context) ([]WatchedResource, error) {
	var resources []WatchedResource

	// Scan Secrets in all namespaces.
	secrets, err := d.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range secrets.Items {
		ann, ok := s.Annotations[watchAnnotation]
		if !ok || ann == "" {
			continue
		}
		paths := ParseWatchAnnotation(ann)
		if len(paths) == 0 {
			continue
		}
		resources = append(resources, WatchedResource{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  s.Namespace,
			Name:       s.Name,
			VaultPaths: paths,
		})
		d.logger.Debug("discovered secret",
			"namespace", s.Namespace,
			"name", s.Name,
			"paths", paths,
		)
	}

	// Scan ConfigMaps in all namespaces.
	configMaps, err := d.client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, cm := range configMaps.Items {
		ann, ok := cm.Annotations[watchAnnotation]
		if !ok || ann == "" {
			continue
		}
		paths := ParseWatchAnnotation(ann)
		if len(paths) == 0 {
			continue
		}
		resources = append(resources, WatchedResource{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Namespace:  cm.Namespace,
			Name:       cm.Name,
			VaultPaths: paths,
		})
		d.logger.Debug("discovered configmap",
			"namespace", cm.Namespace,
			"name", cm.Name,
			"paths", paths,
		)
	}

	// Scan ArgoCD Applications in the argocd namespace using the dynamic client.
	appGVR := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}
	apps, err := d.dynClient.Resource(appGVR).Namespace(d.argoCDNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		d.logger.Warn("failed to list ArgoCD applications, skipping",
			"error", err,
		)
	} else {
		for _, app := range apps.Items {
			annotations := app.GetAnnotations()
			ann, ok := annotations[watchAnnotation]
			if !ok || ann == "" {
				continue
			}
			paths := ParseWatchAnnotation(ann)
			if len(paths) == 0 {
				continue
			}
			resources = append(resources, WatchedResource{
				APIVersion: "argoproj.io/v1alpha1",
				Kind:       "Application",
				Namespace:  app.GetNamespace(),
				Name:       app.GetName(),
				VaultPaths: paths,
			})
			d.logger.Debug("discovered argocd application",
				"namespace", app.GetNamespace(),
				"name", app.GetName(),
				"paths", paths,
			)
		}
	}

	d.logger.Info("discovery complete",
		"resources", len(resources),
	)

	return resources, nil
}

// ParseWatchAnnotation parses the vaultsync/watch annotation value into vault paths.
// It accepts comma-separated values, strips the "secret/data/" prefix if present,
// and trims whitespace.
func ParseWatchAnnotation(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return []string{}
	}

	parts := strings.Split(value, ",")
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Strip the "MOUNT/data/" prefix to get the path relative to the KV v2 mount.
		// e.g., "secret/data/litellm" → "litellm", "kv/data/myapp" → "myapp"
		if idx := strings.Index(p, "/data/"); idx >= 0 {
			p = p[idx+len("/data/"):]
		}
		paths = append(paths, p)
	}
	return paths
}

// BuildPathToResourceMap inverts a list of WatchedResources to a map of
// vault path to the list of resources watching that path.
func BuildPathToResourceMap(resources []WatchedResource) map[string][]WatchedResource {
	m := make(map[string][]WatchedResource)
	for _, r := range resources {
		for _, p := range r.VaultPaths {
			m[p] = append(m[p], r)
		}
	}
	return m
}
