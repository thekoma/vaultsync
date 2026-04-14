package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const stateKey = "versions.json"

// StateStore persists version state between controller restarts.
type StateStore interface {
	// Load reads the last-saved version map. Returns an empty map if no state exists.
	Load(ctx context.Context) (map[string]int, error)

	// Save writes the version map to persistent storage.
	Save(ctx context.Context, versions map[string]int) error
}

// configMapStateStore implements StateStore using a Kubernetes ConfigMap.
type configMapStateStore struct {
	client    kubernetes.Interface
	namespace string
	name      string
	logger    *slog.Logger
}

// NewStateStore creates a StateStore backed by a Kubernetes ConfigMap.
func NewStateStore(client kubernetes.Interface, cfg Config, logger *slog.Logger) StateStore {
	return &configMapStateStore{
		client:    client,
		namespace: cfg.StateNamespace,
		name:      cfg.StateConfigMap,
		logger:    logger,
	}
}

// Load reads the ConfigMap and unmarshals the version map from the stateKey.
// Returns an empty map if the ConfigMap does not exist.
func (s *configMapStateStore) Load(ctx context.Context) (map[string]int, error) {
	cm, err := s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			s.logger.Info("state configmap not found, starting fresh",
				"namespace", s.namespace,
				"name", s.name,
			)
			return make(map[string]int), nil
		}
		return nil, fmt.Errorf("getting state configmap: %w", err)
	}

	raw, ok := cm.Data[stateKey]
	if !ok {
		s.logger.Info("state configmap exists but has no versions key, starting fresh",
			"namespace", s.namespace,
			"name", s.name,
		)
		return make(map[string]int), nil
	}

	var versions map[string]int
	if err := json.Unmarshal([]byte(raw), &versions); err != nil {
		return nil, fmt.Errorf("unmarshaling state data: %w", err)
	}

	s.logger.Info("loaded state",
		"paths", len(versions),
	)
	return versions, nil
}

// Save marshals the version map to JSON and writes it to the ConfigMap.
// Creates the ConfigMap if it does not exist, updates it otherwise.
func (s *configMapStateStore) Save(ctx context.Context, versions map[string]int) error {
	data, err := json.Marshal(versions)
	if err != nil {
		return fmt.Errorf("marshaling state data: %w", err)
	}

	cmClient := s.client.CoreV1().ConfigMaps(s.namespace)
	existing, err := cmClient.Get(ctx, s.name, metav1.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("checking state configmap: %w", err)
		}

		// ConfigMap does not exist — create it.
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.name,
				Namespace: s.namespace,
			},
			Data: map[string]string{
				stateKey: string(data),
			},
		}
		if _, err := cmClient.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating state configmap: %w", err)
		}
		s.logger.Info("created state configmap",
			"namespace", s.namespace,
			"name", s.name,
			"paths", len(versions),
		)
		return nil
	}

	// ConfigMap exists — update it.
	if existing.Data == nil {
		existing.Data = make(map[string]string)
	}
	existing.Data[stateKey] = string(data)
	if _, err := cmClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating state configmap: %w", err)
	}
	s.logger.Info("updated state configmap",
		"namespace", s.namespace,
		"name", s.name,
		"paths", len(versions),
	)
	return nil
}

// DetectChanges compares old and current version maps and returns a map of
// paths whose version increased or that are new in current.
func DetectChanges(old, current map[string]int) map[string]int {
	changed := make(map[string]int)
	for path, curVer := range current {
		oldVer, existed := old[path]
		if !existed || curVer > oldVer {
			changed[path] = curVer
		}
	}
	return changed
}
