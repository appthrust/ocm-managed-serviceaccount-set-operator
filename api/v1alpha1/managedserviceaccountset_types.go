package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CredentialPublication controls where OCM managed-serviceaccount credentials
// may be published.
type CredentialPublication string

const (
	// CredentialPublicationNone creates per-cluster ManagedServiceAccounts only.
	CredentialPublicationNone CredentialPublication = "None"
	// CredentialPublicationClusterProfile enables upstream ClusterProfile credential sync.
	CredentialPublicationClusterProfile CredentialPublication = "ClusterProfile"
)

// ManagedServiceAccountSetSpec declares one OCM managed-serviceaccount intent
// across clusters selected by OCM Placement.
type ManagedServiceAccountSetSpec struct {
	// Placement creates an owned same-namespace OCM Placement.
	// +optional
	Placement *ManagedServiceAccountSetPlacement `json:"placement,omitempty"`

	// PlacementRef references an existing same-namespace OCM Placement.
	// +optional
	PlacementRef *LocalPlacementReference `json:"placementRef,omitempty"`

	// ManagedServiceAccount describes generated OCM ManagedServiceAccount children.
	// +required
	ManagedServiceAccount ManagedServiceAccountTemplate `json:"managedServiceAccount"`

	// RemotePermissions selects reviewed permission profiles to deliver by ManifestWork.
	// +optional
	RemotePermissions *RemotePermissionsSpec `json:"remotePermissions,omitempty"`
}

// ManagedServiceAccountSetPlacement is the supported subset of OCM Placement
// used by this API. The controller renders it to a real OCM Placement.
type ManagedServiceAccountSetPlacement struct {
	// ClusterSets are OCM ManagedClusterSets selected by the generated Placement.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	ClusterSets []string `json:"clusterSets"`

	// Selector subselects ManagedClusters by label after cluster set selection.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// LocalPlacementReference points to a same-namespace OCM Placement.
type LocalPlacementReference struct {
	// Name is the referenced Placement name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ManagedServiceAccountTemplate describes generated OCM ManagedServiceAccount children.
type ManagedServiceAccountTemplate struct {
	// Name is the generated ManagedServiceAccount name in every selected
	// managed cluster namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// CredentialPublication controls whether generated credentials are synced
	// to OCM ClusterProfile namespaces.
	// +kubebuilder:validation:Enum=None;ClusterProfile
	CredentialPublication CredentialPublication `json:"credentialPublication"`

	// Rotation mirrors upstream ManagedServiceAccount rotation settings.
	// +optional
	Rotation ManagedServiceAccountRotation `json:"rotation,omitempty"`

	// Labels are copied to generated ManagedServiceAccount children after
	// reserved labels are removed by validation/defaulting.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are copied to generated ManagedServiceAccount children after
	// reserved annotations are removed by validation/defaulting.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ManagedServiceAccountRotation mirrors the stable upstream rotation fields.
type ManagedServiceAccountRotation struct {
	// Enabled prescribes whether token rotation is enabled.
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Validity is the requested token validity duration.
	// +optional
	Validity *metav1.Duration `json:"validity,omitempty"`
}

// RemotePermissionsSpec selects reviewed permission profiles.
// +kubebuilder:validation:XValidation:rule="!self.enabled || (has(self.profileRefs) && size(self.profileRefs) > 0)",message="spec.remotePermissions.profileRefs is required when remote permissions are enabled"
type RemotePermissionsSpec struct {
	// Enabled enables remote permission delivery by ManifestWork.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ProfileRefs name reviewed permission profiles installed with the operator.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=name
	// +optional
	ProfileRefs []PermissionProfileReference `json:"profileRefs,omitempty"`

	// RemoteServiceAccountNamespace is the namespace used by the upstream
	// managed-serviceaccount add-on on managed clusters.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:default=open-cluster-management-managed-serviceaccount
	// +optional
	RemoteServiceAccountNamespace string `json:"remoteServiceAccountNamespace,omitempty"`
}

// PermissionProfileReference names a reviewed remote RBAC profile.
type PermissionProfileReference struct {
	// Name is the permission profile name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:Enum=aws-workload-identity-selfhosted-irsa
	Name string `json:"name"`
}

// ManagedServiceAccountSetStatus summarizes fan-out state without credential material.
type ManagedServiceAccountSetStatus struct {
	ObservedGeneration   int64                    `json:"observedGeneration,omitempty"`
	PlacementRef         *LocalPlacementReference `json:"placementRef,omitempty"`
	SelectedClusterCount int32                    `json:"selectedClusterCount,omitempty"`
	DesiredClusterCount  int32                    `json:"desiredClusterCount,omitempty"`
	AppliedClusterCount  int32                    `json:"appliedClusterCount,omitempty"`
	ReadyClusterCount    int32                    `json:"readyClusterCount,omitempty"`
	StaleClusterCount    int32                    `json:"staleClusterCount,omitempty"`
	ConflictCount        int32                    `json:"conflictCount,omitempty"`
	FailureCount         int32                    `json:"failureCount,omitempty"`

	// FailedClusters is a bounded diagnostic list. It must not contain token
	// Secret names, token data, kubeconfig data, or credential material.
	// +listType=map
	// +listMapKey=clusterName
	// +optional
	FailedClusters []ClusterFailure `json:"failedClusters,omitempty"`

	// Clusters is a bounded diagnostic list of the first selected clusters. It
	// must not contain token Secret names, token data, kubeconfig data, or
	// credential material.
	// +listType=map
	// +listMapKey=clusterName
	// +optional
	Clusters []ClusterSummary `json:"clusters,omitempty"`

	// Conditions summarize placement, child, remote permission, cleanup, and ready state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ClusterSummary reports non-secret per-cluster state.
type ClusterSummary struct {
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`
	// +optional
	ManagedServiceAccountRef *ChildObjectReference `json:"managedServiceAccountRef,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Ready;Conflict;Failed;Deleting
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ChildObjectReference identifies a generated child object without exposing
// credential material.
type ChildObjectReference struct {
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ClusterFailure reports a bounded per-cluster failure reason.
type ClusterFailure struct {
	// +kubebuilder:validation:MinLength=1
	ClusterName string `json:"clusterName"`
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`
	// +optional
	Message string `json:"message,omitempty"`
}

// ManagedServiceAccountSet declares one managed service account intent across
// OCM Placement-selected managed clusters.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=msaset
// +kubebuilder:validation:XValidation:rule="has(self.spec.placement) != has(self.spec.placementRef)",message="exactly one of spec.placement or spec.placementRef is required"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.managedServiceAccount.name) || self.spec.managedServiceAccount.name == oldSelf.spec.managedServiceAccount.name",message="spec.managedServiceAccount.name is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.managedServiceAccount.credentialPublication) || self.spec.managedServiceAccount.credentialPublication == oldSelf.spec.managedServiceAccount.credentialPublication",message="spec.managedServiceAccount.credentialPublication is immutable"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.remotePermissions) || !oldSelf.spec.remotePermissions.enabled || (has(self.spec.remotePermissions) && self.spec.remotePermissions.enabled)",message="spec.remotePermissions.enabled cannot be disabled after being enabled"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.remotePermissions) || !oldSelf.spec.remotePermissions.enabled || (has(self.spec.remotePermissions) && self.spec.remotePermissions.profileRefs == oldSelf.spec.remotePermissions.profileRefs)",message="spec.remotePermissions.profileRefs is immutable after remote permissions are enabled"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec.remotePermissions) || !oldSelf.spec.remotePermissions.enabled || (has(self.spec.remotePermissions) && self.spec.remotePermissions.remoteServiceAccountNamespace == oldSelf.spec.remotePermissions.remoteServiceAccountNamespace)",message="spec.remotePermissions.remoteServiceAccountNamespace is immutable after remote permissions are enabled"
type ManagedServiceAccountSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ManagedServiceAccountSetSpec   `json:"spec,omitempty"`
	Status            ManagedServiceAccountSetStatus `json:"status,omitempty"`
}

// ManagedServiceAccountSetList contains ManagedServiceAccountSet objects.
// +kubebuilder:object:root=true
type ManagedServiceAccountSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedServiceAccountSet `json:"items"`
}
