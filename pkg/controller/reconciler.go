package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"operator/pkg/dbclient"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// EmitLog helper prints logs to stdout.
func EmitLog(source, evType, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] [%s] %s", strings.ToUpper(source), strings.ToUpper(evType), msg)
}

// K8sReconciler handles reconciliation inside the Kubernetes cluster.
type K8sReconciler struct {
	KubeClient    kubernetes.Interface
	DynamicClient dynamic.Interface
	CRDNamespace  string
	Versions      map[string]int // Track current config version for each CR name
}

// NewK8sReconciler creates a new Kubernetes reconciler.
func NewK8sReconciler(kubeClient kubernetes.Interface, dynClient dynamic.Interface, namespace string) *K8sReconciler {
	if namespace == "" {
		namespace = "default"
	}
	return &K8sReconciler{
		KubeClient:    kubeClient,
		DynamicClient: dynClient,
		CRDNamespace:  namespace,
		Versions:      make(map[string]int),
	}
}

// RunReconciliationLoop starts the periodic watch and reconciliation loop.
func (r *K8sReconciler) RunReconciliationLoop(ctx context.Context) {
	EmitLog("operator", "info", "Starting Kubernetes reconciliation loop in namespace '%s'...", r.CRDNamespace)

	// GroupVersionResource for the DbConfigSync CRD
	gvr := schema.GroupVersionResource{
		Group:    "config.operator.io",
		Version:  "v1",
		Resource: "dbconfigsyncs",
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			EmitLog("operator", "info", "Kubernetes reconciler shutting down.")
			return
		case <-ticker.C:
			// Fetch all DbConfigSync custom resources
			list, err := r.DynamicClient.Resource(gvr).Namespace(r.CRDNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					EmitLog("operator", "warn", "DbConfigSync CRD not found in cluster. Please apply manifests/crd.yaml.")
				} else {
					EmitLog("operator", "error", "Failed to list DbConfigSync CRs: %v", err)
				}
				continue
			}

			// Prune Versions entries for CRs that no longer exist.
			active := make(map[string]struct{}, len(list.Items))
			for _, item := range list.Items {
				active[item.GetName()] = struct{}{}
			}
			for name := range r.Versions {
				if _, exists := active[name]; !exists {
					delete(r.Versions, name)
				}
			}

			for _, item := range list.Items {
				r.reconcileCustomResource(ctx, gvr, item)
			}
		}
	}
}

func (r *K8sReconciler) reconcileCustomResource(ctx context.Context, gvr schema.GroupVersionResource, item unstructured.Unstructured) {
	crName := item.GetName()

	// Unmarshal Unstructured object into our typed DbConfigSync struct
	crBytes, err := json.Marshal(item.Object)
	if err != nil {
		EmitLog("operator", "error", "Failed to marshal CR '%s' unstructured data: %v", crName, err)
		return
	}

	var syncCR DbConfigSync
	if err := json.Unmarshal(crBytes, &syncCR); err != nil {
		EmitLog("operator", "error", "Failed to decode CR '%s' spec: %v", crName, err)
		return
	}

	// Honour refreshInterval: skip this CR if the configured interval has not elapsed yet.
	// status.lastReconciled is written on every reconcile attempt (including error paths),
	// so this rate-limits all attempts, survives operator restarts, and is consistent
	// across replicas because it reads from cluster state rather than in-memory state.
	if syncCR.Spec.RefreshInterval != "" {
		interval, err := time.ParseDuration(syncCR.Spec.RefreshInterval)
		if err != nil || interval <= 0 {
			EmitLog("operator", "warn", "CR '%s' has invalid refreshInterval %q, ignoring", crName, syncCR.Spec.RefreshInterval)
		} else if lastStr, ok, _ := unstructured.NestedString(item.Object, "status", "lastReconciled"); ok && lastStr != "" {
			if last, err := time.Parse("2006-01-02 15:04:05", lastStr); err == nil && time.Since(last) < interval {
				remaining := (interval - time.Since(last)).Round(time.Second)
				EmitLog("operator", "info", "CR '%s' skipped — next reconcile in %s", crName, remaining)
				return
			}
		}
	}

	EmitLog("operator", "info", "Reconciling DbConfigSync '%s'...", crName)

	// Fix 5: guard against no target configured
	if syncCR.Spec.TargetConfigMap == "" && syncCR.Spec.TargetSecret == "" {
		EmitLog("operator", "warn", "CR '%s' has no targetConfigMap or targetSecret configured — nothing to sync.", crName)
		_ = r.updateCRStatus(ctx, gvr, item, "Misconfigured", "Neither targetConfigMap nor targetSecret is set.", map[string]string{}, r.Versions[crName])
		return
	}

	dbStatuses := make(map[string]string)
	aggregatedConfigs := make(map[string]string)
	hasError := false
	var errMsgs []string

	// 1. Fetch values from each database
	for idx, dbSpec := range syncCR.Spec.Databases {
		dbId := fmt.Sprintf("%s-%d", dbSpec.Type, idx)
		EmitLog("operator", "info", "Querying %s database...", dbSpec.Type)

		// Get Connection URI (resolve reference to secret if present)
		uri := dbSpec.ConnectionUri
		if dbSpec.ConnectionSecretRef != nil {
			secretNamespace := dbSpec.ConnectionSecretRef.Namespace
			if secretNamespace == "" {
				secretNamespace = r.CRDNamespace
			}
			secret, err := r.KubeClient.CoreV1().Secrets(secretNamespace).Get(ctx, dbSpec.ConnectionSecretRef.Name, metav1.GetOptions{})
			if err != nil {
				dbStatuses[dbId] = "SecretError"
				errMsgs = append(errMsgs, fmt.Sprintf("Failed to load secret %s: %v", dbSpec.ConnectionSecretRef.Name, err))
				EmitLog("operator", "error", "Secret lookup failed for db source %s: %v", dbId, err)
				hasError = true
				continue
			}

			keyVal, ok := secret.Data[dbSpec.ConnectionSecretRef.Key]
			if !ok {
				dbStatuses[dbId] = "KeyError"
				errMsgs = append(errMsgs, fmt.Sprintf("Key %s not found in secret %s", dbSpec.ConnectionSecretRef.Key, dbSpec.ConnectionSecretRef.Name))
				EmitLog("operator", "error", "Key %s missing in secret %s for db source %s", dbSpec.ConnectionSecretRef.Key, dbSpec.ConnectionSecretRef.Name, dbId)
				hasError = true
				continue
			}
			uri = string(keyVal)
		}

		if uri == "" {
			dbStatuses[dbId] = "ConfigError"
			errMsgs = append(errMsgs, fmt.Sprintf("Empty connection URI for db source %s", dbId))
			EmitLog("operator", "error", "Database %s connection URI is empty", dbId)
			hasError = true
			continue
		}

		// Perform fetch
		dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		configs, err := dbclient.FetchConfig(dbCtx, dbSpec.Type, uri, dbSpec.Query)
		cancel()

		if err != nil {
			dbStatuses[dbId] = "Disconnected"
			errMsgs = append(errMsgs, fmt.Sprintf("%s query failed: %v", dbSpec.Type, err))
			EmitLog(dbSpec.Type, "error", "Failed to query database config: %v", err)
			hasError = true
			continue
		}

		dbStatuses[dbId] = "Connected"
		EmitLog(dbSpec.Type, "success", "Successfully retrieved %d keys", len(configs))

		// Merge into main configs map (applying keyMapping if configured)
		for k, v := range configs {
			targetKey := k
			if dbSpec.KeyMapping != nil {
				if mappedKey, ok := dbSpec.KeyMapping[k]; ok && mappedKey != "" {
					targetKey = mappedKey
				}
			}
			aggregatedConfigs[targetKey] = v
		}
	}

	// Stop if DB read errors occurred
	if hasError {
		_ = r.updateCRStatus(ctx, gvr, item, "Error", strings.Join(errMsgs, "; "), dbStatuses, r.Versions[crName])
		return
	}

	// 2. Run value transforms
	EmitLog("operator", "info", "Executing value transformations...")
	finalConfigs, err := ProcessTransforms(aggregatedConfigs, syncCR.Spec.Transforms)
	if err != nil {
		_ = r.updateCRStatus(ctx, gvr, item, "Error", fmt.Sprintf("Transform error: %v", err), dbStatuses, r.Versions[crName])
		EmitLog("operator", "error", "Transformations failed: %v", err)
		return
	}
	// Fix 4: guard against empty result set
	if len(finalConfigs) == 0 {
		EmitLog("operator", "warn", "CR '%s': no configuration keys found after DB fetch and transforms. Skipping sync to avoid overwriting with empty data.", crName)
		_ = r.updateCRStatus(ctx, gvr, item, "Warning", "No configuration keys found — skipping sync.", dbStatuses, r.Versions[crName])
		return
	}
	EmitLog("operator", "success", "Transformations executed successfully. Syncing %d final keys.", len(finalConfigs))

	// 3. Write ConfigMap or Secret in K8s
	syncChanged := false
	if syncCR.Spec.TargetConfigMap != "" {
		changed, err := r.syncConfigMap(ctx, syncCR.Spec.TargetConfigMap, r.CRDNamespace, finalConfigs, syncCR.Spec.Reflection)
		if err != nil {
			_ = r.updateCRStatus(ctx, gvr, item, "Error", fmt.Sprintf("ConfigMap sync error: %v", err), dbStatuses, r.Versions[crName])
			EmitLog("k8s", "error", "Failed to sync ConfigMap '%s': %v", syncCR.Spec.TargetConfigMap, err)
			return
		}
		syncChanged = changed
	}

	if syncCR.Spec.TargetSecret != "" {
		changed, err := r.syncSecret(ctx, syncCR.Spec.TargetSecret, r.CRDNamespace, finalConfigs, syncCR.Spec.Reflection)
		if err != nil {
			_ = r.updateCRStatus(ctx, gvr, item, "Error", fmt.Sprintf("Secret sync error: %v", err), dbStatuses, r.Versions[crName])
			EmitLog("k8s", "error", "Failed to sync Secret '%s': %v", syncCR.Spec.TargetSecret, err)
			return
		}
		syncChanged = changed || syncChanged
	}

	// Fix 3: compute version increment before status write, commit to map after.
	// Only advance the in-memory version when the status update itself succeeds to
	// prevent the local counter from drifting ahead of the cluster state.
	currVersion := r.Versions[crName]
	if syncChanged || currVersion == 0 {
		currVersion++
		EmitLog("operator", "success", "Configuration change detected! Promoted configuration version to v%d", currVersion)
	}

	if err := r.updateCRStatus(ctx, gvr, item, "Synced", "All database sources synchronized successfully.", dbStatuses, currVersion); err == nil {
		r.Versions[crName] = currVersion
	}
}

func (r *K8sReconciler) syncConfigMap(ctx context.Context, name, namespace string, data map[string]string, reflection ReflectionSpec) (bool, error) {
	cmClient := r.KubeClient.CoreV1().ConfigMaps(namespace)

	// Reflector annotations mapping
	annotations := make(map[string]string)
	if reflection.Allowed {
		annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] = "true"
		if reflection.AllowedNamespaces != "" {
			annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] = reflection.AllowedNamespaces
		}
		if reflection.AutoEnabled {
			annotations["reflector.v1.k8s.emberstack.com/reflection-auto-enabled"] = "true"
			if reflection.AutoNamespaces != "" {
				annotations["reflector.v1.k8s.emberstack.com/reflection-auto-namespaces"] = reflection.AutoNamespaces
			}
		}
	}

	existing, err := cmClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Create ConfigMap
			newCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Namespace:   namespace,
					Annotations: annotations,
				},
				Data: data,
			}
			_, err = cmClient.Create(ctx, newCM, metav1.CreateOptions{})
			if err != nil {
				return false, err
			}
			EmitLog("k8s", "success", "Created target ConfigMap '%s/%s' with Reflector annotations", namespace, name)
			return true, nil
		}
		return false, err
	}

	// Check if data or operator-managed annotations changed (additions or removals).
	dataChanged := !reflect.DeepEqual(existing.Data, data)
	annoChanged := operatorAnnotationsChanged(existing.Annotations, annotations)

	if dataChanged || annoChanged {
		existing.Data = data
		// Merge: preserve third-party annotations, reconcile operator-managed keys
		// (add desired keys, remove operator-managed keys no longer wanted).
		existing.Annotations = mergeOperatorAnnotations(existing.Annotations, annotations)
		_, err = cmClient.Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}
		EmitLog("k8s", "success", "Updated target ConfigMap '%s/%s' (re-sync triggered for Reflector/Reloader)", namespace, name)
		return true, nil
	}

	EmitLog("k8s", "info", "ConfigMap '%s/%s' is up-to-date. No changes.", namespace, name)
	return false, nil
}

func (r *K8sReconciler) syncSecret(ctx context.Context, name, namespace string, data map[string]string, reflection ReflectionSpec) (bool, error) {
	secretClient := r.KubeClient.CoreV1().Secrets(namespace)

	// Reflector annotations
	annotations := make(map[string]string)
	if reflection.Allowed {
		annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] = "true"
		if reflection.AllowedNamespaces != "" {
			annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] = reflection.AllowedNamespaces
		}
		if reflection.AutoEnabled {
			annotations["reflector.v1.k8s.emberstack.com/reflection-auto-enabled"] = "true"
			if reflection.AutoNamespaces != "" {
				annotations["reflector.v1.k8s.emberstack.com/reflection-auto-namespaces"] = reflection.AutoNamespaces
			}
		}
	}

	// Convert data to byte map
	byteData := make(map[string][]byte)
	for k, v := range data {
		byteData[k] = []byte(v)
	}

	existing, err := secretClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			// Create Secret
			newSec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Namespace:   namespace,
					Annotations: annotations,
				},
				Type: corev1.SecretTypeOpaque,
				Data: byteData,
			}
			_, err = secretClient.Create(ctx, newSec, metav1.CreateOptions{})
			if err != nil {
				return false, err
			}
			EmitLog("k8s", "success", "Created target Secret '%s/%s' with Reflector annotations", namespace, name)
			return true, nil
		}
		return false, err
	}

	// Compare byteData and operator-managed annotation keys (additions and removals).
	dataChanged := !reflect.DeepEqual(existing.Data, byteData)
	annoChanged := operatorAnnotationsChanged(existing.Annotations, annotations)

	if dataChanged || annoChanged {
		existing.Data = byteData
		existing.Annotations = mergeOperatorAnnotations(existing.Annotations, annotations)
		_, err = secretClient.Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}
		EmitLog("k8s", "success", "Updated target Secret '%s/%s' (re-sync triggered for Reflector/Reloader)", namespace, name)
		return true, nil
	}

	EmitLog("k8s", "info", "Secret '%s/%s' is up-to-date. No changes.", namespace, name)
	return false, nil
}

// reflectorAnnotationPrefix is the shared prefix for all operator-managed reflector keys.
const reflectorAnnotationPrefix = "reflector.v1.k8s.emberstack.com/"

// operatorAnnotationsChanged returns true when the desired set of operator-managed
// annotation keys (additions OR removals) differs from what exists on the resource.
func operatorAnnotationsChanged(existing, desired map[string]string) bool {
	// Check for missing or changed desired keys.
	for k, want := range desired {
		if got, ok := existing[k]; !ok || got != want {
			return true
		}
	}
	// Check for stale operator-managed keys that are no longer desired.
	for k := range existing {
		if strings.HasPrefix(k, reflectorAnnotationPrefix) {
			if _, stillWanted := desired[k]; !stillWanted {
				return true
			}
		}
	}
	return false
}

// mergeOperatorAnnotations preserves third-party annotations on the resource,
// adds/updates the desired operator-managed keys, and removes any operator-managed
// keys that are no longer in the desired set (e.g. when reflection is disabled).
func mergeOperatorAnnotations(existing, desired map[string]string) map[string]string {
	merged := make(map[string]string, len(existing))
	for k, v := range existing {
		// Drop stale operator-managed keys; third-party keys are preserved.
		if strings.HasPrefix(k, reflectorAnnotationPrefix) {
			continue
		}
		merged[k] = v
	}
	for k, v := range desired {
		merged[k] = v
	}
	return merged
}

func (r *K8sReconciler) updateCRStatus(ctx context.Context, gvr schema.GroupVersionResource, item unstructured.Unstructured, syncStatus, msg string, dbStatuses map[string]string, version int) error {
	crName := item.GetName()

	// In lightweight dynamic controller, update status subresource
	dbStatusesInterface := make(map[string]interface{})
	for k, v := range dbStatuses {
		dbStatusesInterface[k] = v
	}

	statusObj := map[string]interface{}{
		"lastReconciled":   time.Now().Format("2006-01-02 15:04:05"),
		"activeVersion":    int64(version),
		"syncStatus":       syncStatus,
		"message":          msg,
		"databaseStatuses": dbStatusesInterface,
	}

	// Update in cluster using unstructured client patch/update
	if err := unstructured.SetNestedMap(item.Object, statusObj, "status"); err != nil {
		EmitLog("operator", "error", "Failed to set status on custom resource '%s': %v", crName, err)
		return err
	}
	_, err := r.DynamicClient.Resource(gvr).Namespace(r.CRDNamespace).UpdateStatus(ctx, &item, metav1.UpdateOptions{})
	if err != nil {
		// Log but do not block since the ConfigMap itself synced successfully
		EmitLog("operator", "error", "Failed to update custom resource '%s' status: %v", crName, err)
		return err
	}
	return nil
}
