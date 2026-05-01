package controller

import (
	"context"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	workv1 "open-cluster-management.io/api/work/v1"
	msav1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
)

const controllerAccessRBACSliceType = "controller-access-rbac"

func selectorRBACEnabled(set *authv1alpha1.ManagedServiceAccountReplicaSet) bool {
	if set.Spec.RBAC == nil {
		return false
	}
	for _, grant := range set.Spec.RBAC.Grants {
		if grant.ForEachNamespace != nil && grant.ForEachNamespace.Selector != nil {
			return true
		}
	}
	return false
}

func controllerAccessManagedServiceAccountName(set *authv1alpha1.ManagedServiceAccountReplicaSet) string {
	return stableChildName(set.Name, "controller-access")
}

func controllerAccessManifestWorkName(set *authv1alpha1.ManagedServiceAccountReplicaSet) string {
	return stableChildName(set.Name, "access", "rbac")
}

func buildControllerAccessManagedServiceAccount(set *authv1alpha1.ManagedServiceAccountReplicaSet, clusterName, placementRefName string) *msav1beta1.ManagedServiceAccount {
	return &msav1beta1.ManagedServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: msav1beta1.GroupVersion.String(),
			Kind:       "ManagedServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        controllerAccessManagedServiceAccountName(set),
			Namespace:   clusterName,
			Labels:      childLabels(set, placementRefName, nil),
			Annotations: sourceAnnotations(set),
		},
		Spec: msav1beta1.ManagedServiceAccountSpec{
			Rotation: msav1beta1.ManagedServiceAccountRotation{
				Enabled:  true,
				Validity: defaultTokenValidity,
			},
		},
	}
}

func buildControllerAccessManifestWork(set *authv1alpha1.ManagedServiceAccountReplicaSet, clusterName, placementRefName string) *workv1.ManifestWork {
	name := controllerAccessManagedServiceAccountName(set)
	manifests := []workv1.Manifest{
		rawManifest(&rbacv1.ClusterRole{
			TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      sourceLabels(set),
				Annotations: sourceAnnotations(set),
			},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
				Verbs:     []string{"get", "list", "watch"},
			}},
		}),
		rawManifest(&rbacv1.ClusterRoleBinding{
			TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Labels:      sourceLabels(set),
				Annotations: sourceAnnotations(set),
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      name,
				Namespace: set.Spec.Template.Metadata.Namespace,
			}},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     name,
			},
		}),
	}
	return buildManifestWork(set, clusterName, placementRefName, rbacSlice{
		name:          controllerAccessManifestWorkName(set),
		sliceType:     controllerAccessRBACSliceType,
		manifests:     manifests,
		manifestsHash: manifestsHash(manifests),
	})
}

func (r *ManagedServiceAccountReplicaSetReconciler) ensureControllerAccessManagedServiceAccount(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	placementRefName string,
) (*msav1beta1.ManagedServiceAccount, error) {
	desired := buildControllerAccessManagedServiceAccount(set, clusterName, placementRefName)
	return ensureChild(ctx, r, set, desired, &msav1beta1.ManagedServiceAccount{}, msaSpecOps)
}

func (r *ManagedServiceAccountReplicaSetReconciler) ensureControllerAccessManifestWork(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	placementRefName string,
) (*workv1.ManifestWork, error) {
	desired := buildControllerAccessManifestWork(set, clusterName, placementRefName)
	return ensureChild(ctx, r, set, desired, &workv1.ManifestWork{}, manifestWorkSpecOps)
}

func desiredManagedServiceAccountNames(set *authv1alpha1.ManagedServiceAccountReplicaSet) sets.Set[string] {
	names := sets.New(set.Spec.Template.Metadata.Name)
	if selectorRBACEnabled(set) {
		names.Insert(controllerAccessManagedServiceAccountName(set))
	}
	return names
}
