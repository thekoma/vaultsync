package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

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
	watchAnnotation string
	logger          *slog.Logger

	// Optional informer-backed listers. When set and synced, Discover reads
	// from the local cache instead of issuing cluster-wide List calls.
	secretLister    corelisters.SecretLister
	configMapLister corelisters.ConfigMapLister
	secretSynced    cache.InformerSynced
	configMapSynced cache.InformerSynced
}

// NewDiscovery creates a Discovery that scans Kubernetes resources for the given watch annotation.
func NewDiscovery(client kubernetes.Interface, dynClient dynamic.Interface, argoCDNamespace string, watchAnnotation string, logger *slog.Logger) Discovery {
	return &k8sDiscovery{
		client:          client,
		dynClient:       dynClient,
		argoCDNamespace: argoCDNamespace,
		watchAnnotation: watchAnnotation,
		logger:          logger,
	}
}

// NewInformerDiscovery creates a Discovery backed by shared informers for
// Secrets and ConfigMaps. Informers are started in the background; until their
// caches sync, Discover falls back to direct List calls. Object payloads
// (Secret.Data, ConfigMap.Data) are stripped before caching to keep memory low,
// since discovery only needs metadata. Requires the "watch" RBAC verb on
// secrets and configmaps.
func NewInformerDiscovery(ctx context.Context, client kubernetes.Interface, dynClient dynamic.Interface, argoCDNamespace string, watchAnnotation string, resync time.Duration, logger *slog.Logger) Discovery {
	d := &k8sDiscovery{
		client:          client,
		dynClient:       dynClient,
		argoCDNamespace: argoCDNamespace,
		watchAnnotation: watchAnnotation,
		logger:          logger,
	}
	factory := informers.NewSharedInformerFactoryWithOptions(client, resync,
		informers.WithTransform(stripPayload))
	secInf := factory.Core().V1().Secrets()
	cmInf := factory.Core().V1().ConfigMaps()
	d.secretLister = secInf.Lister()
	d.configMapLister = cmInf.Lister()
	d.secretSynced = secInf.Informer().HasSynced
	d.configMapSynced = cmInf.Informer().HasSynced
	factory.Start(ctx.Done())
	return d
}

// stripPayload drops data fields so the informer cache holds only metadata.
func stripPayload(obj interface{}) (interface{}, error) {
	switch o := obj.(type) {
	case *corev1.Secret:
		o.Data = nil
		o.StringData = nil
		o.ManagedFields = nil
	case *corev1.ConfigMap:
		o.Data = nil
		o.BinaryData = nil
		o.ManagedFields = nil
	}
	return obj, nil
}

// cacheReady reports whether informer caches can be used.
func (d *k8sDiscovery) cacheReady() bool {
	return d.secretLister != nil && d.configMapLister != nil &&
		d.secretSynced() && d.configMapSynced()
}

// listSecretsAndConfigMaps returns Secrets and ConfigMaps from the informer
// cache when synced, otherwise via direct List calls.
func (d *k8sDiscovery) listSecretsAndConfigMaps(ctx context.Context) ([]*corev1.Secret, []*corev1.ConfigMap, error) {
	if d.cacheReady() {
		secrets, err := d.secretLister.List(labels.Everything())
		if err != nil {
			return nil, nil, err
		}
		cms, err := d.configMapLister.List(labels.Everything())
		if err != nil {
			return nil, nil, err
		}
		return secrets, cms, nil
	}
	if d.secretLister != nil {
		d.logger.Debug("informer cache not synced, falling back to List")
	}
	sl, err := d.client.CoreV1().Secrets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	cl, err := d.client.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	secrets := make([]*corev1.Secret, len(sl.Items))
	for i := range sl.Items {
		secrets[i] = &sl.Items[i]
	}
	cms := make([]*corev1.ConfigMap, len(cl.Items))
	for i := range cl.Items {
		cms[i] = &cl.Items[i]
	}
	return secrets, cms, nil
}

// Discover scans Secrets, ConfigMaps (all namespaces) and ArgoCD Applications (argocd namespace)
// for the vaultsync/watch annotation.
func (d *k8sDiscovery) Discover(ctx context.Context) ([]WatchedResource, error) {
	var resources []WatchedResource

	secrets, configMaps, err := d.listSecretsAndConfigMaps(ctx)
	if err != nil {
		return nil, err
	}

	// Scan Secrets in all namespaces.
	for _, s := range secrets {
		ann, ok := s.Annotations[d.watchAnnotation]
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
	for _, cm := range configMaps {
		ann, ok := cm.Annotations[d.watchAnnotation]
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
			ann, ok := annotations[d.watchAnnotation]
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

	d.logger.Debug("discovery complete",
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
