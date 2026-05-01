package controller

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	clusterv1alpha1 "open-cluster-management.io/api/cluster/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
	msav1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	clustersdkv1alpha1 "open-cluster-management.io/sdk-go/pkg/apis/cluster/v1alpha1"
	civ1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/access"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type fakeNamespaceSelectorResolver struct {
	namespaces []string
	err        error
}

func (r *fakeNamespaceSelectorResolver) ResolveNamespaceSelector(
	context.Context,
	*authv1alpha1.ManagedServiceAccountReplicaSet,
	string,
	authv1alpha1.RBACGrant,
	labels.Selector,
) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]string(nil), r.namespaces...), nil
}

func TestReconcileCreatesMSAAndRBACManifestWorkSlicesFromOCMDecision(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.Template.Metadata.Labels = map[string]string{
		"team":      "platform",
		labelSetUID: "spoofed",
	}
	set.Spec.RBAC = &authv1alpha1.RBAC{
		Grants: []authv1alpha1.RBACGrant{{
			ID:   "read-secrets",
			Type: authv1alpha1.RBACGrantTypeRole,
			ForEachNamespace: &authv1alpha1.NamespaceIterator{
				Name: "app-a",
			},
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list"},
			}},
		}, {
			ID:       "read-nodes",
			Type:     authv1alpha1.RBACGrantTypeClusterRole,
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-nodes"},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get"},
			}},
		}},
	}
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	c := fakeClient(set, decision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}})
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	msa := &msav1beta1.ManagedServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: "msa"}, msa); err != nil {
		t.Fatalf("expected ManagedServiceAccount: %v", err)
	}
	if got := msa.Labels[labelSetUID]; got != string(set.UID) {
		t.Fatalf("source UID label = %q, want %q", got, set.UID)
	}
	if got := msa.Labels[labelPlacementRefName]; got != "placement-a" {
		t.Fatalf("placement ref label = %q, want placement-a", got)
	}
	if got := msa.Labels["team"]; got != "platform" {
		t.Fatalf("user label was not preserved: %q", got)
	}
	if got := msa.Labels[labelSyncToClusterProfile]; got != "true" {
		t.Fatalf("default sync label = %q, want true", got)
	}

	clusterWork := &workv1.ManifestWork{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: rbacClusterManifestWorkName(set)}, clusterWork); err != nil {
		t.Fatalf("expected cluster ManifestWork: %v", err)
	}
	if clusterWork.Spec.DeleteOption == nil || clusterWork.Spec.DeleteOption.PropagationPolicy != workv1.DeletePropagationPolicyTypeForeground {
		t.Fatalf("ManifestWork delete option = %#v, want Foreground", clusterWork.Spec.DeleteOption)
	}
	if len(clusterWork.Spec.Workload.Manifests) != 2 {
		t.Fatalf("cluster manifest count = %d, want ClusterRole/ClusterRoleBinding", len(clusterWork.Spec.Workload.Manifests))
	}
	namespaceWork := &workv1.ManifestWork{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: rbacNamespaceManifestWorkName(set, "app-a")}, namespaceWork); err != nil {
		t.Fatalf("expected namespace ManifestWork: %v", err)
	}
	if got := namespaceWork.Labels[labelSliceType]; got != rbacNamespaceSliceType {
		t.Fatalf("namespace slice label = %q, want %q", got, rbacNamespaceSliceType)
	}
	if got := namespaceWork.Annotations[annotationTargetNamespace]; got != "app-a" {
		t.Fatalf("target namespace annotation = %q, want app-a", got)
	}
	if len(namespaceWork.Spec.Workload.Manifests) != 2 {
		t.Fatalf("namespace manifest count = %d, want Role/RoleBinding", len(namespaceWork.Spec.Workload.Manifests))
	}
	subject := manifestWorkSubject(t, namespaceWork)
	if subject.Name != "msa" || subject.Namespace != "open-cluster-management-managed-serviceaccount" {
		t.Fatalf("subject = %s/%s, want open-cluster-management-managed-serviceaccount/msa", subject.Namespace, subject.Name)
	}
}

func TestExplicitSyncLabelIsPreservedAndEmptyRBACSkipsManifestWork(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.Template.Metadata.Labels = map[string]string{labelSyncToClusterProfile: "false"}
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	c := fakeClient(set, decision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}})
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	msa := &msav1beta1.ManagedServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: "msa"}, msa); err != nil {
		t.Fatalf("expected MSA: %v", err)
	}
	if got := msa.Labels[labelSyncToClusterProfile]; got != "false" {
		t.Fatalf("sync label = %q, want explicit false", got)
	}
	work := &workv1.ManifestWork{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: rbacClusterManifestWorkName(set)}, work); err == nil {
		t.Fatalf("unexpected ManifestWork when spec.rbac is empty")
	}
}

func TestPlacementObjectIsRequiredForOCMSDKResolution(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithStatusSubresource(&authv1alpha1.ManagedServiceAccountReplicaSet{}).
		WithObjects(set, decision).
		Build()
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
		t.Fatalf("get status: %v", err)
	}
	if len(stored.Status.Placements) != 1 {
		t.Fatalf("placements = %d, want 1", len(stored.Status.Placements))
	}
	resolved := meta.FindStatusCondition(stored.Status.Placements[0].Conditions, conditionPlacementResolved)
	if resolved == nil || resolved.Status != metav1.ConditionFalse || resolved.Reason != reasonPlacementUnavailable {
		t.Fatalf("PlacementResolved condition = %#v, want False/%s when OCM Placement is missing", resolved, reasonPlacementUnavailable)
	}
}

func TestProgressiveRolloutLimitsInitialApplyAndAdvancesAfterSuccess(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.PlacementRefs[0].RolloutStrategy = clusterv1alpha1.RolloutStrategy{
		Type: clusterv1alpha1.Progressive,
		Progressive: &clusterv1alpha1.RolloutProgressive{
			MaxConcurrency: intstr.FromInt32(1),
		},
	}
	c := fakeClient(
		set,
		testOCMDecisionWithClusters("placement-a", "tenant", "placement-a-decision", "cluster-a", "cluster-b"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-b"}},
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: "msa"}, &msav1beta1.ManagedServiceAccount{}); err != nil {
		t.Fatalf("expected first rollout cluster MSA: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-b", Name: "msa"}, &msav1beta1.ManagedServiceAccount{}); err == nil {
		t.Fatalf("cluster-b MSA was created before cluster-a succeeded")
	}
	first := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), first); err != nil {
		t.Fatalf("get first status: %v", err)
	}
	if first.Status.Rollout == nil || first.Status.Rollout.Total != 2 || first.Status.Rollout.Updating != 1 ||
		first.Status.Rollout.Succeeded != 0 || first.Status.Rollout.Failed != 0 || first.Status.Rollout.TimedOut != 0 {
		t.Fatalf("first rollout summary = %#v, want total=2 updating=1 succeeded=0 failed=0 timedOut=0", first.Status.Rollout)
	}
	progressing := meta.FindStatusCondition(first.Status.Rollout.Conditions, conditionProgressing)
	if progressing == nil || progressing.Status != metav1.ConditionTrue || progressing.Reason != reasonProgressing {
		t.Fatalf("first rollout Progressing condition = %#v, want True/%s", progressing, reasonProgressing)
	}
	rolledOut := meta.FindStatusCondition(first.Status.Conditions, conditionPlacementRolledOut)
	if rolledOut == nil || rolledOut.Status != metav1.ConditionFalse || rolledOut.Reason != reasonProgressing {
		t.Fatalf("first PlacementRolledOut condition = %#v, want False/%s", rolledOut, reasonProgressing)
	}
	if len(first.Status.Placements) != 1 || first.Status.Placements[0].Rollout == nil ||
		first.Status.Placements[0].Rollout.Total != 2 || first.Status.Placements[0].Rollout.Updating != 1 {
		t.Fatalf("first placement rollout = %#v, want total=2 updating=1", first.Status.Placements)
	}
	if got, want := first.Status.Placements[0].AvailableDecisionGroups, "1 (1 / 2 clusters applied)"; got != want {
		t.Fatalf("availableDecisionGroups = %q, want %q", got, want)
	}

	clusterA := &msav1beta1.ManagedServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: "msa"}, clusterA); err != nil {
		t.Fatalf("get cluster-a MSA: %v", err)
	}
	clusterA.Status.Conditions = []metav1.Condition{{
		Type:               msav1beta1.ConditionTypeTokenReported,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: clusterA.Generation,
		Reason:             "Ready",
		Message:            "token reported",
		LastTransitionTime: metav1.Now(),
	}}
	if err := c.Update(ctx, clusterA); err != nil {
		t.Fatalf("mark cluster-a MSA ready: %v", err)
	}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-b", Name: "msa"}, &msav1beta1.ManagedServiceAccount{}); err != nil {
		t.Fatalf("expected second rollout cluster MSA after cluster-a success: %v", err)
	}
}

func TestManifestWorkRolloutStatusMatchesOCMGenerationGates(t *testing.T) {
	now := metav1.Now()
	condition := func(conditionType string, status metav1.ConditionStatus, generation int64) metav1.Condition {
		return metav1.Condition{
			Type:               conditionType,
			Status:             status,
			ObservedGeneration: generation,
			Reason:             "Test",
			Message:            "test",
			LastTransitionTime: now,
		}
	}
	cases := []struct {
		name       string
		conditions []metav1.Condition
		want       clustersdkv1alpha1.RolloutStatus
	}{
		{
			name: "missing applied stays ToApply",
			conditions: []metav1.Condition{
				condition(workv1.WorkProgressing, metav1.ConditionFalse, 2),
			},
			want: clustersdkv1alpha1.ToApply,
		},
		{
			name: "stale progressing stays ToApply",
			conditions: []metav1.Condition{
				condition(workv1.WorkApplied, metav1.ConditionTrue, 2),
				condition(workv1.WorkProgressing, metav1.ConditionFalse, 1),
			},
			want: clustersdkv1alpha1.ToApply,
		},
		{
			name: "progressing true",
			conditions: []metav1.Condition{
				condition(workv1.WorkApplied, metav1.ConditionTrue, 2),
				condition(workv1.WorkProgressing, metav1.ConditionTrue, 2),
			},
			want: clustersdkv1alpha1.Progressing,
		},
		{
			name: "progressing true and degraded true fails",
			conditions: []metav1.Condition{
				condition(workv1.WorkApplied, metav1.ConditionTrue, 2),
				condition(workv1.WorkProgressing, metav1.ConditionTrue, 2),
				condition(workv1.WorkDegraded, metav1.ConditionTrue, 2),
			},
			want: clustersdkv1alpha1.Failed,
		},
		{
			name: "progressing false succeeds even with degraded true",
			conditions: []metav1.Condition{
				condition(workv1.WorkApplied, metav1.ConditionTrue, 2),
				condition(workv1.WorkProgressing, metav1.ConditionFalse, 2),
				condition(workv1.WorkDegraded, metav1.ConditionTrue, 2),
			},
			want: clustersdkv1alpha1.Succeeded,
		},
		{
			name: "stale degraded stays ToApply",
			conditions: []metav1.Condition{
				condition(workv1.WorkApplied, metav1.ConditionTrue, 2),
				condition(workv1.WorkProgressing, metav1.ConditionTrue, 2),
				condition(workv1.WorkDegraded, metav1.ConditionTrue, 1),
			},
			want: clustersdkv1alpha1.ToApply,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := &workv1.ManifestWork{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Status:     workv1.ManifestWorkStatus{Conditions: tc.conditions},
			}
			got, _ := manifestWorkRolloutStatus(work)
			if got != tc.want {
				t.Fatalf("manifestWorkRolloutStatus = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRolloutSummaryReportsOCMStyleCounts(t *testing.T) {
	summary := newRolloutSummary(
		4,
		setsOf("cluster-a"),
		setsOf("cluster-d"),
		map[string]clustersdkv1alpha1.ClusterRolloutStatus{
			"cluster-a": {ClusterName: "cluster-a", Status: clustersdkv1alpha1.ToApply},
			"cluster-b": {ClusterName: "cluster-b", Status: clustersdkv1alpha1.Succeeded},
			"cluster-c": {ClusterName: "cluster-c", Status: clustersdkv1alpha1.Failed},
			"cluster-d": {ClusterName: "cluster-d", Status: clustersdkv1alpha1.TimeOut},
		},
		nil,
		7,
	)
	if summary.Updating != 1 || summary.Succeeded != 1 || summary.Failed != 1 || summary.TimedOut != 1 {
		t.Fatalf("summary counts = updating=%d succeeded=%d failed=%d timedOut=%d, want 1/1/1/1",
			summary.Updating, summary.Succeeded, summary.Failed, summary.TimedOut)
	}
	condition := meta.FindStatusCondition(summary.Conditions, conditionProgressing)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != reasonProgressing ||
		condition.Message != "selected clusters 4. managed service accounts 2/4 progressing..., 1 failed 1 timeout." {
		t.Fatalf("progressing condition = %#v", condition)
	}

	completed := newRolloutSummary(
		2,
		setsOf(),
		setsOf(),
		map[string]clustersdkv1alpha1.ClusterRolloutStatus{
			"cluster-a": {ClusterName: "cluster-a", Status: clustersdkv1alpha1.Succeeded},
			"cluster-b": {ClusterName: "cluster-b", Status: clustersdkv1alpha1.Succeeded},
		},
		nil,
		7,
	)
	condition = meta.FindStatusCondition(completed.Conditions, conditionProgressing)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != reasonCompleted ||
		condition.Message != "selected clusters 2. managed service accounts 2/2 completed with no errors, 0 failed 0 timeout." {
		t.Fatalf("completed condition = %#v", condition)
	}

	status := authv1alpha1.ManagedServiceAccountReplicaSetStatus{}
	applyPlacementRolledOutCondition(&status, completed, 7)
	rolledOut := meta.FindStatusCondition(status.Conditions, conditionPlacementRolledOut)
	if rolledOut == nil || rolledOut.Status != metav1.ConditionTrue || rolledOut.Reason != reasonComplete {
		t.Fatalf("PlacementRolledOut condition = %#v, want True/%s", rolledOut, reasonComplete)
	}
}

func TestOverlappingPlacementRefsAggregateSummaryForEachRef(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.PlacementRefs = []authv1alpha1.PlacementRef{
		{Name: "placement-a"},
		{Name: "placement-b"},
	}
	set.Spec.RBAC = &authv1alpha1.RBAC{
		Grants: []authv1alpha1.RBACGrant{{
			ID:       "read-nodes",
			Type:     authv1alpha1.RBACGrantTypeClusterRole,
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-nodes"},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get"},
			}},
		}},
	}
	c := fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		testOCMDecision("placement-b", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
		t.Fatalf("get status: %v", err)
	}
	if stored.Status.SelectedClusterCount != 1 {
		t.Fatalf("selected clusters = %d, want deduplicated count 1", stored.Status.SelectedClusterCount)
	}
	for _, placement := range stored.Status.Placements {
		if placement.Summary == nil || placement.Summary.DesiredTotal != 1 || placement.Summary.Total != 1 {
			t.Fatalf("placement %s summary = %#v, want desired/total 1", placement.Name, placement.Summary)
		}
	}
}

func TestNamespaceGrantListDeletesStaleNamespaceSliceWhenListShrinks(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{
		Grants: []authv1alpha1.RBACGrant{{
			ID:   "read-secrets",
			Type: authv1alpha1.RBACGrantTypeRole,
			ForEachNamespace: &authv1alpha1.NamespaceIterator{
				Names: []string{"app-a", "app-b"},
			},
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get"},
			}},
		}},
	}
	c := &recordingClient{Client: fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
	)}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	for _, namespace := range []string{"app-a", "app-b"} {
		work := &workv1.ManifestWork{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: rbacNamespaceManifestWorkName(set, namespace)}, work); err != nil {
			t.Fatalf("expected namespace slice for %s: %v", namespace, err)
		}
	}

	stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
		t.Fatalf("get set: %v", err)
	}
	base := stored.DeepCopy()
	stored.Spec.RBAC.Grants[0].ForEachNamespace.Names = []string{"app-a"}
	if err := c.Patch(ctx, stored, client.MergeFrom(base)); err != nil {
		t.Fatalf("patch set: %v", err)
	}
	c.deletes = nil

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	wantDelete := "ManifestWork/cluster-a/" + rbacNamespaceManifestWorkName(set, "app-b")
	if len(c.deletes) != 1 || c.deletes[0] != wantDelete {
		t.Fatalf("delete calls = %v, want %s", c.deletes, wantDelete)
	}
}

func TestValidateRuntimeSpecRejectsInvalidGrants(t *testing.T) {
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{
		{
			ID:       "duplicate",
			Type:     authv1alpha1.RBACGrantTypeClusterRole,
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-nodes"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get"}}},
		},
		{
			ID:       "duplicate",
			Type:     authv1alpha1.RBACGrantTypeClusterRole,
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-pods"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"}}},
		},
	}}
	if err := validateRuntimeSpec(set); err == nil {
		t.Fatalf("expected duplicate grant ID to be rejected")
	}

	set = testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
		ID:   "bad-metadata",
		Type: authv1alpha1.RBACGrantTypeRole,
		ForEachNamespace: &authv1alpha1.NamespaceIterator{
			Name: "app-a",
		},
		Metadata: authv1alpha1.RBACGrantMetadata{
			Name:   "read-secrets",
			Labels: map[string]string{labelSyncToClusterProfile: "true"},
		},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}}}
	if err := validateRuntimeSpec(set); err == nil {
		t.Fatalf("expected reserved grant metadata key to be rejected")
	}

	set = testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{
		{
			ID:   "read-secrets-a",
			Type: authv1alpha1.RBACGrantTypeRole,
			ForEachNamespace: &authv1alpha1.NamespaceIterator{
				Name: "app-a",
			},
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
		},
		{
			ID:   "read-secrets-b",
			Type: authv1alpha1.RBACGrantTypeRole,
			ForEachNamespace: &authv1alpha1.NamespaceIterator{
				Names: []string{"app-a", "app-b"},
			},
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"list"}}},
		},
	}}
	if err := validateRuntimeSpec(set); err == nil {
		t.Fatalf("expected duplicate generated Role name to be rejected")
	}
}

func TestSelectorGrantCreatesControllerAccessBootstrapAndWaitsForResolver(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
		ID:   "selected-secrets",
		Type: authv1alpha1.RBACGrantTypeRole,
		ForEachNamespace: &authv1alpha1.NamespaceIterator{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
		},
		Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
		Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}}}
	c := fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err == nil {
		t.Fatalf("expected reconcile to wait for selector resolver")
	}
	accessMSA := &msav1beta1.ManagedServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: controllerAccessManagedServiceAccountName(set)}, accessMSA); err != nil {
		t.Fatalf("expected controller-access ManagedServiceAccount: %v", err)
	}
	accessWork := &workv1.ManifestWork{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: controllerAccessManifestWorkName(set)}, accessWork); err != nil {
		t.Fatalf("expected controller-access ManifestWork: %v", err)
	}
	if len(accessWork.Spec.Workload.Manifests) != 2 {
		t.Fatalf("controller-access manifest count = %d, want ClusterRole/ClusterRoleBinding", len(accessWork.Spec.Workload.Manifests))
	}
	if got := accessWork.Labels[labelSliceType]; got != controllerAccessRBACSliceType {
		t.Fatalf("controller-access slice label = %q, want %q", got, controllerAccessRBACSliceType)
	}
}

func TestAccessConfigUsesClusterProfileNamespaceForSyncedCredentials(t *testing.T) {
	cfg := access.New([]access.Provider{{
		Name: clusterProfileAccessProviderName,
		ExecConfig: &clientcmdapi.ExecConfig{
			Command: "cp-creds",
			Args:    []string{"--existing"},
			Env: []clientcmdapi.ExecEnvVar{{
				Name:  "NAMESPACE",
				Value: "old",
			}},
		},
	}})

	out, err := accessConfigForManagedServiceAccount(cfg, "set-controller-access", "clusterprofile-ns")
	if err != nil {
		t.Fatalf("access config failed: %v", err)
	}
	provider := out.Providers[0]
	if got := provider.ExecConfig.Args; len(got) != 2 || got[1] != "--managed-serviceaccount=set-controller-access" {
		t.Fatalf("args = %#v, want managed-serviceaccount injection", got)
	}
	if got := provider.ExecConfig.Env; len(got) != 1 || got[0].Name != "NAMESPACE" || got[0].Value != "clusterprofile-ns" {
		t.Fatalf("env = %#v, want NAMESPACE=clusterprofile-ns", got)
	}
	if got := cfg.Providers[0].ExecConfig.Env[0].Value; got != "old" {
		t.Fatalf("input config mutated NAMESPACE to %q", got)
	}
}

func TestClusterProfileLookupPrefersReplicaSetNamespaceWhenOCMCreatesDuplicates(t *testing.T) {
	ctx := context.Background()
	resolver := &ClusterInventoryNamespaceResolver{
		LocalClient: fakeClient(
			testClusterProfile("open-cluster-management-addon", "cluster-a", "cluster-a"),
			testClusterProfile("tenant", "cluster-a", "cluster-a"),
		),
	}

	profile, err := resolver.clusterProfileForOCMCluster(ctx, "tenant", "cluster-a")
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if profile.Namespace != "tenant" || profile.Name != "cluster-a" {
		t.Fatalf("profile = %s/%s, want tenant/cluster-a", profile.Namespace, profile.Name)
	}
}

func TestSelectorGrantResolverCreatesAndCleansNamespaceSlices(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
		ID:   "selected-secrets",
		Type: authv1alpha1.RBACGrantTypeRole,
		ForEachNamespace: &authv1alpha1.NamespaceIterator{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
		},
		Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
		Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}}}
	resolver := &fakeNamespaceSelectorResolver{namespaces: []string{"app-a", "app-b"}}
	c := &recordingClient{Client: fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
	)}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme(), NamespaceSelectorResolver: resolver}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	for _, namespace := range []string{"app-a", "app-b"} {
		work := &workv1.ManifestWork{}
		if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: rbacNamespaceManifestWorkName(set, namespace)}, work); err != nil {
			t.Fatalf("expected selector namespace slice for %s: %v", namespace, err)
		}
	}

	resolver.namespaces = []string{"app-a"}
	c.deletes = nil
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	wantDelete := "ManifestWork/cluster-a/" + rbacNamespaceManifestWorkName(set, "app-b")
	if len(c.deletes) != 1 || c.deletes[0] != wantDelete {
		t.Fatalf("delete calls = %v, want %s", c.deletes, wantDelete)
	}
}

func TestPlacementStatusConditionsPreserveLastTransitionTime(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
		ID:   "selected-secrets",
		Type: authv1alpha1.RBACGrantTypeRole,
		ForEachNamespace: &authv1alpha1.NamespaceIterator{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
		},
		Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
		Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}}}
	resolver := &fakeNamespaceSelectorResolver{namespaces: []string{"app-a"}}
	c := fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme(), NamespaceSelectorResolver: resolver}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	first := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), first); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}
	if len(first.Status.Placements) != 1 {
		t.Fatalf("placements after first reconcile = %d, want 1", len(first.Status.Placements))
	}
	firstPlacementResolved := meta.FindStatusCondition(first.Status.Placements[0].Conditions, conditionPlacementResolved)
	if firstPlacementResolved == nil {
		t.Fatalf("expected PlacementResolved condition on first reconcile")
	}
	if firstPlacementResolved.LastTransitionTime.IsZero() {
		t.Fatalf("expected non-zero LastTransitionTime on first reconcile")
	}
	if first.Status.ControllerAccess == nil {
		t.Fatalf("expected ControllerAccess status on first reconcile")
	}
	firstAccessReady := meta.FindStatusCondition(first.Status.ControllerAccess.Conditions, conditionReady)
	if firstAccessReady == nil {
		t.Fatalf("expected ControllerAccess Ready condition on first reconcile")
	}
	if firstAccessReady.LastTransitionTime.IsZero() {
		t.Fatalf("expected non-zero LastTransitionTime on ControllerAccess Ready first reconcile")
	}

	// metav1.Time has second-level precision; sleep > 1s so a re-stamp would round to a different value.
	time.Sleep(1100 * time.Millisecond)
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	second := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), second); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}
	secondPlacementResolved := meta.FindStatusCondition(second.Status.Placements[0].Conditions, conditionPlacementResolved)
	if secondPlacementResolved == nil {
		t.Fatalf("expected PlacementResolved condition on second reconcile")
	}
	if !secondPlacementResolved.LastTransitionTime.Equal(&firstPlacementResolved.LastTransitionTime) {
		t.Fatalf("PlacementStatus PlacementResolved LastTransitionTime: first=%s second=%s; want preserved (status did not transition)",
			firstPlacementResolved.LastTransitionTime, secondPlacementResolved.LastTransitionTime)
	}
	if second.Status.ControllerAccess == nil {
		t.Fatalf("expected ControllerAccess status on second reconcile")
	}
	secondAccessReady := meta.FindStatusCondition(second.Status.ControllerAccess.Conditions, conditionReady)
	if secondAccessReady == nil {
		t.Fatalf("expected ControllerAccess Ready condition on second reconcile")
	}
	if !secondAccessReady.LastTransitionTime.Equal(&firstAccessReady.LastTransitionTime) {
		t.Fatalf("ControllerAccessStatus Ready LastTransitionTime: first=%s second=%s; want preserved (status did not transition)",
			firstAccessReady.LastTransitionTime, secondAccessReady.LastTransitionTime)
	}
}

// TestClusterRolloutStatusPropagatesResolverError pins that
// clusterRolloutStatus propagates resolver errors from the desiredRBACSlices
// branch to the caller, so the workqueue requeues with backoff. Returning
// (status, nil) would silently swallow transient resolver errors and report
// the cluster as Progressing forever without Event/backoff, because
// controller-runtime skips exponential requeue on nil err.
//
// The test reaches the resolver branch by hand-stamping a fully-Ready primary
// MSA, fully-Ready controller-access MSA, and Available controller-access
// ManifestWork in cluster-a so traversal advances past the rbacEnabled gate.
func TestClusterRolloutStatusPropagatesResolverError(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
		ID:   "selected-secrets",
		Type: authv1alpha1.RBACGrantTypeRole,
		ForEachNamespace: &authv1alpha1.NamespaceIterator{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
		},
		Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
		Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}}}
	if !selectorRBACEnabled(set) || !rbacEnabled(set) {
		t.Fatalf("test precondition: selectorRBACEnabled=%t rbacEnabled=%t, both want true", selectorRBACEnabled(set), rbacEnabled(set))
	}

	now := metav1.Now()

	// Primary MSA: identical to desired so childMatches returns true, plus
	// ConditionTypeTokenReported=True so managedServiceAccountReady -> Succeeded.
	primaryMSA := buildManagedServiceAccount(set, "cluster-a", "placement-a")
	primaryMSA.Status.Conditions = []metav1.Condition{{
		Type:               msav1beta1.ConditionTypeTokenReported,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: primaryMSA.Generation,
		Reason:             "Ready",
		Message:            "token reported",
		LastTransitionTime: now,
	}}

	// Controller-access MSA: same shape, hand-stamped Ready.
	accessMSA := buildControllerAccessManagedServiceAccount(set, "cluster-a", "placement-a")
	accessMSA.Status.Conditions = []metav1.Condition{{
		Type:               msav1beta1.ConditionTypeTokenReported,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: accessMSA.Generation,
		Reason:             "Ready",
		Message:            "token reported",
		LastTransitionTime: now,
	}}

	// Controller-access ManifestWork: childMatches via buildControllerAccessManifestWork,
	// plus Applied=True / Progressing=False / Available=True with ObservedGeneration=1
	// (fake client sets Generation=1 on Create) so manifestWorkRolloutStatus -> Succeeded.
	// Canonicalize via a throwaway fake-client round-trip so Workload.Manifests
	// reach the same Raw shape both copies exhibit on read; otherwise childMatches
	// would diverge on RawExtension representation and stop traversal early.
	accessWork := canonicalizeViaFakeClient(t, buildControllerAccessManifestWork(set, "cluster-a", "placement-a"))
	accessWork.Generation = 1
	accessWork.Status.Conditions = []metav1.Condition{
		{
			Type:               workv1.WorkApplied,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			Reason:             "Applied",
			Message:            "applied",
			LastTransitionTime: now,
		},
		{
			Type:               workv1.WorkProgressing,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: 1,
			Reason:             "Done",
			Message:            "done",
			LastTransitionTime: now,
		},
		{
			Type:               workv1.WorkAvailable,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: 1,
			Reason:             "Available",
			Message:            "available",
			LastTransitionTime: now,
		},
	}

	errSentinel := errors.New("resolver: cache not synced")
	resolver := &fakeNamespaceSelectorResolver{err: errSentinel}

	c := fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		primaryMSA,
		accessMSA,
		accessWork,
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{
		Client:                    c,
		APIReader:                 c,
		Scheme:                    testScheme(),
		NamespaceSelectorResolver: resolver,
	}

	status, err := reconciler.clusterRolloutStatus(ctx, set, "cluster-a", "placement-a")
	if err == nil {
		t.Fatalf("clusterRolloutStatus err = nil; want propagated resolver error so workqueue requeues with backoff")
	}
	if !errors.Is(err, errSentinel) && !strings.Contains(err.Error(), errSentinel.Error()) {
		t.Fatalf("clusterRolloutStatus err = %v; want chain containing %v", err, errSentinel)
	}
	if status.Status != clustersdkv1alpha1.Progressing {
		t.Fatalf("clusterRolloutStatus status.Status = %v, want Progressing (status field must still be populated alongside the err)", status.Status)
	}
}

// TestClusterRolloutStatusPrimaryConflictReturnsFailedStatus pins that when the
// primary ManagedServiceAccount on a cluster carries labels that don't match
// ownedSelector(set) (foreign / hand-edited / collision with another set), the
// returned ClusterRolloutStatus advertises Failed alongside the err that wraps
// errChildConflict. The Status field must be populated because planRollout
// swallows errChildConflict so the rollout summary, per-placement summary,
// and downstream cluster status updates keep progressing on the OTHER clusters
// in the same parent. Without the Failed stamp the conflict cluster would
// silently look like ToApply forever.
func TestClusterRolloutStatusPrimaryConflictReturnsFailedStatus(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()

	// MSA exists at the desired key but its labels do not contain the owner
	// label set, so ownedBySet returns false. This is the canonical "foreign"
	// MSA case: another controller / a manual kubectl apply / a stale set with
	// a different UID created the object first.
	foreignMSA := buildManagedServiceAccount(set, "cluster-a", "placement-a")
	foreignMSA.Labels = map[string]string{"owner": "someone-else"}

	c := fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		foreignMSA,
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	status, err := reconciler.clusterRolloutStatus(ctx, set, "cluster-a", "placement-a")
	if err == nil {
		t.Fatalf("clusterRolloutStatus err = nil; want err wrapping errChildConflict")
	}
	if !errors.Is(err, errChildConflict) {
		t.Fatalf("clusterRolloutStatus err = %v; want errors.Is(err, errChildConflict)", err)
	}
	if status.Status != clustersdkv1alpha1.Failed {
		t.Fatalf("clusterRolloutStatus status.Status = %v, want Failed (so rollout summary records a failed cluster when planRollout swallows errChildConflict)", status.Status)
	}
	if status.ClusterName != "cluster-a" {
		t.Fatalf("clusterRolloutStatus status.ClusterName = %q, want %q", status.ClusterName, "cluster-a")
	}
}

// TestPlanRolloutSwallowsChildConflictAndAnnotatesFailed pins that planRollout
// does NOT propagate errChildConflict as a fatal error. The conflict is a
// per-cluster outcome — reconcileChildren observes it independently via
// ensureChild → recordErr and surfaces it on the parent condition. If
// planRollout aborted on errChildConflict the entire Reconcile would short-
// circuit before patchStatus, dropping status updates for OTHER clusters in
// the same parent and skipping deleteStaleChildren. controller-runtime's
// Reconcile contract (pkg/reconcile/reconcile.go: "all state for that given
// root object") demands the controller keeps going.
func TestPlanRolloutSwallowsChildConflictAndAnnotatesFailed(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()

	foreignMSA := buildManagedServiceAccount(set, "cluster-a", "placement-a")
	foreignMSA.Labels = map[string]string{"owner": "someone-else"}

	c := fakeClient(
		set,
		testOCMDecision("placement-a", "tenant", "cluster-a"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		foreignMSA,
	)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	resolution := reconciler.resolvePlacementRefs(ctx, set, set.Generation)
	if resolution.err != nil {
		t.Fatalf("resolvePlacementRefs err = %v, want nil", resolution.err)
	}
	if got, want := len(resolution.clusters), 1; got != want {
		t.Fatalf("resolved clusters = %d, want %d", got, want)
	}

	plan, err := reconciler.planRollout(ctx, set, resolution)
	if err != nil {
		t.Fatalf("planRollout err = %v; want nil (errChildConflict is per-cluster, not transport)", err)
	}
	got, ok := plan.statuses["cluster-a"]
	if !ok {
		t.Fatalf("plan.statuses missing cluster-a; want a status entry carrying Failed")
	}
	if got.Status != clustersdkv1alpha1.Failed {
		t.Fatalf("plan.statuses[cluster-a].Status = %v, want Failed", got.Status)
	}
}

// TestReconcileWithChildConflictKeepsCleanCluster pins the end-to-end contract
// that a per-cluster errChildConflict does not abort the whole Reconcile.
// Cluster A has a foreign-labeled MSA → conflict. Cluster B is clean. Plus a
// stale ManifestWork in cluster-c (not selected by placement-a) that
// deleteStaleChildren must clean up in the same reconcile pass.
//
// On a single Reconcile call:
//   - Reconcile returns nil err — recordErr swallows errChildConflict into
//     result.conflicts so the conflict surfaces via Ready=False/ChildConflict
//     (level-triggered) without rate-limited workqueue backoff for a
//     non-transient state. A subsequent watch event on the foreign object
//     resolving fires the next reconcile.
//   - status is patched. ManagedServiceAccountsReady reflects ChildConflict.
//     Ready itself is preempted by CleanupBlocked because the stale
//     ManifestWork in cluster-c triggers cleanupPending and the overlay
//     promotes the cleanup signal on Ready, while the more specific
//     ManagedServiceAccountsReady retains the conflict reason from
//     applyChildResultConditions.
//   - status.Rollout.Total == 2 and Rollout.Succeeded < Total, so the
//     conflict is not silently masked as Succeeded. The RolloutHandler
//     re-queues cluster-a's Failed status into ClustersToRollout (it retries
//     on the next reconcile), landing it in Updating rather than Failed for
//     newRolloutSummary's bookkeeping; what matters is Progressing stays
//     True, not which sub-bucket cluster-a falls in.
//   - The stale ManifestWork in cluster-c is deleted in the same pass.
func TestReconcileWithChildConflictKeepsCleanCluster(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecisionWithClusters("placement-a", "tenant", "placement-a-decision", "cluster-a", "cluster-b")

	// cluster-a: foreign-labeled MSA → ensureManagedServiceAccount returns
	// errChildConflict from the ownedBySet branch in ensureChild.
	foreignMSA := buildManagedServiceAccount(set, "cluster-a", "placement-a")
	foreignMSA.Labels = map[string]string{"owner": "someone-else"}

	// Stale ManifestWork in cluster-c (not selected by placement-a) so
	// deleteStaleChildren has something to delete. Labels match sourceLabels(set)
	// so the owner-label selector finds it.
	staleWork := &workv1.ManifestWork{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbacClusterManifestWorkName(set),
			Namespace: "cluster-c",
			Labels:    sourceLabels(set),
		},
	}

	c := &recordingClient{Client: fakeClient(
		set,
		decision,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-b"}},
		foreignMSA,
		staleWork,
	)}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("Reconcile err = %v; want nil — recordErr swallows errChildConflict into result.conflicts so the condition surfaces ChildConflict without rate-limited requeue", err)
	}

	stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
		t.Fatalf("get status: %v", err)
	}

	ready := meta.FindStatusCondition(stored.Status.Conditions, conditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("Ready condition = %#v, want False", ready)
	}
	// Ready's Reason is preempted by CleanupBlocked because deleteStaleChildren
	// reported a stale ManifestWork in cluster-c (cleanupPending=true), and the
	// post-applyChildResultConditions overlay rewrites Ready to CleanupBlocked.
	// This is intentional: in-flight cleanup is a higher-priority operational
	// signal than ChildConflict on Ready. The conflict is still surfaced via
	// the more specific ManagedServiceAccountsReady condition (set by
	// applyChildResultConditions before the overlay), which we assert below.
	if ready.Reason != reasonCleanupBlocked {
		t.Fatalf("Ready.Reason = %q, want %q (cleanupPending overlay preempts ChildConflict on Ready)",
			ready.Reason, reasonCleanupBlocked)
	}
	msaReady := meta.FindStatusCondition(stored.Status.Conditions, conditionManagedServiceAccountsReady)
	if msaReady == nil || msaReady.Status != metav1.ConditionFalse {
		t.Fatalf("ManagedServiceAccountsReady condition = %#v, want False", msaReady)
	}
	if msaReady.Reason != reasonChildConflict {
		t.Fatalf("ManagedServiceAccountsReady.Reason = %q, want %q (per-cluster conflict surfaced via reconcileChildren, NOT swallowed by planRollout)",
			msaReady.Reason, reasonChildConflict)
	}

	// cluster-b is clean and is not blocked by cluster-a's conflict; its MSA
	// must exist after one reconcile.
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-b", Name: "msa"}, &msav1beta1.ManagedServiceAccount{}); err != nil {
		t.Fatalf("expected cluster-b ManagedServiceAccount to be created despite cluster-a conflict: %v", err)
	}

	// Rollout summary covers both clusters; cluster-a is counted as Failed.
	if stored.Status.Rollout == nil {
		t.Fatalf("Status.Rollout = nil; want populated rollout summary")
	}
	if stored.Status.Rollout.Total != 2 {
		t.Fatalf("Status.Rollout.Total = %d, want 2", stored.Status.Rollout.Total)
	}
	// cluster-a's conflict surfaces in the rollout summary as a non-completed
	// rollout: the RolloutHandler re-includes the Failed-status cluster in
	// ClustersToRollout (it retries on the next reconcile), landing it in
	// Updating rather than Failed for newRolloutSummary's bookkeeping. What
	// matters is that the conflict is NOT silently masked as Succeeded —
	// Rollout.Succeeded must remain < Total so Progressing stays True.
	if stored.Status.Rollout.Succeeded == stored.Status.Rollout.Total {
		t.Fatalf("Status.Rollout.Succeeded = %d == Total; cluster-a conflict was silently masked as Succeeded", stored.Status.Rollout.Succeeded)
	}
	if stored.Status.Rollout.Updating+stored.Status.Rollout.Failed+stored.Status.Rollout.TimedOut < 1 {
		t.Fatalf("Status.Rollout = %+v; want at least one cluster reflected as Updating/Failed/TimedOut due to cluster-a conflict", stored.Status.Rollout)
	}

	// deleteStaleChildren must run in the same pass even when cluster-a has
	// a per-cluster conflict: the stale cluster-c ManifestWork is deleted.
	staleDeleted := false
	for _, d := range c.deletes {
		if d == "ManifestWork/cluster-c/"+rbacClusterManifestWorkName(set) {
			staleDeleted = true
			break
		}
	}
	if !staleDeleted {
		t.Fatalf("stale ManifestWork in cluster-c was not deleted; deletes=%v (deleteStaleChildren must run despite per-cluster conflict)", c.deletes)
	}
}

// TestReconcileWithChildConflictEmitsWarningEvent pins that a pure-conflict
// reconcile (no other errors) emits exactly one Warning event with
// reason=reasonChildConflict and action="ApplyChildren". Because recordErr
// routes errChildConflict into result.conflicts (NOT result.err), the
// existing reasonApplyFailed branch is skipped on conflict-only reconciles;
// without a dedicated conflict-event branch users would only see the
// ChildConflict signal via status.conditions, losing the Warning event.
func TestReconcileWithChildConflictEmitsWarningEvent(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")

	// Foreign-labeled MSA in cluster-a so ensureChild's ownership check
	// returns errChildConflict (the only error in this reconcile).
	foreignMSA := buildManagedServiceAccount(set, "cluster-a", "placement-a")
	foreignMSA.Labels = map[string]string{"owner": "someone-else"}

	c := fakeClient(
		set,
		decision,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		foreignMSA,
	)
	recorder := events.NewFakeRecorder(8)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme(), Recorder: recorder}

	result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)})
	if err != nil {
		t.Fatalf("Reconcile err = %v; want nil — recordErr swallows errChildConflict into result.conflicts", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("Reconcile result = %+v; want zero (pure-conflict path returns ctrl.Result{} without rate-limited requeue)", result)
	}

	var conflictEvents, applyFailedEvents int
	drain := time.After(100 * time.Millisecond)
draining:
	for {
		select {
		case ev := <-recorder.Events:
			switch {
			case strings.HasPrefix(ev, corev1.EventTypeWarning+" "+reasonChildConflict+" "):
				conflictEvents++
			case strings.HasPrefix(ev, corev1.EventTypeWarning+" "+reasonApplyFailed+" "):
				applyFailedEvents++
			}
		case <-drain:
			break draining
		}
	}
	if conflictEvents != 1 {
		t.Fatalf("Warning %s events observed = %d, want exactly 1 (conflict-only reconcile must emit a ChildConflict Warning event so operators see the signal beyond status.conditions)", reasonChildConflict, conflictEvents)
	}
	if applyFailedEvents != 0 {
		t.Fatalf("Warning %s events observed = %d, want 0 (ApplyFailed must not double-fire on a pure-conflict reconcile; condition reason is ChildConflict, not ApplyFailed)", reasonApplyFailed, applyFailedEvents)
	}
}

// TestAggregateExistingRBACSummaryDoesNotEmitWarningOnResolverError pins that
// non-rolling cluster RBAC summary aggregation does NOT emit Warning events
// when the namespace selector resolver fails. Per-cluster Warning events
// defeat event dedup (cluster-name-bearing messages never collapse), and
// actionable misconfigurations are already surfaced via the active-rollout
// path's reasonApplyFailed Warning event.
func TestAggregateExistingRBACSummaryDoesNotEmitWarningOnResolverError(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
		ID:   "selected-secrets",
		Type: authv1alpha1.RBACGrantTypeRole,
		ForEachNamespace: &authv1alpha1.NamespaceIterator{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
		},
		Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
		Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
	}}}

	resolver := &fakeNamespaceSelectorResolver{err: errors.New("resolver: cache not synced")}
	recorder := events.NewFakeRecorder(8)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{
		Client:                    fakeClient(set),
		APIReader:                 fakeClient(set),
		Scheme:                    testScheme(),
		Recorder:                  recorder,
		NamespaceSelectorResolver: resolver,
	}

	placementByKey := map[string]*authv1alpha1.PlacementStatus{"placement-a": {Name: "placement-a"}}
	placementRefKeys := []string{"placement-a"}
	status := authv1alpha1.ManagedServiceAccountReplicaSetStatus{}

	reconciler.aggregateExistingRBACSummary(ctx, set, "cluster-a", "placement-a", placementRefKeys, placementByKey, &status)

	var waitingForRBACEvents int
	drain := time.After(100 * time.Millisecond)
draining:
	for {
		select {
		case ev := <-recorder.Events:
			if strings.HasPrefix(ev, corev1.EventTypeWarning+" "+reasonWaitingForRBAC+" ") {
				waitingForRBACEvents++
			}
		case <-drain:
			break draining
		}
	}
	if waitingForRBACEvents != 0 {
		t.Fatalf("Warning %s events observed = %d, want 0: non-rolling cluster aggregation must not emit Warning events for transient resolver failures; per-cluster error messages defeat dedup and actionable misconfig is already surfaced via reasonApplyFailed on the active-rollout path", reasonWaitingForRBAC, waitingForRBACEvents)
	}
}

// TestAggregateExistingRBACSummaryAccumulatesDesiredTotalOnPartialResolverError
// pins the contract that when desiredRBACSlices returns a partial set of
// rbacSlices alongside a resolver error (e.g. a selector-targeted role grant
// fails while a cluster-role grant resolves successfully), the non-rolling
// summary aggregation MUST still contribute the resolved slices' count to
// status.Summary.DesiredTotal and to each placement's Summary.DesiredTotal.
//
// Silently dropping the cluster from DesiredTotal violates
// api-conventions.md L357-362 ("Conditions should complement more detailed
// information about the observed status of an object" — counts must reflect
// the full denominator) and the OCM ManifestWorkReplicaSet "continue +
// aggregate" precedent
// (refs/ocm/pkg/work/hub/controllers/manifestworkreplicasetcontroller/manifestworkreplicaset_deploy_reconcile.go).
// It would also mask degraded state (already-Succeeded clusters appear
// healthier than reality) for operators reading status.Summary.
func TestAggregateExistingRBACSummaryAccumulatesDesiredTotalOnPartialResolverError(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{
		{
			ID:       "read-nodes",
			Type:     authv1alpha1.RBACGrantTypeClusterRole,
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-nodes"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get"}}},
		},
		{
			ID:   "selected-secrets",
			Type: authv1alpha1.RBACGrantTypeRole,
			ForEachNamespace: &authv1alpha1.NamespaceIterator{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			},
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
		},
	}}

	resolver := &fakeNamespaceSelectorResolver{err: errors.New("resolver: cache not synced")}
	recorder := events.NewFakeRecorder(8)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{
		Client:                    fakeClient(set),
		APIReader:                 fakeClient(set),
		Scheme:                    testScheme(),
		Recorder:                  recorder,
		NamespaceSelectorResolver: resolver,
	}

	placementByKey := map[string]*authv1alpha1.PlacementStatus{"placement-a": {Name: "placement-a"}}
	placementRefKeys := []string{"placement-a"}
	status := authv1alpha1.ManagedServiceAccountReplicaSetStatus{}

	reconciler.aggregateExistingRBACSummary(ctx, set, "cluster-a", "placement-a", placementRefKeys, placementByKey, &status)

	if status.Summary == nil {
		t.Fatalf("status.Summary = nil, want non-nil with DesiredTotal contribution from cluster-role grant")
	}
	if got, want := status.Summary.DesiredTotal, int32(1); got != want {
		t.Fatalf("status.Summary.DesiredTotal = %d, want %d: cluster-role grant resolves unconditionally and must contribute even when a selector grant fails; silently skipping the whole cluster masks degraded state", got, want)
	}
	if placementByKey["placement-a"].Summary == nil {
		t.Fatalf("placementByKey[placement-a].Summary = nil, want partial DesiredTotal contribution")
	}
	if got, want := placementByKey["placement-a"].Summary.DesiredTotal, int32(1); got != want {
		t.Fatalf("placementByKey[placement-a].Summary.DesiredTotal = %d, want %d: per-placement Summary must mirror cluster-level partial aggregation", got, want)
	}

	var waitingForRBACEvents int
	drain := time.After(100 * time.Millisecond)
draining:
	for {
		select {
		case ev := <-recorder.Events:
			if strings.HasPrefix(ev, corev1.EventTypeWarning+" "+reasonWaitingForRBAC+" ") {
				waitingForRBACEvents++
			}
		case <-drain:
			break draining
		}
	}
	if waitingForRBACEvents != 0 {
		t.Fatalf("Warning %s events observed = %d, want 0: partial aggregation must preserve the no-Warning behavior pinned by TestAggregateExistingRBACSummaryDoesNotEmitWarningOnResolverError", reasonWaitingForRBAC, waitingForRBACEvents)
	}
}

func TestDeleteWaitsForManifestWorkBeforeDeletingManagedServiceAccount(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	now := metav1.NewTime(time.Now())
	set.DeletionTimestamp = &now
	work := &workv1.ManifestWork{ObjectMeta: metav1.ObjectMeta{Name: rbacClusterManifestWorkName(set), Namespace: "cluster-a", Labels: sourceLabels(set)}}
	msa := &msav1beta1.ManagedServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "msa", Namespace: "cluster-a", Labels: sourceLabels(set)}}
	c := &recordingClient{Client: fakeClient(set, work, msa)}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.reconcileDelete(ctx, set); err != nil {
		t.Fatalf("delete reconcile failed: %v", err)
	}
	if len(c.deletes) != 1 || c.deletes[0] != "ManifestWork/cluster-a/set-rbac-cluster" {
		t.Fatalf("delete calls = %v, want only ManifestWork before MSA", c.deletes)
	}
}

func TestStaleCleanupDeletesManifestWorkBeforeManagedServiceAccount(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	staleWork := &workv1.ManifestWork{ObjectMeta: metav1.ObjectMeta{Name: rbacClusterManifestWorkName(set), Namespace: "cluster-b", Labels: sourceLabels(set)}}
	staleMSA := &msav1beta1.ManagedServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "msa", Namespace: "cluster-b", Labels: sourceLabels(set)}}
	c := &recordingClient{Client: fakeClient(
		set,
		decision,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		staleWork,
		staleMSA,
	)}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(c.deletes) != 1 || c.deletes[0] != "ManifestWork/cluster-b/set-rbac-cluster" {
		t.Fatalf("delete calls = %v, want only stale ManifestWork before stale MSA", c.deletes)
	}
}

// TestReconcileRequeueSemantics pins the controller-runtime Reconciler contract
// (pkg/reconcile/reconcile.go: when err != nil, Result is ignored and the
// controller applies exponential backoff). The Reconcile tail must therefore
// keep RequeueAfter at zero whenever it returns a non-nil error, and only set
// RequeueAfter on the success branches that actually want fixed-interval
// polling. A previous version of this controller paired RequeueAfter with the
// child-reconcile error and silently lost the requeue interval; this test
// would have failed against that code on the selector-RBAC-with-error case
// because it would have observed RequeueAfter == selectorRequeueInterval
// instead of zero.
func TestReconcileRequeueSemantics(t *testing.T) {
	t.Run("child error with selector RBAC returns zero RequeueAfter", func(t *testing.T) {
		ctx := context.Background()
		// selector-targeted grant + nil resolver guarantees childResult.err != nil
		// while leaving selectorRBACEnabled(set) == true. This is the precise
		// shape that the pre-fix code handled wrong: it returned
		// (Result{RequeueAfter: selectorRequeueInterval}, err), but
		// controller-runtime drops Result on error so the requeue interval was
		// silently ignored.
		set := testReplicaSet()
		set.Spec.RBAC = &authv1alpha1.RBAC{Grants: []authv1alpha1.RBACGrant{{
			ID:   "selected-secrets",
			Type: authv1alpha1.RBACGrantTypeRole,
			ForEachNamespace: &authv1alpha1.NamespaceIterator{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
			},
			Metadata: authv1alpha1.RBACGrantMetadata{Name: "read-secrets"},
			Rules:    []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"}}},
		}}}
		c := fakeClient(
			set,
			testOCMDecision("placement-a", "tenant", "cluster-a"),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
		)
		// NamespaceSelectorResolver is intentionally nil so resolveGrantNamespaces
		// returns "selector-targeted RBAC grant ... is waiting for controller
		// namespace access", which lifts childResult.err to non-nil.
		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)})
		if err == nil {
			t.Fatalf("expected non-nil error from child reconcile path")
		}
		if result.RequeueAfter != 0 {
			t.Fatalf("RequeueAfter = %s, want 0 when err != nil (controller-runtime applies backoff); pre-fix code returned %s", result.RequeueAfter, selectorRequeueInterval)
		}
	})

	t.Run("clean reconcile with cleanup pending returns cleanupRequeueInterval and nil err", func(t *testing.T) {
		ctx := context.Background()
		// Stale ManifestWork in cluster-b (not selected by placement-a) makes
		// deleteStaleChildren report cleanupPending == true while
		// childResult.err stays nil. The Reconcile tail must surface the
		// cleanup poll interval together with a nil error so that
		// controller-runtime honors it.
		set := testReplicaSet()
		decision := testOCMDecision("placement-a", "tenant", "cluster-a")
		staleWork := &workv1.ManifestWork{ObjectMeta: metav1.ObjectMeta{Name: rbacClusterManifestWorkName(set), Namespace: "cluster-b", Labels: sourceLabels(set)}}
		staleMSA := &msav1beta1.ManagedServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "msa", Namespace: "cluster-b", Labels: sourceLabels(set)}}
		c := fakeClient(
			set,
			decision,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}},
			staleWork,
			staleMSA,
		)
		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)})
		if err != nil {
			t.Fatalf("expected nil error on cleanup-pending path, got %v", err)
		}
		if result.RequeueAfter != cleanupRequeueInterval {
			t.Fatalf("RequeueAfter = %s, want %s on cleanupPending+nil-err path", result.RequeueAfter, cleanupRequeueInterval)
		}
	})
}

type recordingClient struct {
	client.Client
	deletes []string
}

func (c *recordingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.deletes = append(c.deletes, objectKind(obj)+"/"+obj.GetNamespace()+"/"+obj.GetName())
	return c.Client.Delete(ctx, obj, opts...)
}

func objectKind(obj client.Object) string {
	switch obj.(type) {
	case *workv1.ManifestWork:
		return "ManifestWork"
	case *msav1beta1.ManagedServiceAccount:
		return "ManagedServiceAccount"
	default:
		return obj.GetObjectKind().GroupVersionKind().Kind
	}
}

func manifestWorkSubject(t *testing.T, work *workv1.ManifestWork) rbacv1.Subject {
	t.Helper()
	for _, manifest := range work.Spec.Workload.Manifests {
		obj := manifest.Object
		if obj == nil && len(manifest.Raw) > 0 {
			decoded, _, err := serializer.NewCodecFactory(testScheme()).UniversalDeserializer().Decode(manifest.Raw, nil, nil)
			if err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
			obj = decoded
		}
		switch binding := obj.(type) {
		case *rbacv1.ClusterRoleBinding:
			return binding.Subjects[0]
		case *rbacv1.RoleBinding:
			return binding.Subjects[0]
		}
	}
	t.Fatalf("binding manifest not found")
	return rbacv1.Subject{}
}

func testReplicaSet() *authv1alpha1.ManagedServiceAccountReplicaSet {
	return &authv1alpha1.ManagedServiceAccountReplicaSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: authv1alpha1.GroupVersion.String(),
			Kind:       "ManagedServiceAccountReplicaSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "set",
			Namespace:  "tenant",
			UID:        types.UID("set-uid"),
			Generation: 7,
			Finalizers: []string{finalizerName},
		},
		Spec: authv1alpha1.ManagedServiceAccountReplicaSetSpec{
			PlacementRefs: []authv1alpha1.PlacementRef{{
				Name: "placement-a",
			}},
			Template: authv1alpha1.ManagedServiceAccountTemplate{
				Metadata: authv1alpha1.ManagedServiceAccountTemplateMetadata{
					Name:      "msa",
					Namespace: "open-cluster-management-managed-serviceaccount",
				},
				Spec: authv1alpha1.ManagedServiceAccountTemplateSpec{
					Rotation: authv1alpha1.ManagedServiceAccountRotation{Enabled: ptr.To(true)},
				},
			},
		},
	}
}

func testOCMDecision(placementName, namespace, clusterName string) *clusterv1beta1.PlacementDecision {
	return testOCMDecisionWithClusters(placementName, namespace, placementName+"-decision", clusterName)
}

func testOCMDecisionWithClusters(placementName, namespace, name string, clusterNames ...string) *clusterv1beta1.PlacementDecision {
	decisions := make([]clusterv1beta1.ClusterDecision, 0, len(clusterNames))
	for _, clusterName := range clusterNames {
		decisions = append(decisions, clusterv1beta1.ClusterDecision{
			ClusterName: clusterName,
			Reason:      "Selected",
		})
	}
	return &clusterv1beta1.PlacementDecision{
		TypeMeta: metav1.TypeMeta{APIVersion: clusterv1beta1.GroupVersion.String(), Kind: "PlacementDecision"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				clusterv1beta1.PlacementLabel:          placementName,
				clusterv1beta1.DecisionGroupNameLabel:  "",
				clusterv1beta1.DecisionGroupIndexLabel: "0",
			},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: decisions,
		},
	}
}

func testOCMPlacementFromDecisions(name, namespace string, decisions []*clusterv1beta1.PlacementDecision) *clusterv1beta1.Placement {
	clusterNames := map[string]struct{}{}
	groupsByKey := map[string]*clusterv1beta1.DecisionGroupStatus{}
	for _, decision := range decisions {
		ensureTestOCMDecisionGroupLabels(decision)
		groupName := decision.Labels[clusterv1beta1.DecisionGroupNameLabel]
		groupIndex, err := strconv.Atoi(decision.Labels[clusterv1beta1.DecisionGroupIndexLabel])
		if err != nil {
			groupIndex = 0
		}
		groupKey := groupName + "/" + strconv.Itoa(groupIndex)
		group := groupsByKey[groupKey]
		if group == nil {
			group = &clusterv1beta1.DecisionGroupStatus{
				DecisionGroupName:  groupName,
				DecisionGroupIndex: int32(groupIndex),
			}
			groupsByKey[groupKey] = group
		}
		group.Decisions = append(group.Decisions, decision.Name)
		group.ClustersCount += int32(len(decision.Status.Decisions))
		for _, clusterDecision := range decision.Status.Decisions {
			clusterNames[clusterDecision.ClusterName] = struct{}{}
		}
	}

	groupKeys := make([]string, 0, len(groupsByKey))
	for key := range groupsByKey {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool {
		left := groupsByKey[groupKeys[i]]
		right := groupsByKey[groupKeys[j]]
		if left.DecisionGroupIndex != right.DecisionGroupIndex {
			return left.DecisionGroupIndex < right.DecisionGroupIndex
		}
		return left.DecisionGroupName < right.DecisionGroupName
	})
	decisionGroups := make([]clusterv1beta1.DecisionGroupStatus, 0, len(groupKeys))
	for _, key := range groupKeys {
		decisionGroups = append(decisionGroups, *groupsByKey[key])
	}

	return &clusterv1beta1.Placement{
		TypeMeta: metav1.TypeMeta{APIVersion: clusterv1beta1.GroupVersion.String(), Kind: "Placement"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Status: clusterv1beta1.PlacementStatus{
			NumberOfSelectedClusters: int32(len(clusterNames)),
			DecisionGroups:           decisionGroups,
		},
	}
}

func ensureTestOCMDecisionGroupLabels(decision *clusterv1beta1.PlacementDecision) {
	if decision.Labels == nil {
		decision.Labels = map[string]string{}
	}
	if _, ok := decision.Labels[clusterv1beta1.DecisionGroupNameLabel]; !ok {
		decision.Labels[clusterv1beta1.DecisionGroupNameLabel] = ""
	}
	if _, ok := decision.Labels[clusterv1beta1.DecisionGroupIndexLabel]; !ok {
		decision.Labels[clusterv1beta1.DecisionGroupIndexLabel] = "0"
	}
}

func testClusterProfile(namespace, name, clusterName string) *civ1alpha1.ClusterProfile {
	return &civ1alpha1.ClusterProfile{
		TypeMeta: metav1.TypeMeta{APIVersion: civ1alpha1.GroupVersion.String(), Kind: "ClusterProfile"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{ocmClusterNameLabel: clusterName},
		},
		Spec: civ1alpha1.ClusterProfileSpec{ClusterManager: civ1alpha1.ClusterManager{Name: "open-cluster-management"}},
	}
}

func fakeClient(objects ...client.Object) client.Client {
	objects = seedTestOCMPlacements(objects)
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithStatusSubresource(&authv1alpha1.ManagedServiceAccountReplicaSet{}).
		WithObjects(objects...).
		Build()
}

func canonicalizeViaFakeClient(t *testing.T, obj *workv1.ManifestWork) *workv1.ManifestWork {
	t.Helper()

	c := fakeClient()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatal(err)
	}

	out := &workv1.ManifestWork{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), out); err != nil {
		t.Fatal(err)
	}
	return out
}

func setsOf(items ...string) sets.Set[string] {
	return sets.New[string](items...)
}

func seedTestOCMPlacements(objects []client.Object) []client.Object {
	seeded := append([]client.Object(nil), objects...)
	existingPlacements := map[types.NamespacedName]struct{}{}
	decisionsByPlacement := map[types.NamespacedName][]*clusterv1beta1.PlacementDecision{}

	for _, obj := range seeded {
		switch typed := obj.(type) {
		case *clusterv1beta1.Placement:
			existingPlacements[types.NamespacedName{Namespace: typed.Namespace, Name: typed.Name}] = struct{}{}
		case *clusterv1beta1.PlacementDecision:
			ensureTestOCMDecisionGroupLabels(typed)
			placementName := typed.Labels[clusterv1beta1.PlacementLabel]
			if placementName == "" {
				continue
			}
			key := types.NamespacedName{Namespace: typed.Namespace, Name: placementName}
			decisionsByPlacement[key] = append(decisionsByPlacement[key], typed)
		}
	}

	placementRefs := map[types.NamespacedName]struct{}{}
	for _, obj := range seeded {
		set, ok := obj.(*authv1alpha1.ManagedServiceAccountReplicaSet)
		if !ok {
			continue
		}
		for _, ref := range set.Spec.PlacementRefs {
			placementRefs[types.NamespacedName{Namespace: set.Namespace, Name: ref.Name}] = struct{}{}
		}
	}

	keys := make([]types.NamespacedName, 0, len(placementRefs))
	for key := range placementRefs {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Namespace != keys[j].Namespace {
			return keys[i].Namespace < keys[j].Namespace
		}
		return keys[i].Name < keys[j].Name
	})
	for _, key := range keys {
		if _, exists := existingPlacements[key]; exists {
			continue
		}
		seeded = append(seeded, testOCMPlacementFromDecisions(key.Name, key.Namespace, decisionsByPlacement[key]))
	}
	return seeded
}

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := authv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := clusterv1beta1.Install(scheme); err != nil {
		panic(err)
	}
	if err := msav1beta1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := workv1.Install(scheme); err != nil {
		panic(err)
	}
	if err := civ1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return scheme
}

// TestObservedGenerationOnlyAdvancesWhenControllerActsOnSpec pins the
// Kubernetes API convention that top-level status.observedGeneration must
// only equal spec.generation once the controller has begun acting on the
// current spec — see contributors/devel/sig-architecture/api-conventions.md
// (top-level observedGeneration semantic, lines 530-534): the value must
// reflect the most recent generation the controller has reconciled, NOT
// merely the most recent generation it has seen.
//
// Pre-fix behavior unconditionally stamped set.Generation in newStatus()
// regardless of whether validation or placement resolution had succeeded,
// which made early-return paths advertise that the controller had reconciled
// a spec it had actually rejected. The fix carries the previous
// observedGeneration forward in newStatus() and only re-stamps the current
// generation after spec validation + placement resolution succeed.
//
// Each subtest seeds Status.ObservedGeneration = 5 with set.Generation = 7
// so a regression to the pre-fix code would observe ObservedGeneration == 7
// on every path; the fix keeps it at 5 on the two early-return paths and
// advances it to 7 only on the success path.
func TestObservedGenerationOnlyAdvancesWhenControllerActsOnSpec(t *testing.T) {
	t.Run("validation failure early return preserves previous ObservedGeneration", func(t *testing.T) {
		ctx := context.Background()
		set := testReplicaSet()
		// Empty PlacementRefs triggers validateRuntimeSpec failure → early
		// return at controller.go line 116. The controller has NOT acted on
		// the current spec, so ObservedGeneration must NOT advance to 7.
		set.Spec.PlacementRefs = nil
		set.Status.ObservedGeneration = 5
		c := fakeClient(set)
		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

		if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}

		stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
			t.Fatalf("get status: %v", err)
		}
		if got, want := stored.Status.ObservedGeneration, int64(5); got != want {
			t.Fatalf("Status.ObservedGeneration = %d, want %d (controller rejected spec via validation early return; pre-fix code unconditionally stamped set.Generation=%d in newStatus)", got, want, set.Generation)
		}
		// Sanity: the InvalidSpec early-return condition should be stamped
		// with the *current* generation (per-condition observedGeneration is
		// independent of top-level — api-conventions.md lines 446-450).
		invalid := meta.FindStatusCondition(stored.Status.Conditions, conditionPlacementResolved)
		if invalid == nil || invalid.ObservedGeneration != set.Generation {
			t.Fatalf("PlacementResolved condition.ObservedGeneration = %#v, want %d (per-condition stamp is unaffected by the top-level fix)", invalid, set.Generation)
		}
	})

	t.Run("placement decision list failure early return preserves previous ObservedGeneration", func(t *testing.T) {
		ctx := context.Background()
		set := testReplicaSet()
		// OCM PlacementDecision listing fails. resolvePlacementRefs returns
		// err != nil with len(clusters) == 0, so the controller has not begun
		// acting on the current spec.
		set.Status.ObservedGeneration = 5
		base := fakeClient(set)
		c := &listFailingClient{Client: base, listErr: errors.New("synthetic placement list failure")}
		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

		if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}

		stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
			t.Fatalf("get status: %v", err)
		}
		if got, want := stored.Status.ObservedGeneration, int64(5); got != want {
			t.Fatalf("Status.ObservedGeneration = %d, want %d (placement decisions could not be listed so controller did not act on spec; pre-fix code unconditionally stamped set.Generation=%d in newStatus)", got, want, set.Generation)
		}
	})

	t.Run("successful placement resolution advances ObservedGeneration to current Generation", func(t *testing.T) {
		ctx := context.Background()
		set := testReplicaSet()
		set.Status.ObservedGeneration = 5
		decision := testOCMDecision("placement-a", "tenant", "cluster-a")
		c := fakeClient(set, decision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}})
		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

		if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}

		stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
			t.Fatalf("get status: %v", err)
		}
		if got, want := stored.Status.ObservedGeneration, set.Generation; got != want {
			t.Fatalf("Status.ObservedGeneration = %d, want %d (placement resolved, controller acted on current spec; observedGeneration must advance per api-conventions.md top-level semantic)", got, want)
		}
	})
}

// TestWaitForCleanupAdvancesObservedGeneration pins the cleanup-path
// observedGeneration semantic. The cleanup path actively reconciles the
// current spec — the controller is observing the DeletionTimestamp at the
// current generation and progressing the cleanup state machine. Per
// api-conventions.md "Typical status properties" (top-level observedGeneration
// must reflect the most recent generation the controller has reconciled),
// status.ObservedGeneration MUST advance to set.Generation in this path. A
// regression that lets newStatus()'s preserved (stale) ObservedGeneration
// flow through waitForCleanup would leave operators staring at a status that
// claims the controller has not yet observed the DeletionTimestamp it is
// actively acting on.
func TestWaitForCleanupAdvancesObservedGeneration(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	now := metav1.NewTime(time.Now())
	set.DeletionTimestamp = &now
	set.Status.ObservedGeneration = 5
	// Orphan ManifestWork carrying the controller's source labels keeps the
	// cleanup state machine in waitForCleanup (remainingWorks > 0) so the
	// reconcile lands on the path under test instead of progressing to
	// finalizer removal.
	staleWork := &workv1.ManifestWork{ObjectMeta: metav1.ObjectMeta{Name: rbacClusterManifestWorkName(set), Namespace: "cluster-a", Labels: sourceLabels(set)}}
	c := fakeClient(set, staleWork)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.reconcileDelete(ctx, set); err != nil {
		t.Fatalf("reconcileDelete failed: %v", err)
	}

	stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
		t.Fatalf("get status: %v", err)
	}
	if got, want := stored.Status.ObservedGeneration, set.Generation; got != want {
		t.Fatalf("Status.ObservedGeneration = %d, want %d (cleanup path actively reconciles current spec; pre-fix code let newStatus() preserve the stale value=5)", got, want)
	}
	// Sanity: the per-condition observedGeneration must also reflect the
	// current generation (independent of the top-level fix —
	// api-conventions.md condition.observedGeneration semantic).
	blocked := meta.FindStatusCondition(stored.Status.Conditions, conditionCleanupBlocked)
	if blocked == nil || blocked.ObservedGeneration != set.Generation {
		t.Fatalf("CleanupBlocked condition.ObservedGeneration = %#v, want %d (per-condition stamp must reflect current generation)", blocked, set.Generation)
	}
}

func TestReconcileEmitsWarningEventOnInvalidSpec(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.PlacementRefs = nil
	c := fakeClient(set)
	recorder := events.NewFakeRecorder(8)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme(), Recorder: recorder}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	select {
	case ev := <-recorder.Events:
		if !strings.HasPrefix(ev, corev1.EventTypeWarning+" "+reasonInvalidSpec+" ") {
			t.Fatalf("expected Warning %s event, got: %s", reasonInvalidSpec, ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected an event, got none")
	}
}

// actionCapturingRecorder records the `action` argument passed to Eventf so
// tests can assert it survives recordWarningEvent's signature.
// events.FakeRecorder formats its channel string as "type reason note" only,
// so the action arg is otherwise unobservable from tests.
type actionCapturingRecorder struct {
	action string
}

func (r *actionCapturingRecorder) Eventf(_ runtime.Object, _ runtime.Object, _, _, action, _ string, _ ...interface{}) {
	r.action = action
}

func TestReconcileWarningEventCarriesActionArg(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	set.Spec.PlacementRefs = nil
	c := fakeClient(set)
	recorder := &actionCapturingRecorder{}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme(), Recorder: recorder}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if got, want := recorder.action, "ValidateSpec"; got != want {
		t.Fatalf("Eventf action = %q, want %q (recordWarningEvent must propagate the action arg per events.k8s.io v1 API)", got, want)
	}
}

var (
	errInjectedStatusPatch = errors.New("injected: status patch")
	errInjectedChildApply  = errors.New("injected: child apply")
)

// childApplyFailingClient injects errInjectedChildApply on Create/Patch of
// *msav1beta1.ManagedServiceAccount and injects errInjectedStatusPatch on
// Status().Patch. All other operations delegate to the embedded fake client.
// Both Create and Patch are intercepted so the test stays correct regardless
// of whether ensureChild routes through Create (NotFound branch) or Patch
// (existing-but-drifted branch).
type childApplyFailingClient struct {
	client.Client
}

func (c *childApplyFailingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*msav1beta1.ManagedServiceAccount); ok {
		return errInjectedChildApply
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *childApplyFailingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*msav1beta1.ManagedServiceAccount); ok {
		return errInjectedChildApply
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *childApplyFailingClient) Status() client.SubResourceWriter {
	return &failingStatusWriter{SubResourceWriter: c.Client.Status()}
}

type failingStatusWriter struct {
	client.SubResourceWriter
}

func (w *failingStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return errInjectedStatusPatch
}

// TestReconcileJoinsChildAndStatusPatchErrors pins two invariants of the
// ApplyChildren tail in Reconcile:
//
//  1. When BOTH childResult.err and patchStatus fail in the same Reconcile,
//     the returned error wraps both via errors.Join so operators see both
//     root causes; the child apply error must not be dropped.
//  2. The Warning event with reason=reasonApplyFailed / action="ApplyChildren"
//     fires whenever childResult.err != nil, regardless of whether
//     patchStatus succeeded or failed; it must not be skipped on patchStatus
//     failure.
func TestReconcileJoinsChildAndStatusPatchErrors(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	base := fakeClient(set, decision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}})
	c := &childApplyFailingClient{Client: base}
	recorder := events.NewFakeRecorder(8)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme(), Recorder: recorder}

	_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)})
	if err == nil {
		t.Fatalf("expected non-nil error when both child apply and status patch fail")
	}
	if !errors.Is(err, errInjectedChildApply) {
		t.Fatalf("returned err = %v, want errors.Is(err, errInjectedChildApply) == true (child apply error must be preserved alongside status patch error via errors.Join)", err)
	}
	if !errors.Is(err, errInjectedStatusPatch) {
		t.Fatalf("returned err = %v, want errors.Is(err, errInjectedStatusPatch) == true (status patch error must be joined with child apply error so operators see both root causes)", err)
	}

	// The Warning event must fire even when patchStatus also fails.
	var applyFailedEvents int
	drain := time.After(100 * time.Millisecond)
draining:
	for {
		select {
		case ev := <-recorder.Events:
			if strings.HasPrefix(ev, corev1.EventTypeWarning+" "+reasonApplyFailed+" ") {
				applyFailedEvents++
			}
		case <-drain:
			break draining
		}
	}
	if applyFailedEvents != 1 {
		t.Fatalf("Warning %s events observed = %d, want exactly 1 (event must fire on child error even when patchStatus also fails)", reasonApplyFailed, applyFailedEvents)
	}
}

// rejectingNamespaceClient fails any cache-side Namespace Get/List, modeling
// the chart RBAC where namespaces are `get`-only.
type rejectingNamespaceClient struct {
	client.Client
}

func (c *rejectingNamespaceClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Namespace); ok {
		return errCacheNamespaceForbidden
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *rejectingNamespaceClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.NamespaceList); ok {
		return errCacheNamespaceForbidden
	}
	return c.Client.List(ctx, list, opts...)
}

type namespaceCountingReader struct {
	client.Reader
	namespaceGets int
}

func (r *namespaceCountingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Namespace); ok {
		r.namespaceGets++
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

var errCacheNamespaceForbidden = errors.New("namespace Get/List on the cache-backed Client is forbidden in tests; use APIReader")

func TestRequireManagedClusterNamespaceUsesAPIReader(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}}

	cacheClient := &rejectingNamespaceClient{Client: fakeClient(set, decision)}
	apiReader := &namespaceCountingReader{Reader: fakeClient(namespace)}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{
		Client:    cacheClient,
		APIReader: apiReader,
		Scheme:    testScheme(),
	}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if apiReader.namespaceGets == 0 {
		t.Fatalf("APIReader.Get on Namespace was never called; controller bypassed the uncached reader")
	}
	msa := &msav1beta1.ManagedServiceAccount{}
	if err := cacheClient.Client.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: "msa"}, msa); err != nil {
		t.Fatalf("expected child ManagedServiceAccount after APIReader namespace check: %v", err)
	}
}

func TestRequireManagedClusterNamespaceMissingFailsClusterWithoutPanic(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	decision := testOCMDecision("placement-a", "tenant", "cluster-a")
	c := fakeClient(set, decision)
	apiReader := &namespaceCountingReader{Reader: fakeClient()}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{
		Client:    c,
		APIReader: apiReader,
		Scheme:    testScheme(),
	}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err == nil {
		t.Fatalf("expected reconcile to surface namespace NotFound as a non-nil error (failures path)")
	}
	if apiReader.namespaceGets == 0 {
		t.Fatalf("APIReader.Get on Namespace was never called")
	}
	msa := &msav1beta1.ManagedServiceAccount{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-a", Name: "msa"}, msa); err == nil {
		t.Fatalf("unexpected child ManagedServiceAccount created when managed cluster namespace is absent")
	}
}

func TestPlacementEmptyConditionIsRemovedAfterPlacementGetsClusters(t *testing.T) {
	ctx := context.Background()
	set := testReplicaSet()
	emptyDecision := &clusterv1beta1.PlacementDecision{
		TypeMeta: metav1.TypeMeta{APIVersion: clusterv1beta1.GroupVersion.String(), Kind: "PlacementDecision"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "placement-a-decision",
			Namespace: "tenant",
			Labels:    map[string]string{clusterv1beta1.PlacementLabel: "placement-a"},
		},
		Status: clusterv1beta1.PlacementDecisionStatus{
			Decisions: []clusterv1beta1.ClusterDecision{},
		},
	}
	c := fakeClient(set, emptyDecision)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	first := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), first); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}
	if len(first.Status.Placements) != 1 {
		t.Fatalf("placements after first reconcile = %d, want 1", len(first.Status.Placements))
	}
	gotEmpty := meta.FindStatusCondition(first.Status.Placements[0].Conditions, conditionPlacementEmpty)
	if gotEmpty == nil || gotEmpty.Status != metav1.ConditionTrue || gotEmpty.Reason != reasonNoDecisions {
		t.Fatalf("first reconcile: expected %s=True with reason %s, got %#v", conditionPlacementEmpty, reasonNoDecisions, gotEmpty)
	}
	if first.Status.Placements[0].SelectedClusterCount != 0 {
		t.Fatalf("first reconcile placement SelectedClusterCount = %d, want 0", first.Status.Placements[0].SelectedClusterCount)
	}

	// Recover: populate the PlacementDecision with one cluster decision and seed the cluster namespace.
	storedDecision := &clusterv1beta1.PlacementDecision{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "placement-a-decision"}, storedDecision); err != nil {
		t.Fatalf("get stored decision: %v", err)
	}
	storedDecision.Status.Decisions = []clusterv1beta1.ClusterDecision{{ClusterName: "cluster-a", Reason: "Selected"}}
	if err := c.Update(ctx, storedDecision); err != nil {
		t.Fatalf("update decision with one cluster: %v", err)
	}
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}}); err != nil {
		t.Fatalf("create cluster-a namespace: %v", err)
	}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	second := &authv1alpha1.ManagedServiceAccountReplicaSet{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(set), second); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}
	if len(second.Status.Placements) != 1 {
		t.Fatalf("placements after second reconcile = %d, want 1", len(second.Status.Placements))
	}
	if got := meta.FindStatusCondition(second.Status.Placements[0].Conditions, conditionPlacementEmpty); got != nil {
		t.Fatalf("second reconcile: expected %s removed (placement now has one cluster), but stale True remained from previous reconcile: %#v", conditionPlacementEmpty, got)
	}
	if second.Status.Placements[0].SelectedClusterCount != 1 {
		t.Fatalf("second reconcile placement SelectedClusterCount = %d, want 1", second.Status.Placements[0].SelectedClusterCount)
	}
}

type listFailingClient struct {
	client.Client
	listErr error
}

func (c *listFailingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.listErr
}

func TestMapFuncHandlersLogV1OnListError(t *testing.T) {
	set := testReplicaSet()
	base := fakeClient(set)
	listErr := errors.New("synthetic list failure")
	failing := &listFailingClient{Client: base, listErr: listErr}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: failing, APIReader: base, Scheme: testScheme()}

	var captured []string
	sink := funcr.New(func(_, args string) {
		captured = append(captured, args)
	}, funcr.Options{Verbosity: 1})
	ctx := crlog.IntoContext(context.Background(), sink)

	captured = nil
	if got := reconciler.requestsForPlacementRefName(ctx, "tenant", "placement-a"); got != nil {
		t.Fatalf("requestsForPlacementRefName got %v, want nil on List error", got)
	}
	if !logCapturedAll(captured, "placementRef name failed", "placement-a") {
		t.Fatalf("requestsForPlacementRefName did not log expected V(1) message; captured=%v", captured)
	}
}

// TestRequestsForClusterProfile_NonClusterProfileObjectReturnsNil pins the
// guard at the top of requestsForClusterProfile: if the watched object passed
// in is not a *civ1alpha1.ClusterProfile, the mapper must return nil without
// listing. The Watches(...) wiring in SetupWithManager binds this mapper to
// ClusterProfile, but the type guard is the second line of defense in case the
// builder chain is rewired and the mapper is invoked for a foreign type.
func TestRequestsForClusterProfile_NonClusterProfileObjectReturnsNil(t *testing.T) {
	c := fakeClient(testReplicaSet())
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	// A *corev1.Namespace is not a ClusterProfile; the mapper must reject it.
	got := reconciler.requestsForClusterProfile(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
	})
	if got != nil {
		t.Fatalf("requestsForClusterProfile(non-ClusterProfile obj) = %v, want nil", got)
	}
}

// TestRequestsForClusterProfile_EnqueuesAllReplicaSetsClusterWide pins the
// fan-out semantic: ClusterProfile is cluster-scoped, so any
// ManagedServiceAccountReplicaSet across any namespace may be affected by an
// AccessProviders / CredentialProviders status change. The mapper must list
// cluster-wide and enqueue one Request per ReplicaSet, deduplication by
// (namespace, name) implicit in the unique listing.
func TestRequestsForClusterProfile_EnqueuesAllReplicaSetsClusterWide(t *testing.T) {
	setA := testReplicaSet()
	setA.Name = "set-a"
	setA.Namespace = "tenant-a"

	setB := testReplicaSet()
	setB.Name = "set-b"
	setB.Namespace = "tenant-a"

	setC := testReplicaSet()
	setC.Name = "set-c"
	setC.Namespace = "tenant-b"

	c := fakeClient(setA, setB, setC)
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

	profile := testClusterProfile("open-cluster-management-addon", "cluster-a", "cluster-a")
	got := reconciler.requestsForClusterProfile(context.Background(), profile)

	want := sets.New[string](
		"tenant-a/set-a",
		"tenant-a/set-b",
		"tenant-b/set-c",
	)
	gotSet := sets.New[string]()
	for _, req := range got {
		gotSet.Insert(req.Namespace + "/" + req.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("requestsForClusterProfile returned %d Requests, want %d (cluster-wide list across two namespaces): %v", len(got), len(want), got)
	}
	if !gotSet.Equal(want) {
		t.Fatalf("requestsForClusterProfile request set = %v, want %v", sets.List(gotSet), sets.List(want))
	}
}

// TestRequestsForClusterProfile_ListErrorReturnsNil pins the best-effort
// contract: if the cluster-wide List fails (e.g. cache not synced), the mapper
// must log at V(1) and return nil rather than panicking or surfacing the err.
// Losing one trigger is acceptable; the next ClusterProfile event or the
// selectorRequeueInterval poll will recover.
func TestRequestsForClusterProfile_ListErrorReturnsNil(t *testing.T) {
	base := fakeClient(testReplicaSet())
	listErr := errors.New("synthetic ClusterProfile list failure")
	failing := &listFailingClient{Client: base, listErr: listErr}
	reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: failing, APIReader: base, Scheme: testScheme()}

	var captured []string
	sink := funcr.New(func(_, args string) {
		captured = append(captured, args)
	}, funcr.Options{Verbosity: 1})
	ctx := crlog.IntoContext(context.Background(), sink)

	profile := testClusterProfile("open-cluster-management-addon", "cluster-a", "cluster-a")
	if got := reconciler.requestsForClusterProfile(ctx, profile); got != nil {
		t.Fatalf("requestsForClusterProfile got %v, want nil on List error (best-effort: lost trigger acceptable, log at V(1))", got)
	}
	if !logCapturedAll(captured, "ClusterProfile event failed", "cluster-a") {
		t.Fatalf("requestsForClusterProfile did not log expected V(1) message; captured=%v", captured)
	}
}

func logCapturedAll(items []string, subs ...string) bool {
	for _, item := range items {
		matched := true
		for _, sub := range subs {
			if !strings.Contains(item, sub) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

const primaryForCall = "For(&authv1alpha1.ManagedServiceAccountReplicaSet{}"

func anchorPrimaryForCall(t *testing.T) (text string, forStart int) {
	t.Helper()
	src, err := os.ReadFile("managedserviceaccountreplicaset_controller.go")
	if err != nil {
		t.Fatalf("read controller source: %v", err)
	}
	text = string(src)
	idx := strings.Index(text, primaryForCall)
	if idx < 0 {
		t.Fatalf("primary For(...) call not found; refactor must keep status-driven watch contract")
	}
	return text, idx
}

func TestPrimaryWatchOmitsGenerationChangedPredicate(t *testing.T) {
	text, idx := anchorPrimaryForCall(t)
	if got := strings.Count(text, primaryForCall); got != 1 {
		t.Fatalf("expected exactly one primary For(...) call; got %d", got)
	}
	closing := strings.Index(text[idx:], ").")
	if closing < 0 {
		t.Fatalf("could not locate closing of primary For(...) call near offset %d", idx)
	}
	forArgs := text[idx : idx+closing]
	if strings.Contains(forArgs, "GenerationChangedPredicate") {
		t.Fatalf("primary For(...) must not gate with GenerationChangedPredicate; the controller is status-driven (LastTransitionTime preservation and stale condition removal depend on status-only updates re-triggering Reconcile). Saw: %s", forArgs)
	}
}

// Child Watches(...) must not gate on GenerationChangedPredicate: child MSA
// TokenReported and ManifestWork Applied transitions are status-only updates
// that don't bump .metadata.generation, and would be silently dropped, breaking
// aggregation of child status into ManagedServiceAccountReplicaSet.status.
//
// ClusterProfile is also included here even though it is not a "child" in the
// owns-via-source-labels sense: ClusterProfile.Status.AccessProviders /
// CredentialProviders changes (status-only, no generation bump) must still
// re-trigger Reconcile, so the ClusterProfile Watches(...) shares the same
// no-GenerationChangedPredicate contract.
func TestChildWatchesOmitGenerationChangedPredicate(t *testing.T) {
	text, idx := anchorPrimaryForCall(t)
	// Anchor after the primary For(...) call so the legitimate design-comment
	// mention of GenerationChangedPredicate above the builder chain is excluded.
	watchesScope := text[idx+len(primaryForCall):]
	if strings.Contains(watchesScope, "GenerationChangedPredicate") {
		t.Fatalf("child Watches(...) in SetupWithManager must not use GenerationChangedPredicate; controller is status-driven (see TestPrimaryWatchOmitsGenerationChangedPredicate)")
	}
	// Assert each known status-driven watched type is wired via Watches(...)
	// in SetupWithManager. If a future refactor swaps in a wrapper or moves a
	// watch out of the builder chain, the no-GenerationChangedPredicate guard
	// above could become a vacuous truth for a missing watch; this list
	// pins the watched-types contract explicitly.
	wantWatches := []string{
		"Watches(&clusterv1beta1.Placement{}",
		"Watches(&clusterv1beta1.PlacementDecision{}",
		"Watches(&msav1beta1.ManagedServiceAccount{}",
		"Watches(&workv1.ManifestWork{}",
		"Watches(&civ1alpha1.ClusterProfile{}",
	}
	for _, want := range wantWatches {
		if !strings.Contains(watchesScope, want) {
			t.Fatalf("expected SetupWithManager to register %s after the primary For(...); status-driven watches must be wired without GenerationChangedPredicate", want)
		}
	}
}

// TestReconcileDeleteSkipsAlreadyTerminatingChildren pins the level-triggered
// deletion contract documented at controller-runtime/pkg/reconcile/reconcile.go
// (lines 102-105: Reconcile is level-triggered and may be invoked any number of
// times for the same object). reconcileDelete lists ManifestWork /
// ManagedServiceAccount children selected by ownedSelector(set) and the OCM
// finalizer keeps them around with a non-zero DeletionTimestamp until the
// agent finishes cleanup. Issuing Delete again on those terminating items is
// at best a no-op and at worst noisy churn (audit log entries, redundant
// admission traffic, repeated deletionTimestamp/finalizer round-trips on the
// hub). The post-fix loop guards each item with `if !item.DeletionTimestamp
// .IsZero() { continue }`. This regression test seeds one terminating + one
// live child of each kind and asserts that only the live child receives a
// Delete call, while the terminating child is observed but skipped. It also
// pins the wait-for-cleanup return contract: as long as len(works) > 0 (or
// len(msas) > 0 once works is empty) the reconciler must surface
// RequeueAfter == cleanupRequeueInterval with nil err so controller-runtime
// honors the poll. If the skip is reverted, the recordingClient will record
// a Delete on the already-terminating object and this test FAILs.
func TestReconcileDeleteSkipsAlreadyTerminatingChildren(t *testing.T) {
	cases := []struct {
		name          string
		seedObjects   func(set *authv1alpha1.ManagedServiceAccountReplicaSet) []client.Object
		wantDeletes   []string
		forbidDeletes []string
	}{
		{
			// ManifestWork branch: works list contains both a terminating and a
			// live entry. reconcileDelete must Delete only the live one and
			// then return RequeueAfter=cleanupRequeueInterval because
			// len(works) > 0 (the terminating one is still tracked).
			name: "ManifestWork loop skips terminating items",
			seedObjects: func(set *authv1alpha1.ManagedServiceAccountReplicaSet) []client.Object {
				now := metav1.NewTime(time.Now())
				terminating := &workv1.ManifestWork{ObjectMeta: metav1.ObjectMeta{
					Name:              rbacClusterManifestWorkName(set),
					Namespace:         "cluster-terminating",
					Labels:            sourceLabels(set),
					Finalizers:        []string{"work.cleanup.example/keep"},
					DeletionTimestamp: &now,
				}}
				live := &workv1.ManifestWork{ObjectMeta: metav1.ObjectMeta{
					Name:      rbacClusterManifestWorkName(set),
					Namespace: "cluster-live",
					Labels:    sourceLabels(set),
				}}
				return []client.Object{terminating, live}
			},
			wantDeletes: []string{
				"ManifestWork/cluster-live/" + rbacClusterManifestWorkName(testReplicaSet()),
			},
			forbidDeletes: []string{
				"ManifestWork/cluster-terminating/" + rbacClusterManifestWorkName(testReplicaSet()),
			},
		},
		{
			// ManagedServiceAccount branch: works list is empty so the
			// reconciler advances to the msa loop. One terminating msa + one
			// live msa. Only the live one must be deleted; we must still see
			// RequeueAfter=cleanupRequeueInterval because the terminating msa
			// is still in the cache (len(msas) > 0).
			name: "ManagedServiceAccount loop skips terminating items",
			seedObjects: func(set *authv1alpha1.ManagedServiceAccountReplicaSet) []client.Object {
				now := metav1.NewTime(time.Now())
				terminating := &msav1beta1.ManagedServiceAccount{ObjectMeta: metav1.ObjectMeta{
					Name:              "msa",
					Namespace:         "cluster-terminating",
					Labels:            sourceLabels(set),
					Finalizers:        []string{"msa.cleanup.example/keep"},
					DeletionTimestamp: &now,
				}}
				live := &msav1beta1.ManagedServiceAccount{ObjectMeta: metav1.ObjectMeta{
					Name:      "msa",
					Namespace: "cluster-live",
					Labels:    sourceLabels(set),
				}}
				return []client.Object{terminating, live}
			},
			wantDeletes: []string{
				"ManagedServiceAccount/cluster-live/msa",
			},
			forbidDeletes: []string{
				"ManagedServiceAccount/cluster-terminating/msa",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			set := testReplicaSet()
			now := metav1.NewTime(time.Now())
			set.DeletionTimestamp = &now

			seeds := append([]client.Object{set}, tc.seedObjects(set)...)
			c := &recordingClient{Client: fakeClient(seeds...)}
			reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}

			result, err := reconciler.reconcileDelete(ctx, set)
			if err != nil {
				t.Fatalf("reconcileDelete returned error: %v", err)
			}
			if result.RequeueAfter != cleanupRequeueInterval {
				t.Fatalf("RequeueAfter = %s, want %s (level-triggered cleanup poll on len(works|msas) > 0)", result.RequeueAfter, cleanupRequeueInterval)
			}

			for _, want := range tc.wantDeletes {
				if !contains(c.deletes, want) {
					t.Fatalf("Delete calls = %v, want to include live-child Delete %q", c.deletes, want)
				}
			}
			for _, forbid := range tc.forbidDeletes {
				if contains(c.deletes, forbid) {
					t.Fatalf("Delete calls = %v, must NOT include already-terminating child %q (controller-runtime is level-triggered; redundant DELETEs on items the OCM finalizer is still draining are wasted churn)", c.deletes, forbid)
				}
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// TestBuildManagedServiceAccountRotationEnabledTriState pins the *bool tri-state
// forwarding contract for spec.template.spec.rotation.enabled. The hub-side CRD
// default is true (kubebuilder:default=true), so a nil pointer (SSA / kubectl
// apply that omits the field) must forward to Enabled=true on the generated
// child, an explicit false must round-trip as false, and an explicit true must
// stay true. Without the *bool change, all three cases would collapse into the
// Go zero value (false) and silently disable rotation for omitted fields.
func TestBuildManagedServiceAccountRotationEnabledTriState(t *testing.T) {
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{name: "nil falls back to CRD default true", in: nil, want: true},
		{name: "explicit false forwards as false", in: ptr.To(false), want: false},
		{name: "explicit true forwards as true", in: ptr.To(true), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := testReplicaSet()
			set.Spec.Template.Spec.Rotation.Enabled = tc.in

			got := buildManagedServiceAccount(set, "cluster-a", "placement-a")
			if got.Spec.Rotation.Enabled != tc.want {
				t.Fatalf("buildManagedServiceAccount Rotation.Enabled = %t, want %t (input *bool = %v)", got.Spec.Rotation.Enabled, tc.want, tc.in)
			}
		})
	}
}

// TestFinalizerPatchUsesOptimisticLock pins the merge-patch shape used by
// both finalizer mutation paths in the controller. metadata.finalizers is a
// shared slice that other controllers (and admission plugins) may also
// mutate. A non-locked merge patch from a stale resourceVersion would
// silently overwrite the other writer's entry; the apiserver's
// optimistic-lock precondition is therefore load-bearing.
//
// controller-runtime implements client.MergeFromWithOptimisticLock by
// embedding the original object's metadata.resourceVersion as a top-level
// field on the diff (see pkg/client/patch.go: it clears RV on `original`
// and stamps it onto `modified`, so the JSON merge patch diff carries
// `"resourceVersion":"<rv>"`). A regression to plain client.MergeFrom(base)
// would diff two objects with identical RV and produce a payload with no
// `resourceVersion` key — silently dropping the precondition.
//
// This test intercepts the Patch call on the parent
// ManagedServiceAccountReplicaSet, asks the patch object for its rendered
// payload via patch.Data(obj), and asserts the payload contains a
// `"resourceVersion":"` substring. We assert on substring (not exact JSON
// match) to stay tolerant of merge-patch field ordering, but the assertion
// would fail under client.MergeFrom(base) because that path does not set
// any resourceVersion on the modified object.
func TestFinalizerPatchUsesOptimisticLock(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		ctx := context.Background()
		set := testReplicaSet()
		// Drop the seeded finalizer so the AddFinalizer branch in
		// Reconcile actually triggers a Patch call. testReplicaSet()
		// installs the finalizer by default for the steady-state tests.
		set.Finalizers = nil

		decision := testOCMDecision("placement-a", "tenant", "cluster-a")
		base := fakeClient(set, decision, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"}})

		var captured [][]byte
		c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
			Patch: func(ctx context.Context, inner client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*authv1alpha1.ManagedServiceAccountReplicaSet); ok {
					data, err := patch.Data(obj)
					if err != nil {
						t.Fatalf("patch.Data(parent) failed: %v", err)
					}
					captured = append(captured, append([]byte(nil), data...))
				}
				return inner.Patch(ctx, obj, patch, opts...)
			},
		})

		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}
		if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)}); err != nil {
			t.Fatalf("reconcile failed: %v", err)
		}

		// Sanity: the controller actually wrote the finalizer through, so
		// we know the captured Patch is the finalizer-add one (the
		// Reconcile early-returns immediately after AddFinalizer Patch).
		stored := &authv1alpha1.ManagedServiceAccountReplicaSet{}
		if err := base.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
			t.Fatalf("get stored set: %v", err)
		}
		if !contains(stored.Finalizers, finalizerName) {
			t.Fatalf("finalizer %q not present on stored set; AddFinalizer Patch did not land (finalizers=%v)", finalizerName, stored.Finalizers)
		}
		if len(captured) == 0 {
			t.Fatalf("no Patch call captured for parent ManagedServiceAccountReplicaSet on finalizer-add path; controller may have skipped AddFinalizer")
		}

		// First captured patch is the finalizer-add one (Reconcile
		// returns immediately after that Patch in the no-finalizer
		// branch).
		payload := string(captured[0])
		if !strings.Contains(payload, `"resourceVersion":"`) {
			t.Fatalf("finalizer-add Patch payload missing resourceVersion precondition; got %q (a regression to client.MergeFrom(base) would not stamp resourceVersion on the diff and would silently overwrite concurrent finalizer mutations)", payload)
		}
		if !strings.Contains(payload, `"finalizers"`) {
			t.Fatalf("finalizer-add Patch payload missing finalizers diff; got %q (sanity check that we captured the right Patch call)", payload)
		}
	})

	t.Run("remove", func(t *testing.T) {
		ctx := context.Background()
		set := testReplicaSet()
		// testReplicaSet() already installs finalizerName, which is
		// required for RemoveFinalizer to actually mutate state.
		now := metav1.NewTime(time.Now())
		set.DeletionTimestamp = &now

		// Seed no orphan ManifestWork / ManagedServiceAccount children so
		// reconcileDelete falls through to the RemoveFinalizer + Patch
		// branch on the first reconcile pass.
		base := fakeClient(set)

		var captured [][]byte
		c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
			Patch: func(ctx context.Context, inner client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*authv1alpha1.ManagedServiceAccountReplicaSet); ok {
					data, err := patch.Data(obj)
					if err != nil {
						t.Fatalf("patch.Data(parent) failed: %v", err)
					}
					captured = append(captured, append([]byte(nil), data...))
				}
				return inner.Patch(ctx, obj, patch, opts...)
			},
		})

		// Mirror the production state: reconcileDelete is invoked with
		// the live in-memory set whose RV must match the apiserver copy.
		// Re-Get from the underlying fake client so the RV the
		// optimistic-lock patch will embed is the canonical one.
		live := &authv1alpha1.ManagedServiceAccountReplicaSet{}
		if err := base.Get(ctx, client.ObjectKeyFromObject(set), live); err != nil {
			t.Fatalf("get live set: %v", err)
		}

		reconciler := &ManagedServiceAccountReplicaSetReconciler{Client: c, APIReader: c, Scheme: testScheme()}
		if _, err := reconciler.reconcileDelete(ctx, live); err != nil {
			t.Fatalf("reconcileDelete failed: %v", err)
		}

		if len(captured) == 0 {
			t.Fatalf("no Patch call captured for parent ManagedServiceAccountReplicaSet on finalizer-remove path; controller may have skipped RemoveFinalizer")
		}

		// The last captured patch is the finalizer-remove one
		// (reconcileDelete may patch status earlier on cleanup-blocked
		// paths, but with no orphans seeded it goes directly to the
		// RemoveFinalizer branch).
		payload := string(captured[len(captured)-1])
		if !strings.Contains(payload, `"resourceVersion":"`) {
			t.Fatalf("finalizer-remove Patch payload missing resourceVersion precondition; got %q (a regression to client.MergeFrom(base) would not stamp resourceVersion on the diff and would silently overwrite concurrent finalizer mutations)", payload)
		}
		if !strings.Contains(payload, `"finalizers"`) {
			t.Fatalf("finalizer-remove Patch payload missing finalizers diff; got %q (sanity check that we captured the right Patch call)", payload)
		}
	})
}
