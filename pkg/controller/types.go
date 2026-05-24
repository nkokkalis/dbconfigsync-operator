package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DbConfigSync represents the Custom Resource for our database config operator.
type DbConfigSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DbConfigSyncSpec   `json:"spec"`
	Status DbConfigSyncStatus `json:"status,omitempty"`
}

// DbConfigSyncSpec defines the desired state of DbConfigSync.
type DbConfigSyncSpec struct {
	TargetConfigMap string         `json:"targetConfigMap,omitempty"`
	TargetSecret    string         `json:"targetSecret,omitempty"`
	Reflection      ReflectionSpec `json:"reflection,omitempty"`
	Databases       []DatabaseSpec `json:"databases"`
	Transforms      []TransformSpec `json:"transforms,omitempty"`
}

// ReflectionSpec contains parameters for emberstack/reflector compatibility.
type ReflectionSpec struct {
	Allowed           bool   `json:"allowed,omitempty"`
	AllowedNamespaces string `json:"allowedNamespaces,omitempty"`
	AutoEnabled       bool   `json:"autoEnabled,omitempty"`
	AutoNamespaces    string `json:"autoNamespaces,omitempty"`
}

// DatabaseSpec defines connection details and queries for a config source.
type DatabaseSpec struct {
	Type                string             `json:"type"` // postgresql, mysql, redis, mongodb
	ConnectionUri       string             `json:"connectionUri,omitempty"`
	ConnectionSecretRef *SecretReference   `json:"connectionSecretRef,omitempty"`
	Query               string             `json:"query"` // query or key command
	KeyMapping          map[string]string  `json:"keyMapping,omitempty"`
}

// SecretReference references a key in a Kubernetes Secret.
type SecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Key       string `json:"key"`
}

// TransformSpec defines variables generated dynamically via templates or joins.
type TransformSpec struct {
	Name          string   `json:"name"`          // Env variable target name (e.g. DATABASE_URL)
	Type          string   `json:"type"`          // template, join, base64, jsonpath
	Template      string   `json:"template,omitempty"`      // Go template string (used for 'template' type)
	Separator     string   `json:"separator,omitempty"`     // Separator (used for 'join' type)
	SourceKeys    []string `json:"sourceKeys,omitempty"`    // Keys to join (used for 'join' type)
	SourcePattern string   `json:"sourcePattern,omitempty"` // Regex key match pattern (used for 'join' type)
	SourceKey     string   `json:"sourceKey,omitempty"`     // Input key (used for 'base64', 'jsonpath' types)
	Operation     string   `json:"operation,omitempty"`     // Operation e.g. encode/decode (used for 'base64' type)
	JsonPath      string   `json:"jsonPath,omitempty"`      // JSON path syntax e.g. $.field (used for 'jsonpath' type)
}

// DbConfigSyncStatus defines the observed state of DbConfigSync.
type DbConfigSyncStatus struct {
	LastReconciled   string            `json:"lastReconciled,omitempty"`
	ActiveVersion    int               `json:"activeVersion,omitempty"`
	SyncStatus       string            `json:"syncStatus,omitempty"` // Synced, Error, Syncing
	Message          string            `json:"message,omitempty"`
	DatabaseStatuses map[string]string `json:"databaseStatuses,omitempty"`
}

// DbConfigSyncList contains a list of DbConfigSync resources.
type DbConfigSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DbConfigSync `json:"items"`
}
