// Package v1alpha1 contains API schema definitions for the authentication.appthrust.io API group.
// +kubebuilder:object:generate=true
// +groupName=authentication.appthrust.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// Group is the Kubernetes API group.
	Group = "authentication.appthrust.io"
	// Version is the Kubernetes API version.
	Version = "v1alpha1"
)

var (
	// GroupVersion identifies this API group and version.
	GroupVersion = schema.GroupVersion{Group: Group, Version: Version}
	// SchemeBuilder registers this API group with a runtime scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds this API group to a runtime scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		GroupVersion,
		&ManagedServiceAccountReplicaSet{},
		&ManagedServiceAccountReplicaSetList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
