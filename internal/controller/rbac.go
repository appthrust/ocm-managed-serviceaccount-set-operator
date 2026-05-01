package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	workv1 "open-cluster-management.io/api/work/v1"
)

const (
	rbacClusterSliceType   = "workload-rbac-cluster"
	rbacNamespaceSliceType = "workload-rbac-namespace"
)

type NamespaceSelectorResolver interface {
	ResolveNamespaceSelector(ctx context.Context, set *authv1alpha1.ManagedServiceAccountReplicaSet, clusterName string, grant authv1alpha1.RBACGrant, selector labels.Selector) ([]string, error)
}

type rbacSlice struct {
	name            string
	sliceType       string
	targetNamespace string
	manifests       []workv1.Manifest
	manifestsHash   string
}

func rbacEnabled(set *authv1alpha1.ManagedServiceAccountReplicaSet) bool {
	return set.Spec.RBAC != nil && len(set.Spec.RBAC.Grants) > 0
}

func rbacClusterManifestWorkName(set *authv1alpha1.ManagedServiceAccountReplicaSet) string {
	return stableChildName(set.Name, "rbac", "cluster")
}

func rbacNamespaceManifestWorkName(set *authv1alpha1.ManagedServiceAccountReplicaSet, namespace string) string {
	return stableChildName(set.Name, "rbac", "ns", stableHash(namespace))
}

func buildManifestWork(
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName, placementRefName string,
	slice rbacSlice,
) *workv1.ManifestWork {
	annotations := sourceAnnotations(set)
	annotations[annotationSpecHash] = slice.manifestsHash
	lbls := childLabels(set, placementRefName, nil)
	lbls[labelSliceType] = slice.sliceType
	if slice.targetNamespace != "" {
		lbls[labelTargetNamespace] = slice.targetNamespace
		lbls[labelTargetNamespaceHash] = stableHash(slice.targetNamespace)
		annotations[annotationTargetNamespace] = slice.targetNamespace
	}
	return &workv1.ManifestWork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: workv1.GroupVersion.String(),
			Kind:       "ManifestWork",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        slice.name,
			Namespace:   clusterName,
			Labels:      lbls,
			Annotations: annotations,
		},
		Spec: workv1.ManifestWorkSpec{
			DeleteOption: &workv1.DeleteOption{
				PropagationPolicy: workv1.DeletePropagationPolicyTypeForeground,
			},
			Workload: workv1.ManifestsTemplate{
				Manifests: slice.manifests,
			},
		},
	}
}

func desiredRBACSlices(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	resolver NamespaceSelectorResolver,
) ([]rbacSlice, error) {
	if set.Spec.RBAC == nil {
		return nil, nil
	}
	subject := rbacv1.Subject{
		Kind:      "ServiceAccount",
		Name:      set.Spec.Template.Metadata.Name,
		Namespace: set.Spec.Template.Metadata.Namespace,
	}
	clusterManifests := []workv1.Manifest{}
	namespaceManifests := map[string][]workv1.Manifest{}
	var joined error
	for _, grant := range set.Spec.RBAC.Grants {
		switch grant.Type {
		case authv1alpha1.RBACGrantTypeClusterRole:
			clusterManifests = append(clusterManifests, renderClusterGrant(set, grant, subject)...)
		case authv1alpha1.RBACGrantTypeRole:
			namespaces, err := resolveGrantNamespaces(ctx, set, clusterName, grant, resolver)
			if err != nil {
				joined = errors.Join(joined, err)
				continue
			}
			for _, namespace := range namespaces {
				namespaceManifests[namespace] = append(namespaceManifests[namespace], renderNamespacedGrant(set, grant, namespace, subject)...)
			}
		default:
			return nil, fmt.Errorf("unsupported RBAC grant type %q for grant %q", grant.Type, grant.ID)
		}
	}

	slicesOut := make([]rbacSlice, 0, 1+len(namespaceManifests))
	if len(clusterManifests) > 0 {
		slicesOut = append(slicesOut, rbacSlice{
			name:          rbacClusterManifestWorkName(set),
			sliceType:     rbacClusterSliceType,
			manifests:     clusterManifests,
			manifestsHash: manifestsHash(clusterManifests),
		})
	}
	for _, namespace := range slices.Sorted(maps.Keys(namespaceManifests)) {
		slicesOut = append(slicesOut, rbacSlice{
			name:            rbacNamespaceManifestWorkName(set, namespace),
			sliceType:       rbacNamespaceSliceType,
			targetNamespace: namespace,
			manifests:       namespaceManifests[namespace],
			manifestsHash:   manifestsHash(namespaceManifests[namespace]),
		})
	}
	return slicesOut, joined
}

func resolveGrantNamespaces(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	grant authv1alpha1.RBACGrant,
	resolver NamespaceSelectorResolver,
) ([]string, error) {
	if grant.ForEachNamespace == nil {
		return nil, fmt.Errorf("role grant %q requires forEachNamespace", grant.ID)
	}
	switch iterator := grant.ForEachNamespace; {
	case iterator.Name != "":
		return []string{iterator.Name}, nil
	case len(iterator.Names) > 0:
		return sets.List(sets.New(iterator.Names...)), nil
	case iterator.Selector != nil:
		selector, err := metav1.LabelSelectorAsSelector(iterator.Selector)
		if err != nil {
			return nil, fmt.Errorf("invalid namespace selector for grant %q: %w", grant.ID, err)
		}
		if resolver == nil {
			return nil, fmt.Errorf("selector-targeted RBAC grant %q is waiting for controller namespace access", grant.ID)
		}
		namespaces, err := resolver.ResolveNamespaceSelector(ctx, set, clusterName, grant, selector)
		if err != nil {
			return nil, err
		}
		return sets.List(sets.New(namespaces...)), nil
	default:
		return nil, fmt.Errorf("role grant %q requires exactly one namespace iterator mode", grant.ID)
	}
}

func renderNamespacedGrant(
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	grant authv1alpha1.RBACGrant,
	namespace string,
	subject rbacv1.Subject,
) []workv1.Manifest {
	roleName := grant.Metadata.Name
	objectLabels := rbacObjectLabels(set, grant, namespace)
	objectAnnotations := rbacObjectAnnotations(set, grant, namespace)
	return []workv1.Manifest{
		rawManifest(&rbacv1.Role{
			TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "Role"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        roleName,
				Namespace:   namespace,
				Labels:      objectLabels,
				Annotations: objectAnnotations,
			},
			Rules: slices.Clone(grant.Rules),
		}),
		rawManifest(&rbacv1.RoleBinding{
			TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "RoleBinding"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        roleName,
				Namespace:   namespace,
				Labels:      objectLabels,
				Annotations: objectAnnotations,
			},
			Subjects: []rbacv1.Subject{subject},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "Role",
				Name:     roleName,
			},
		}),
	}
}

func renderClusterGrant(
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	grant authv1alpha1.RBACGrant,
	subject rbacv1.Subject,
) []workv1.Manifest {
	roleName := grant.Metadata.Name
	objectLabels := rbacObjectLabels(set, grant, "")
	objectAnnotations := rbacObjectAnnotations(set, grant, "")
	return []workv1.Manifest{
		rawManifest(&rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        roleName,
				Labels:      objectLabels,
				Annotations: objectAnnotations,
			},
			Rules: slices.Clone(grant.Rules),
		}),
		rawManifest(&rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        roleName,
				Labels:      objectLabels,
				Annotations: objectAnnotations,
			},
			Subjects: []rbacv1.Subject{subject},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     roleName,
			},
		}),
	}
}

func rbacObjectLabels(set *authv1alpha1.ManagedServiceAccountReplicaSet, grant authv1alpha1.RBACGrant, namespace string) map[string]string {
	lbls := labels.Merge(sanitizedMetadata(grant.Metadata.Labels), sourceLabels(set))
	lbls[labelGrantID] = grant.ID
	if namespace != "" {
		lbls[labelTargetNamespace] = namespace
		lbls[labelTargetNamespaceHash] = stableHash(namespace)
	}
	return lbls
}

func rbacObjectAnnotations(set *authv1alpha1.ManagedServiceAccountReplicaSet, grant authv1alpha1.RBACGrant, namespace string) map[string]string {
	annotations := labels.Merge(sanitizedMetadata(grant.Metadata.Annotations), sourceAnnotations(set))
	if namespace != "" {
		annotations[annotationTargetNamespace] = namespace
	}
	return annotations
}

func rawManifest(obj runtime.Object) workv1.Manifest {
	return workv1.Manifest{RawExtension: runtime.RawExtension{Object: obj}}
}

func (r *ManagedServiceAccountReplicaSetReconciler) ensureManifestWork(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	placementRefName string,
	slice rbacSlice,
) (*workv1.ManifestWork, error) {
	desired := buildManifestWork(set, clusterName, placementRefName, slice)
	return ensureChild(ctx, r, set, desired, &workv1.ManifestWork{}, manifestWorkSpecOps)
}

func manifestWorkAvailable(work *workv1.ManifestWork) bool {
	return manifestWorkConditionTrue(work, workv1.WorkAvailable)
}

func summarizeManifestWork(work *workv1.ManifestWork, expectedHash string) authv1alpha1.Summary {
	summary := authv1alpha1.Summary{Total: 1}
	if manifestWorkConditionTrue(work, workv1.WorkApplied) {
		summary.Applied = 1
	}
	if manifestWorkConditionTrue(work, workv1.WorkAvailable) {
		summary.Available = 1
	}
	if manifestWorkConditionTrue(work, workv1.WorkDegraded) {
		summary.Degraded = 1
	}
	if manifestWorkConditionTrue(work, workv1.WorkProgressing) {
		summary.Progressing = 1
	}
	if work.Annotations[annotationSpecHash] == expectedHash {
		summary.Updated = 1
	}
	return summary
}

func manifestWorkConditionTrue(work *workv1.ManifestWork, conditionType string) bool {
	condition := meta.FindStatusCondition(work.Status.Conditions, conditionType)
	return condition != nil &&
		condition.Status == metav1.ConditionTrue &&
		condition.ObservedGeneration == work.Generation
}

func addSummary(dst *authv1alpha1.Summary, src authv1alpha1.Summary) {
	dst.DesiredTotal += src.DesiredTotal
	dst.Total += src.Total
	dst.Applied += src.Applied
	dst.Available += src.Available
	dst.Degraded += src.Degraded
	dst.Progressing += src.Progressing
	dst.Updated += src.Updated
}

func manifestsHash(manifests []workv1.Manifest) string {
	if len(manifests) == 0 {
		return ""
	}
	sum := sha256.New()
	enc := json.NewEncoder(sum)
	for _, manifest := range manifests {
		// json.Encoder.Encode of a runtime.Object scheme-registered struct cannot fail in practice.
		_ = enc.Encode(manifest.Object)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16]
}

func stableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:10]
}
