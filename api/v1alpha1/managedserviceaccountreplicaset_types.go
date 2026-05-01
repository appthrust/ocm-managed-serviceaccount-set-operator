package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1alpha1 "open-cluster-management.io/api/cluster/v1alpha1"
)

// ManagedServiceAccountReplicaSetSpec declares one OCM managed-serviceaccount
// intent across clusters selected by placementRefs.
type ManagedServiceAccountReplicaSetSpec struct {
	// PlacementRefs select target ManagedClusters from same-namespace OCM
	// Placement and PlacementDecision output.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	PlacementRefs []PlacementRef `json:"placementRefs"`

	// Template is the per-cluster ManagedServiceAccount template.
	// +required
	Template ManagedServiceAccountTemplate `json:"template"`

	// RBAC declares typed remote RBAC to bind to the generated managed service
	// account on every selected managed cluster.
	// +optional
	RBAC *RBAC `json:"rbac,omitempty"`
}

// PlacementRef points to a same-namespace OCM Placement.
type PlacementRef struct {
	// Name of the OCM Placement in the same namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// RolloutStrategy controls how generated ManagedServiceAccounts and
	// optional RBAC ManifestWorks are applied across clusters selected by this
	// placement ref.
	// +kubebuilder:default={type: All}
	// +optional
	RolloutStrategy clusterv1alpha1.RolloutStrategy `json:"rolloutStrategy,omitempty"`
}

// ManagedServiceAccountTemplate describes generated OCM
// ManagedServiceAccount children.
type ManagedServiceAccountTemplate struct {
	// Metadata is copied to each generated ManagedServiceAccount.
	// +required
	Metadata ManagedServiceAccountTemplateMetadata `json:"metadata"`

	// Spec is the per-cluster ManagedServiceAccount spec.
	// +required
	Spec ManagedServiceAccountTemplateSpec `json:"spec"`
}

// ManagedServiceAccountTemplateMetadata describes generated child metadata.
type ManagedServiceAccountTemplateMetadata struct {
	// Name is the ServiceAccount name created on each managed cluster and
	// the metadata.name of every generated hub-side ManagedServiceAccount.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Namespace is the ServiceAccount installation namespace on managed
	// clusters. This is not the hub namespace where child ManagedServiceAccount
	// objects are created.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// Labels are copied to each generated ManagedServiceAccount after
	// controller-reserved keys are rejected by validation.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are copied to each generated ManagedServiceAccount after
	// controller-reserved keys are rejected by validation.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ManagedServiceAccountTemplateSpec mirrors the stable upstream fields the
// controller passes through to generated ManagedServiceAccounts.
type ManagedServiceAccountTemplateSpec struct {
	// Rotation mirrors upstream ManagedServiceAccount rotation settings.
	// +optional
	Rotation ManagedServiceAccountRotation `json:"rotation,omitempty"`

	// TTLSecondsAfterCreation is passed through only when explicitly set.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TTLSecondsAfterCreation *int32 `json:"ttlSecondsAfterCreation,omitempty"`
}

// ManagedServiceAccountRotation mirrors the stable upstream rotation fields.
type ManagedServiceAccountRotation struct {
	// Enabled prescribes whether token rotation is enabled.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Validity is the requested token validity duration.
	// +optional
	Validity *metav1.Duration `json:"validity,omitempty"`
}

// RBAC declares generated remote RBAC.
// +kubebuilder:validation:XValidation:rule="!has(self.grants) || self.grants.all(g, self.grants.exists_one(h, h.id == g.id))",message="spec.rbac.grants[].id must be unique"
type RBAC struct {
	// Grants render typed remote RBAC resources on each selected managed
	// cluster.
	// +optional
	// +kubebuilder:validation:MaxItems=128
	// +listType=map
	// +listMapKey=id
	Grants []RBACGrant `json:"grants,omitempty"`
}

// RBACGrant renders one closed-domain RBAC grant. Role grants expand to one
// Role and RoleBinding per target namespace. ClusterRole grants render one
// ClusterRole and ClusterRoleBinding per selected managed cluster.
// +kubebuilder:validation:XValidation:rule="self.type == 'ClusterRole' ? !has(self.forEachNamespace) : has(self.forEachNamespace)",message="Role grants require forEachNamespace and ClusterRole grants must omit it"
// +kubebuilder:validation:XValidation:rule="!has(self.forEachNamespace) || ((has(self.forEachNamespace.name) ? 1 : 0) + (has(self.forEachNamespace.names) ? 1 : 0) + (has(self.forEachNamespace.selector) ? 1 : 0)) == 1",message="forEachNamespace must set exactly one of name, names, or selector"
type RBACGrant struct {
	// ID is the stable grant identity. Renaming it is treated as deleting one
	// generated grant and creating another.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`

	// Type selects the closed generated RBAC resource family.
	// +required
	// +kubebuilder:validation:Enum=Role;ClusterRole
	Type RBACGrantType `json:"type"`

	// ForEachNamespace declares namespace fan-out for Role grants.
	// +optional
	ForEachNamespace *NamespaceIterator `json:"forEachNamespace,omitempty"`

	// Metadata is copied to generated RBAC resources after controller-reserved
	// keys are rejected by validation.
	// +required
	Metadata RBACGrantMetadata `json:"metadata"`

	// Rules are copied to the generated Role or ClusterRole.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	Rules []rbacv1.PolicyRule `json:"rules"`
}

// RBACGrantType is a closed generated RBAC resource family.
type RBACGrantType string

const (
	RBACGrantTypeRole        RBACGrantType = "Role"
	RBACGrantTypeClusterRole RBACGrantType = "ClusterRole"
)

// NamespaceIterator declares how a Role grant expands to target namespaces.
type NamespaceIterator struct {
	// Name targets one namespace.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name,omitempty"`

	// Names targets a fixed list of namespaces.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Names []string `json:"names,omitempty"`

	// Selector targets the current remote namespaces whose labels match this
	// Kubernetes label selector.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// RBACGrantMetadata matches Kubernetes RBAC metadata semantics except that
// namespace is supplied by the namespace iterator for Role grants.
type RBACGrantMetadata struct {
	// Name is applied as metadata.name to the generated Role or ClusterRole
	// and to its matching RoleBinding or ClusterRoleBinding on managed
	// clusters.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Labels are copied to the generated RBAC objects after
	// controller-reserved keys are rejected by validation.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are copied to the generated RBAC objects after
	// controller-reserved keys are rejected by validation.
	// +optional
	// +kubebuilder:validation:MaxProperties=32
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ManagedServiceAccountReplicaSetStatus summarizes fan-out state without
// credential material.
type ManagedServiceAccountReplicaSetStatus struct {
	// ObservedGeneration is the last generation reconciled by the controller.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SelectedClusterCount is the union-deduplicated number of ManagedClusters
	// resolved from spec.placementRefs.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SelectedClusterCount int32 `json:"selectedClusterCount,omitempty"`

	// ReadyClusterCount is the number of selected clusters whose child MSA is
	// reconciled and whose RBAC ManifestWork, when present, is Available.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ReadyClusterCount int32 `json:"readyClusterCount,omitempty"`

	// Summary aggregates child RBAC ManifestWork state.
	// +optional
	Summary *Summary `json:"summary,omitempty"`

	// Rollout summarizes rollout progress across the union of selected
	// ManagedClusters.
	// +optional
	Rollout *RolloutSummary `json:"rollout,omitempty"`

	// ControllerAccess summarizes the internal namespace-reader bootstrap
	// access used only for selector-targeted grants.
	// +optional
	ControllerAccess *ControllerAccessStatus `json:"controllerAccess,omitempty"`

	// Placements reports per-ref resolution and child aggregation.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	Placements []PlacementStatus `json:"placements,omitempty"`

	// Conditions summarize top-level placement, rollout, readiness, and
	// cleanup state.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ControllerAccessStatus summarizes internal selector-resolution access.
type ControllerAccessStatus struct {
	// DesiredClusterCount is the selected cluster count that needs internal
	// namespace-reader access.
	// +optional
	// +kubebuilder:validation:Minimum=0
	DesiredClusterCount int32 `json:"desiredClusterCount,omitempty"`

	// ReadyClusterCount is the count whose controller-access MSA and
	// namespace-reader ManifestWork are ready.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ReadyClusterCount int32 `json:"readyClusterCount,omitempty"`

	// Conditions summarize controller-access readiness.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Summary aggregates child ManifestWork state.
type Summary struct {
	// DesiredTotal is the number of RBAC ManifestWork objects expected across
	// all selected ManagedClusters for the current spec.
	// +kubebuilder:validation:Minimum=0
	DesiredTotal int32 `json:"desiredTotal,omitempty"`
	// Total is the number of RBAC ManifestWork objects observed across all
	// selected ManagedClusters.
	// +kubebuilder:validation:Minimum=0
	Total int32 `json:"total,omitempty"`
	// Applied is the number of observed RBAC ManifestWork objects whose
	// Applied condition is True for the current generation.
	// +kubebuilder:validation:Minimum=0
	Applied int32 `json:"applied,omitempty"`
	// Available is the number of observed RBAC ManifestWork objects whose
	// Available condition is True for the current generation.
	// +kubebuilder:validation:Minimum=0
	Available int32 `json:"available,omitempty"`
	// Degraded is the number of observed RBAC ManifestWork objects whose
	// Degraded condition is True for the current generation.
	// +kubebuilder:validation:Minimum=0
	Degraded int32 `json:"degraded,omitempty"`
	// Progressing is the number of observed RBAC ManifestWork objects whose
	// Progressing condition is True for the current generation.
	// +kubebuilder:validation:Minimum=0
	Progressing int32 `json:"progressing,omitempty"`
	// Updated is the number of observed RBAC ManifestWork objects whose spec
	// hash matches the desired manifests hash and are therefore not stale.
	// +kubebuilder:validation:Minimum=0
	Updated int32 `json:"updated,omitempty"`
}

// PlacementStatus is the per-ref view of resolution and child aggregation.
type PlacementStatus struct {
	// Name identifies the placementRef this status describes and matches the
	// corresponding spec.placementRefs[*].name entry.
	// +required
	Name string `json:"name"`

	// SelectedClusterCount is the number of ManagedClusters resolved from
	// this placement ref.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SelectedClusterCount int32 `json:"selectedClusterCount,omitempty"`

	// Summary aggregates child RBAC ManifestWork state scoped to the
	// ManagedClusters selected by this placement ref.
	// +optional
	Summary *Summary `json:"summary,omitempty"`

	// Rollout summarizes rollout progress for this placement ref.
	// +optional
	Rollout *RolloutSummary `json:"rollout,omitempty"`

	// AvailableDecisionGroups mirrors the OCM ManifestWorkReplicaSet decision
	// group progress message for this placement ref.
	// +optional
	AvailableDecisionGroups string `json:"availableDecisionGroups,omitempty"`

	// Conditions report per-ref resolution state, such as PlacementResolved
	// and PlacementEmpty.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RolloutSummary summarizes rollout state in OCM rollout terms.
type RolloutSummary struct {
	// Total is the number of selected ManagedClusters considered by this
	// rollout summary.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Total int32 `json:"total,omitempty"`
	// Updating is the number of selected ManagedClusters currently in the
	// rollout window, including ToApply, Progressing, Failed within deadline,
	// and Succeeded within minSuccessTime.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Updating int32 `json:"updating,omitempty"`
	// Succeeded is the number of selected ManagedClusters that completed
	// rollout and are no longer in the active rollout window.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Succeeded int32 `json:"succeeded,omitempty"`
	// Failed is the number of selected ManagedClusters in Failed state outside
	// the active rollout window.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Failed int32 `json:"failed,omitempty"`
	// TimedOut is the number of selected ManagedClusters whose rollout exceeded
	// progressDeadline.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TimedOut int32 `json:"timedOut,omitempty"`
	// Conditions summarize rollout progression.
	// +kubebuilder:validation:MaxItems=4
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ManagedServiceAccountReplicaSet fans out OCM ManagedServiceAccount children
// to ManagedClusters selected by spec.placementRefs.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=managedserviceaccountreplicasets,scope=Namespaced,shortName=msars
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Selected",type="integer",JSONPath=".status.selectedClusterCount",description="ManagedClusters selected by placementRefs"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyClusterCount",description="Selected clusters reconciled with the latest child resources"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.template.metadata.labels) || self.spec.template.metadata.labels.all(key, !(key in ['app.kubernetes.io/managed-by', 'app.kubernetes.io/part-of', 'authentication.appthrust.io/set-name', 'authentication.appthrust.io/set-namespace', 'authentication.appthrust.io/set-uid', 'authentication.appthrust.io/placement-ref-name']))",message="spec.template.metadata.labels must not contain controller-reserved keys"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.template.metadata.annotations) || self.spec.template.metadata.annotations.all(key, !(key in ['app.kubernetes.io/managed-by', 'app.kubernetes.io/part-of', 'authentication.appthrust.io/set-name', 'authentication.appthrust.io/set-namespace', 'authentication.appthrust.io/set-uid', 'authentication.appthrust.io/placement-ref-name']))",message="spec.template.metadata.annotations must not contain controller-reserved keys"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.rbac) || !has(self.spec.rbac.grants) || self.spec.rbac.grants.all(g, !has(g.metadata.labels) || g.metadata.labels.all(key, !(key in ['app.kubernetes.io/managed-by', 'app.kubernetes.io/part-of', 'authentication.appthrust.io/set-name', 'authentication.appthrust.io/set-namespace', 'authentication.appthrust.io/set-uid', 'authentication.appthrust.io/placement-ref-name', 'authentication.appthrust.io/grant-id', 'authentication.appthrust.io/slice-type', 'authentication.appthrust.io/target-namespace', 'authentication.appthrust.io/target-namespace-hash', 'authentication.appthrust.io/spec-hash', 'authentication.open-cluster-management.io/sync-to-clusterprofile'])))",message="spec.rbac.grants[].metadata.labels must not contain controller-reserved keys"
// +kubebuilder:validation:XValidation:rule="!has(self.spec.rbac) || !has(self.spec.rbac.grants) || self.spec.rbac.grants.all(g, !has(g.metadata.annotations) || g.metadata.annotations.all(key, !(key in ['app.kubernetes.io/managed-by', 'app.kubernetes.io/part-of', 'authentication.appthrust.io/set-name', 'authentication.appthrust.io/set-namespace', 'authentication.appthrust.io/set-uid', 'authentication.appthrust.io/placement-ref-name', 'authentication.appthrust.io/grant-id', 'authentication.appthrust.io/slice-type', 'authentication.appthrust.io/target-namespace', 'authentication.appthrust.io/target-namespace-hash', 'authentication.appthrust.io/spec-hash', 'authentication.open-cluster-management.io/sync-to-clusterprofile'])))",message="spec.rbac.grants[].metadata.annotations must not contain controller-reserved keys"
type ManagedServiceAccountReplicaSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ManagedServiceAccountReplicaSetSpec   `json:"spec,omitempty"`
	Status            ManagedServiceAccountReplicaSetStatus `json:"status,omitempty"`
}

// ManagedServiceAccountReplicaSetList contains ManagedServiceAccountReplicaSet objects.
// +kubebuilder:object:root=true
type ManagedServiceAccountReplicaSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedServiceAccountReplicaSet `json:"items"`
}
