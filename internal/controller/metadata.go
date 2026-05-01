package controller

import (
	"maps"
	"strings"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	controllerName = "ocm-managed-serviceaccount-replicaset-controller"

	finalizerName = "authentication.appthrust.io/managedserviceaccountreplicaset-cleanup"

	labelManagedBy            = "app.kubernetes.io/managed-by"
	labelPartOf               = "app.kubernetes.io/part-of"
	labelSetName              = "authentication.appthrust.io/set-name"
	labelSetNamespace         = "authentication.appthrust.io/set-namespace"
	labelSetUID               = "authentication.appthrust.io/set-uid"
	labelPlacementRefName     = "authentication.appthrust.io/placement-ref-name"
	labelGrantID              = "authentication.appthrust.io/grant-id"
	labelSliceType            = "authentication.appthrust.io/slice-type"
	labelTargetNamespace      = "authentication.appthrust.io/target-namespace"
	labelTargetNamespaceHash  = "authentication.appthrust.io/target-namespace-hash"
	labelSyncToClusterProfile = "authentication.open-cluster-management.io/sync-to-clusterprofile"
	ocmClusterNameLabel       = "open-cluster-management.io/cluster-name"

	annotationSpecHash        = "authentication.appthrust.io/spec-hash"
	annotationTargetNamespace = "authentication.appthrust.io/target-namespace"
)

var reservedChildMetadataKeys = sets.New[string](
	labelManagedBy,
	labelPartOf,
	labelSetName,
	labelSetNamespace,
	labelSetUID,
	labelPlacementRefName,
	labelGrantID,
	labelSliceType,
	labelTargetNamespace,
	labelTargetNamespaceHash,
	annotationSpecHash,
)

// Validation-only superset; child sanitization keeps labelSyncToClusterProfile overridable.
var reservedGrantMetadataKeys = reservedChildMetadataKeys.Clone().Insert(labelSyncToClusterProfile)

func sourceLabels(set *authv1alpha1.ManagedServiceAccountReplicaSet) map[string]string {
	return map[string]string{
		labelManagedBy:    controllerName,
		labelPartOf:       set.Name,
		labelSetName:      set.Name,
		labelSetNamespace: set.Namespace,
		labelSetUID:       string(set.UID),
	}
}

func sourceAnnotations(set *authv1alpha1.ManagedServiceAccountReplicaSet) map[string]string {
	return map[string]string{
		labelSetName:      set.Name,
		labelSetNamespace: set.Namespace,
		labelSetUID:       string(set.UID),
	}
}

func childLabels(set *authv1alpha1.ManagedServiceAccountReplicaSet, placementRefName string, extra map[string]string) map[string]string {
	owner := sourceLabels(set)
	owner[labelPlacementRefName] = placementRefName
	lbls := labels.Merge(sanitizedMetadata(extra), owner)
	if _, ok := lbls[labelSyncToClusterProfile]; !ok {
		lbls[labelSyncToClusterProfile] = "true"
	}
	return lbls
}

func childAnnotations(set *authv1alpha1.ManagedServiceAccountReplicaSet, extra map[string]string) map[string]string {
	return labels.Merge(sanitizedMetadata(extra), sourceAnnotations(set))
}

func sanitizedMetadata(values map[string]string) map[string]string {
	cleaned := maps.Clone(values)
	maps.DeleteFunc(cleaned, func(key, _ string) bool {
		return reservedChildMetadataKeys.Has(key)
	})
	return cleaned
}

// Used both as a list filter and as the post-list ownership check so the two cannot drift.
func ownedSelector(set *authv1alpha1.ManagedServiceAccountReplicaSet) labels.Selector {
	return labels.SelectorFromValidatedSet(sourceLabels(set))
}

func ownedBySet(set *authv1alpha1.ManagedServiceAccountReplicaSet, lbls map[string]string) bool {
	return ownedSelector(set).Matches(labels.Set(lbls))
}

// ok=false on missing source labels is intentional: AGENTS.md L52-53 (Deletion
// Guardrails) requires hub-side children to carry source labels, and ownedSelector
// List skips unlabeled children — so the Watches MapFunc drops these events to
// stay consistent with List behavior.
func requestFromSourceLabels(lbls map[string]string) (types.NamespacedName, bool) {
	name := lbls[labelSetName]
	namespace := lbls[labelSetNamespace]
	if name == "" || namespace == "" {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Name: name, Namespace: namespace}, true
}

func stableChildName(parts ...string) string {
	name := strings.Join(parts, "-")
	if len(name) <= validation.DNS1123SubdomainMaxLength {
		return strings.TrimSuffix(name, "-")
	}

	suffix := stableHash(name)
	prefixLimit := validation.DNS1123SubdomainMaxLength - len(suffix) - 1
	prefix := strings.TrimSuffix(name[:prefixLimit], "-")
	return prefix + "-" + suffix
}
