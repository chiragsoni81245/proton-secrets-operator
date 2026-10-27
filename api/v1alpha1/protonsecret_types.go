/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// ConditionReady is True when the target Secret matches Proton Pass.
	ConditionReady = "Ready"
	// ConditionSynced is True when the last attempt to read Proton Pass succeeded.
	ConditionSynced = "Synced"

	// ForceSyncAnnotation triggers an immediate reconcile when its value changes.
	ForceSyncAnnotation = "proton.chiragsoni.org/force-sync"
	// ManagedByLabel marks Secrets created by this operator.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	// ManagedByValue is the value of ManagedByLabel.
	ManagedByValue = "proton-secrets-operator"
	// HashAnnotation holds the SHA-256 of the synced data on the target Secret.
	HashAnnotation = "proton.chiragsoni.org/data-hash"
	// RestartedAtAnnotation is patched into pod templates of rollout targets.
	RestartedAtAnnotation = "proton.chiragsoni.org/restartedAt"
)

// CreationPolicy controls what happens if the target Secret already exists.
// +kubebuilder:validation:Enum=Owner;Merge;None
type CreationPolicy string

const (
	// CreationPolicyOwner creates the Secret and owns it; refuses to adopt foreign Secrets.
	CreationPolicyOwner CreationPolicy = "Owner"
	// CreationPolicyMerge adopts an existing Secret and manages only the listed keys.
	CreationPolicyMerge CreationPolicy = "Merge"
	// CreationPolicyNone never creates; only updates a Secret that is already ours.
	CreationPolicyNone CreationPolicy = "None"
)

// DeletionPolicy controls what happens to the Secret when the ProtonSecret is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	DeletionPolicyDelete DeletionPolicy = "Delete"
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// SecretTarget describes the Kubernetes Secret to produce.
type SecretTarget struct {
	// name of the Secret. Defaults to the ProtonSecret name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// type of the Secret.
	// +optional
	// +kubebuilder:default=Opaque
	Type corev1.SecretType `json:"type,omitempty"`

	// creationPolicy controls adoption of pre-existing Secrets.
	// +optional
	// +kubebuilder:default=Owner
	CreationPolicy CreationPolicy `json:"creationPolicy,omitempty"`

	// deletionPolicy controls the Secret's fate when this resource is deleted.
	// +optional
	// +kubebuilder:default=Delete
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// labels added to the Secret.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations added to the Secret.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DataSource maps one Secret key to a Proton Pass reference.
type DataSource struct {
	// secretKey is the key in the resulting Secret.
	// +required
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	// +kubebuilder:validation:MaxLength=253
	SecretKey string `json:"secretKey"`

	// remoteRef is a pass-cli secret reference: pass://VAULT/ITEM[/FIELD].
	// +required
	// +kubebuilder:validation:Pattern=`^pass://[^/]+/[^/]+(/[^/?]+)?(\?totp=(code|uri))?$`
	RemoteRef string `json:"remoteRef"`
}

// RolloutTarget names a workload to restart when the Secret data changes.
type RolloutTarget struct {
	// kind of the workload.
	// +required
	// +kubebuilder:validation:Enum=Deployment;StatefulSet;DaemonSet
	Kind string `json:"kind"`

	// name of the workload, in the same namespace as the ProtonSecret.
	// +required
	Name string `json:"name"`
}

// ProtonSecretSpec defines the desired state of ProtonSecret
// +kubebuilder:validation:XValidation:rule="self.data.all(d, self.data.exists_one(e, e.secretKey == d.secretKey))",message="secretKey values must be unique"
type ProtonSecretSpec struct {
	// refreshInterval is how often Proton Pass is re-read. Minimum 1m.
	// +optional
	// +kubebuilder:default="1h"
	RefreshInterval *metav1.Duration `json:"refreshInterval,omitempty"`

	// target describes the Secret to create.
	// +optional
	Target SecretTarget `json:"target,omitempty"`

	// data maps Secret keys to Proton Pass references.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	Data []DataSource `json:"data"`

	// suspend pauses reconciliation.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// rolloutTargets are restarted when the Secret data changes.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	RolloutTargets []RolloutTarget `json:"rolloutTargets,omitempty"`
}

// ProtonSecretStatus defines the observed state of ProtonSecret.
type ProtonSecretStatus struct {
	// observedGeneration is the last spec generation processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastSyncTime is when the Secret was last verified against Proton Pass.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// lastSyncHash is a SHA-256 over the synced data (never the values).
	// +optional
	LastSyncHash string `json:"lastSyncHash,omitempty"`

	// secretName is the name of the managed Secret.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// conditions: Ready and Synced.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=psec
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="LastSync",type=date,JSONPath=`.status.lastSyncTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ProtonSecret is the Schema for the protonsecrets API
type ProtonSecret struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ProtonSecret
	// +required
	Spec ProtonSecretSpec `json:"spec"`

	// status defines the observed state of ProtonSecret
	// +optional
	Status ProtonSecretStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ProtonSecretList contains a list of ProtonSecret
type ProtonSecretList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ProtonSecret `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ProtonSecret{}, &ProtonSecretList{})
		return nil
	})
}
