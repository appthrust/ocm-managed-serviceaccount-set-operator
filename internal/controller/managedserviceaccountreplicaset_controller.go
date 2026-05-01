package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	authv1alpha1 "github.com/appthrust/ocm-managed-serviceaccount-replicaset-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	clusterv1alpha1 "open-cluster-management.io/api/cluster/v1alpha1"
	clusterv1beta1 "open-cluster-management.io/api/cluster/v1beta1"
	workv1 "open-cluster-management.io/api/work/v1"
	msav1beta1 "open-cluster-management.io/managed-serviceaccount/apis/authentication/v1beta1"
	clustersdkv1alpha1 "open-cluster-management.io/sdk-go/pkg/apis/cluster/v1alpha1"
	clustersdkv1beta1 "open-cluster-management.io/sdk-go/pkg/apis/cluster/v1beta1"
	civ1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	conditionPlacementResolved           = "PlacementResolved"
	conditionManagedServiceAccountsReady = "ManagedServiceAccountsReady"
	conditionRemotePermissionsApplied    = "RemotePermissionsApplied"
	conditionCleanupBlocked              = "CleanupBlocked"
	conditionReady                       = "Ready"
	conditionPlacementRolledOut          = "PlacementRolledOut"

	conditionPlacementEmpty = "PlacementEmpty"
	conditionProgressing    = "Progressing"

	reasonInvalidSpec           = "InvalidSpec"
	reasonPlacementUnavailable  = "PlacementUnavailable"
	reasonResolved              = "AsExpected"
	reasonNoDecisions           = "NoDecisions"
	reasonApplyFailed           = "ApplyFailed"
	reasonChildConflict         = "ChildConflict"
	reasonClusterFailed         = "ClusterFailed"
	reasonWaitingForCredentials = "WaitingForCredentials"
	reasonWaitingForRBAC        = "WaitingForRBAC"
	reasonReady                 = "Ready"
	reasonProgressing           = "Progressing"
	reasonCompleted             = "Completed"
	reasonComplete              = "Complete"
	reasonDisabled              = "Disabled"
	reasonApplied               = "Applied"
	reasonNotDeleting           = "NotDeleting"
	reasonWaitingForOCMCleanup  = "WaitingForOCMCleanup"
	reasonCleanupBlocked        = "CleanupBlocked"

	msgChildConflict          = "one or more generated children conflict with existing objects"
	msgClusterFailed          = "one or more selected clusters could not be reconciled"
	msgWaitingForCredentials  = "generated ManagedServiceAccounts are not ready on every selected cluster"
	msgWaitingForRBAC         = "generated RBAC ManifestWorks are not available on every selected cluster"
	msgStaleCleanupBlocked    = "waiting for stale ManifestWork cleanup before deleting stale ManagedServiceAccounts"
	msgStaleCleanupInProgress = "stale child cleanup is still in progress"

	cleanupRequeueInterval  = 10 * time.Second
	selectorRequeueInterval = 30 * time.Second
)

var defaultTokenValidity = metav1.Duration{Duration: 360 * 24 * time.Hour}

// ManagedServiceAccountReplicaSetReconciler fans out OCM
// ManagedServiceAccount children from OCM placement output.
type ManagedServiceAccountReplicaSetReconciler struct {
	client.Client
	// APIReader bypasses the cache so namespace existence checks do not
	// trigger a cluster-wide LIST/WATCH informer the chart RBAC does not grant.
	APIReader                 client.Reader
	Scheme                    *runtime.Scheme
	Recorder                  events.EventRecorder
	NamespaceSelectorResolver NamespaceSelectorResolver
}

func (r *ManagedServiceAccountReplicaSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var set authv1alpha1.ManagedServiceAccountReplicaSet
	if err := r.Get(ctx, req.NamespacedName, &set); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	keepClusters := sets.New[string]()
	defer func() { r.stopNamespaceCaches(&set, keepClusters) }()

	if !set.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &set)
	}

	base := set.DeepCopy()
	if controllerutil.AddFinalizer(&set, finalizerName) {
		// Optimistic lock: metadata.finalizers is a shared slice that other
		// controllers (and admission plugins) may also mutate; a non-locked
		// merge patch from a stale RV would silently drop their entries.
		if err := r.Patch(ctx, &set, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("added finalizer", "finalizer", finalizerName)
		return ctrl.Result{}, nil
	}

	status := newStatus(&set)
	if err := validateRuntimeSpec(&set); err != nil {
		r.recordWarningEvent(&set, reasonInvalidSpec, "ValidateSpec", err.Error())
		setEarlyReturnConditions(&status, reasonInvalidSpec, err.Error(), set.Generation)
		return ctrl.Result{}, r.patchStatus(ctx, &set, status)
	}

	resolution := r.resolvePlacementRefs(ctx, &set, set.Generation)
	status.Placements = resolution.placementStatuses
	status.SelectedClusterCount = int32(len(resolution.clusters))
	if resolution.err != nil && len(resolution.clusters) == 0 {
		r.recordWarningEvent(&set, reasonPlacementUnavailable, "ResolvePlacement", resolution.err.Error())
		setEarlyReturnConditions(&status, reasonPlacementUnavailable, resolution.err.Error(), set.Generation)
		return ctrl.Result{}, r.patchStatus(ctx, &set, status)
	}
	setCondition(&status.Conditions, conditionPlacementResolved, metav1.ConditionTrue, reasonResolved, "placement refs resolved", set.Generation)
	status.ObservedGeneration = set.Generation
	if selectorRBACEnabled(&set) {
		keepClusters = sets.New(resolution.clusters...)
	}

	rollout, err := r.planRollout(ctx, &set, resolution)
	if err != nil {
		r.recordWarningEvent(&set, reasonApplyFailed, "PlanRollout", err.Error())
		childResult := childReconcileResult{err: err}
		applyChildResultConditions(&status, childResult, len(resolution.clusters), set.Generation, rbacEnabled(&set))
		return ctrl.Result{}, errors.Join(r.patchStatus(ctx, &set, status), err)
	}
	status.Rollout = rollout.summary
	applyPlacementRolloutStatus(&status, rollout)
	applyPlacementRolledOutCondition(&status, rollout.summary, set.Generation)

	childResult := r.reconcileChildren(
		ctx,
		&set,
		resolution,
		rollout.clusters,
		rollout.cleanupClusters,
		rollout.statuses,
		&status,
	)
	status.ReadyClusterCount = childResult.ready
	applyChildResultConditions(&status, childResult, len(resolution.clusters), set.Generation, rbacEnabled(&set))
	if childResult.cleanupPending {
		setCondition(&status.Conditions, conditionCleanupBlocked, metav1.ConditionTrue, reasonWaitingForOCMCleanup, msgStaleCleanupBlocked, set.Generation)
		setCondition(&status.Conditions, conditionReady, metav1.ConditionFalse, reasonCleanupBlocked, msgStaleCleanupInProgress, set.Generation)
	} else {
		setNotDeletingCondition(&status, set.Generation)
	}

	patchErr := r.patchStatus(ctx, &set, status)
	if childResult.err != nil {
		r.recordWarningEvent(&set, reasonApplyFailed, "ApplyChildren", childResult.err.Error())
		return ctrl.Result{}, errors.Join(patchErr, childResult.err)
	}
	if childResult.conflicts > 0 {
		r.recordWarningEvent(&set, reasonChildConflict, "ApplyChildren", msgChildConflict)
	}
	if patchErr != nil {
		return ctrl.Result{}, patchErr
	}
	if childResult.cleanupPending {
		return ctrl.Result{RequeueAfter: cleanupRequeueInterval}, nil
	}
	if rollout.requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: rollout.requeueAfter}, nil
	}
	if selectorRBACEnabled(&set) {
		return ctrl.Result{RequeueAfter: selectorRequeueInterval}, nil
	}
	return ctrl.Result{}, nil
}

type placementResolution struct {
	clusters             []string
	clusterPlacementRefs map[string][]string
	clusterGroupsByRef   map[string]clustersdkv1beta1.ClusterGroupsMap
	placementsByRef      map[string]*clusterv1beta1.Placement
	placementStatuses    []authv1alpha1.PlacementStatus
	err                  error
}

// primaryRef returns the placement ref that first claimed the cluster.
// The labels written onto generated children are keyed off this ref; all
// reconcile/status operations for that cluster pass it through.
//
// Callers must only invoke this for clusters present in p.clusters; the empty
// string returned for unknown clusters is a footgun for label generation.
func (p placementResolution) primaryRef(clusterName string) string {
	refs := p.clusterPlacementRefs[clusterName]
	if len(refs) == 0 {
		return ""
	}
	return refs[0]
}

func (r *ManagedServiceAccountReplicaSetReconciler) resolvePlacementRefs(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	generation int64,
) placementResolution {
	result := placementResolution{
		clusterPlacementRefs: map[string][]string{},
		clusterGroupsByRef:   map[string]clustersdkv1beta1.ClusterGroupsMap{},
		placementsByRef:      map[string]*clusterv1beta1.Placement{},
		placementStatuses:    make([]authv1alpha1.PlacementStatus, 0, len(set.Spec.PlacementRefs)),
	}
	// Carry previous Conditions forward so meta.SetStatusCondition preserves
	// LastTransitionTime when status does not transition.
	previousByKey := map[string][]metav1.Condition{}
	for _, p := range set.Status.Placements {
		previousByKey[p.Name] = slices.Clone(p.Conditions)
	}
	union := sets.New[string]()
	for _, ref := range set.Spec.PlacementRefs {
		placement, clusterGroups, placementStatus, err := r.resolvePlacementRef(ctx, set, ref, generation, previousByKey[ref.Name])
		clusters := sets.List(clusterGroups.GetClusters())
		if err != nil {
			setCondition(&placementStatus.Conditions, conditionPlacementResolved, metav1.ConditionFalse, reasonPlacementUnavailable, err.Error(), generation)
			result.err = errors.Join(result.err, err)
		}
		slices.Sort(clusters)
		result.placementsByRef[ref.Name] = placement
		result.clusterGroupsByRef[ref.Name] = clusterGroups
		placementStatus.SelectedClusterCount = int32(len(clusters))
		if len(clusters) == 0 && err == nil {
			setCondition(&placementStatus.Conditions, conditionPlacementEmpty, metav1.ConditionTrue, reasonNoDecisions, "placement ref resolved to zero clusters", generation)
		} else {
			meta.RemoveStatusCondition(&placementStatus.Conditions, conditionPlacementEmpty)
		}
		result.placementStatuses = append(result.placementStatuses, placementStatus)
		for _, clusterName := range clusters {
			union.Insert(clusterName)
			result.clusterPlacementRefs[clusterName] = append(result.clusterPlacementRefs[clusterName], ref.Name)
		}
	}
	result.clusters = sets.List(union)
	return result
}

func (r *ManagedServiceAccountReplicaSetReconciler) resolvePlacementRef(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	ref authv1alpha1.PlacementRef,
	generation int64,
	previousConditions []metav1.Condition,
) (*clusterv1beta1.Placement, clustersdkv1beta1.ClusterGroupsMap, authv1alpha1.PlacementStatus, error) {
	status := authv1alpha1.PlacementStatus{
		Name:       ref.Name,
		Conditions: previousConditions,
	}
	placement, clusterGroups, err := r.resolveOCMPlacement(ctx, set.Namespace, ref.Name)
	if err == nil {
		setCondition(&status.Conditions, conditionPlacementResolved, metav1.ConditionTrue, reasonResolved, "OCM Placement and PlacementDecision objects resolved", generation)
	}
	return placement, clusterGroups, status, err
}

func (r *ManagedServiceAccountReplicaSetReconciler) resolveOCMPlacement(ctx context.Context, namespace, placementName string) (*clusterv1beta1.Placement, clustersdkv1beta1.ClusterGroupsMap, error) {
	placement := &clusterv1beta1.Placement{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: placementName}, placement); err != nil {
		return nil, nil, err
	}
	tracker := clustersdkv1beta1.NewPlacementDecisionClustersTrackerWithGroups(
		placement,
		placementDecisionGetter{ctx: ctx, client: r.Client},
		nil,
	)
	if err := tracker.Refresh(); err != nil {
		return placement, nil, err
	}
	return placement, tracker.ExistingClusterGroupsBesides(), nil
}

type placementDecisionGetter struct {
	ctx    context.Context
	client client.Client
}

func (g placementDecisionGetter) List(selector labels.Selector, namespace string) ([]*clusterv1beta1.PlacementDecision, error) {
	var decisions clusterv1beta1.PlacementDecisionList
	if err := g.client.List(g.ctx, &decisions, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	out := make([]*clusterv1beta1.PlacementDecision, 0, len(decisions.Items))
	for i := range decisions.Items {
		out = append(out, &decisions.Items[i])
	}
	return out, nil
}

type rolloutPlan struct {
	clusters                []string
	cleanupClusters         sets.Set[string]
	statuses                map[string]clustersdkv1alpha1.ClusterRolloutStatus
	placementRollouts       map[string]*authv1alpha1.RolloutSummary
	availableDecisionGroups map[string]string
	summary                 *authv1alpha1.RolloutSummary
	requeueAfter            time.Duration
}

func (r *ManagedServiceAccountReplicaSetReconciler) planRollout(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	resolution placementResolution,
) (rolloutPlan, error) {
	plan := rolloutPlan{
		cleanupClusters:         sets.New[string](),
		statuses:                map[string]clustersdkv1alpha1.ClusterRolloutStatus{},
		placementRollouts:       map[string]*authv1alpha1.RolloutSummary{},
		availableDecisionGroups: map[string]string{},
	}
	rolloutClusters := sets.New[string]()
	timeoutClusters := sets.New[string]()
	for _, ref := range set.Spec.PlacementRefs {
		refKey := ref.Name
		clusterGroups := resolution.clusterGroupsByRef[refKey]
		placement := resolution.placementsByRef[refKey]
		if len(clusterGroups) == 0 {
			plan.placementRollouts[refKey] = newRolloutSummary(
				0,
				sets.New[string](),
				sets.New[string](),
				map[string]clustersdkv1alpha1.ClusterRolloutStatus{},
				placementRolloutConditions(set, refKey),
				set.Generation,
			)
			plan.availableDecisionGroups[refKey] = availableDecisionGroupsMessage(placement, clusterGroups, plan.statuses, nil)
			continue
		}

		refStatuses := map[string]clustersdkv1alpha1.ClusterRolloutStatus{}
		existingStatuses := make([]clustersdkv1alpha1.ClusterRolloutStatus, 0, len(clusterGroups.GetClusters()))
		for _, clusterName := range sets.List(clusterGroups.GetClusters()) {
			status, err := r.clusterRolloutStatus(ctx, set, clusterName, resolution.primaryRef(clusterName))
			// errChildConflict is reported per-cluster via reconcileChildren; do not abort planning.
			if err != nil && !errors.Is(err, errChildConflict) {
				return plan, err
			}
			plan.statuses[clusterName] = status
			refStatuses[clusterName] = status
			existingStatuses = append(existingStatuses, status)
		}

		tracker := clustersdkv1beta1.NewPlacementDecisionClustersTrackerWithGroups(nil, nil, clusterGroups)
		rolloutHandler, err := clustersdkv1alpha1.NewRolloutHandler[struct{}](tracker, noopClusterRolloutStatus)
		if err != nil {
			return plan, err
		}
		_, rolloutResult, err := rolloutHandler.GetRolloutCluster(defaultRolloutStrategy(ref.RolloutStrategy), existingStatuses)
		if err != nil {
			return plan, err
		}
		refRolloutClusters := sets.New[string]()
		refTimeoutClusters := sets.New[string]()
		// rolloutHandler re-queue intentionally overwrites Failed clusterRolloutStatus entries (e.g. errChildConflict); conflict surfaces via reconcileChildren independently.
		for _, cluster := range rolloutResult.ClustersToRollout {
			if cluster.ClusterName == "" {
				continue
			}
			refStatuses[cluster.ClusterName] = cluster
			plan.statuses[cluster.ClusterName] = cluster
			refRolloutClusters.Insert(cluster.ClusterName)
			rolloutClusters.Insert(cluster.ClusterName)
			plan.cleanupClusters.Insert(cluster.ClusterName)
		}
		for _, cluster := range rolloutResult.ClustersTimeOut {
			if cluster.ClusterName == "" {
				continue
			}
			refStatuses[cluster.ClusterName] = cluster
			plan.statuses[cluster.ClusterName] = cluster
			refTimeoutClusters.Insert(cluster.ClusterName)
			timeoutClusters.Insert(cluster.ClusterName)
		}
		plan.placementRollouts[refKey] = newRolloutSummary(
			int32(clusterGroups.GetClusters().Len()),
			refRolloutClusters,
			refTimeoutClusters,
			refStatuses,
			placementRolloutConditions(set, refKey),
			set.Generation,
		)
		plan.availableDecisionGroups[refKey] = availableDecisionGroupsMessage(placement, clusterGroups, refStatuses, refRolloutClusters)
		if rolloutResult.RecheckAfter != nil && *rolloutResult.RecheckAfter > 0 &&
			(plan.requeueAfter == 0 || *rolloutResult.RecheckAfter < plan.requeueAfter) {
			plan.requeueAfter = *rolloutResult.RecheckAfter
		}
	}
	plan.clusters = sets.List(rolloutClusters)
	plan.summary = newRolloutSummary(
		int32(len(resolution.clusters)),
		rolloutClusters,
		timeoutClusters,
		plan.statuses,
		rolloutConditions(set.Status.Rollout),
		set.Generation,
	)
	return plan, nil
}

func applyPlacementRolloutStatus(status *authv1alpha1.ManagedServiceAccountReplicaSetStatus, rollout rolloutPlan) {
	for i := range status.Placements {
		refKey := status.Placements[i].Name
		status.Placements[i].Rollout = rollout.placementRollouts[refKey]
		status.Placements[i].AvailableDecisionGroups = rollout.availableDecisionGroups[refKey]
	}
}

func applyPlacementRolledOutCondition(status *authv1alpha1.ManagedServiceAccountReplicaSetStatus, summary *authv1alpha1.RolloutSummary, generation int64) {
	if summary == nil {
		return
	}
	progressing := meta.FindStatusCondition(summary.Conditions, conditionProgressing)
	if progressing == nil {
		return
	}
	if progressing.Status == metav1.ConditionFalse && progressing.Reason == reasonCompleted {
		setCondition(&status.Conditions, conditionPlacementRolledOut, metav1.ConditionTrue, reasonComplete, progressing.Message, generation)
		return
	}
	setCondition(&status.Conditions, conditionPlacementRolledOut, metav1.ConditionFalse, reasonProgressing, progressing.Message, generation)
}

func placementRolloutConditions(set *authv1alpha1.ManagedServiceAccountReplicaSet, refKey string) []metav1.Condition {
	for _, placement := range set.Status.Placements {
		if placement.Name == refKey {
			return rolloutConditions(placement.Rollout)
		}
	}
	return nil
}

func rolloutConditions(summary *authv1alpha1.RolloutSummary) []metav1.Condition {
	if summary == nil {
		return nil
	}
	return slices.Clone(summary.Conditions)
}

func newRolloutSummary(
	total int32,
	updating sets.Set[string],
	timedOut sets.Set[string],
	statuses map[string]clustersdkv1alpha1.ClusterRolloutStatus,
	previousConditions []metav1.Condition,
	generation int64,
) *authv1alpha1.RolloutSummary {
	summary := &authv1alpha1.RolloutSummary{
		Total:      total,
		Updating:   int32(updating.Len()),
		TimedOut:   int32(timedOut.Len()),
		Conditions: previousConditions,
	}
	for clusterName, status := range statuses {
		if updating.Has(clusterName) || timedOut.Has(clusterName) {
			continue
		}
		switch status.Status {
		case clustersdkv1alpha1.Succeeded:
			summary.Succeeded++
		case clustersdkv1alpha1.Failed:
			summary.Failed++
		case clustersdkv1alpha1.TimeOut:
			summary.TimedOut++
		}
	}

	if summary.Total == 0 || summary.Succeeded != summary.Total {
		setCondition(&summary.Conditions, conditionProgressing, metav1.ConditionTrue, reasonProgressing,
			fmt.Sprintf("selected clusters %d. managed service accounts %d/%d progressing..., %d failed %d timeout.",
				summary.Total, summary.Updating+summary.Succeeded, summary.Total, summary.Failed, summary.TimedOut),
			generation)
	} else {
		setCondition(&summary.Conditions, conditionProgressing, metav1.ConditionFalse, reasonCompleted,
			fmt.Sprintf("selected clusters %d. managed service accounts %d/%d completed with no errors, %d failed %d timeout.",
				summary.Total, summary.Succeeded, summary.Total, summary.Failed, summary.TimedOut),
			generation)
	}
	return summary
}

func availableDecisionGroupsMessage(
	placement *clusterv1beta1.Placement,
	clusterGroups clustersdkv1beta1.ClusterGroupsMap,
	statuses map[string]clustersdkv1alpha1.ClusterRolloutStatus,
	applying sets.Set[string],
) string {
	totalGroups := len(clusterGroups)
	selectedClusters := clusterGroups.GetClusters()
	totalClusters := selectedClusters.Len()
	if placement != nil {
		if len(placement.Status.DecisionGroups) > 0 {
			totalGroups = len(placement.Status.DecisionGroups)
		}
		if placement.Status.NumberOfSelectedClusters > 0 {
			totalClusters = int(placement.Status.NumberOfSelectedClusters)
		}
	}

	appliedClusters := 0
	for clusterName, status := range statuses {
		if !selectedClusters.Has(clusterName) {
			continue
		}
		if applying.Has(clusterName) || status.Status != clustersdkv1alpha1.ToApply {
			appliedClusters++
		}
	}
	return fmt.Sprintf("%d (%d / %d clusters applied)", totalGroups, appliedClusters, totalClusters)
}

func noopClusterRolloutStatus(clusterName string, _ struct{}) (clustersdkv1alpha1.ClusterRolloutStatus, error) {
	return clustersdkv1alpha1.ClusterRolloutStatus{
		ClusterName: clusterName,
		Status:      clustersdkv1alpha1.ToApply,
	}, nil
}

func defaultRolloutStrategy(strategy clusterv1alpha1.RolloutStrategy) clusterv1alpha1.RolloutStrategy {
	if strategy.Type == "" {
		strategy.Type = clusterv1alpha1.All
	}
	return strategy
}

func (r *ManagedServiceAccountReplicaSetReconciler) clusterRolloutStatus(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	placementRefName string,
) (clustersdkv1alpha1.ClusterRolloutStatus, error) {
	status := clustersdkv1alpha1.ClusterRolloutStatus{
		ClusterName: clusterName,
		Status:      clustersdkv1alpha1.ToApply,
	}

	desiredMSA := buildManagedServiceAccount(set, clusterName, placementRefName)
	msa := &msav1beta1.ManagedServiceAccount{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(desiredMSA), msa); apierrors.IsNotFound(err) {
		return status, nil
	} else if err != nil {
		return status, err
	}
	if !ownedBySet(set, msa.GetLabels()) {
		// Stamp Failed so the rollout summary reflects the conflict for this cluster
		// even when planRollout swallows errChildConflict (it is per-cluster, not transient).
		status.Status = clustersdkv1alpha1.Failed
		return status, fmt.Errorf("%w: ManagedServiceAccount %s/%s is not owned by this set", errChildConflict, msa.Namespace, msa.Name)
	}
	if !childMatches(msa, desiredMSA, msaSpecOps) {
		return status, nil
	}
	msaStatus, transitionTime := managedServiceAccountRolloutStatus(msa)
	if msaStatus != clustersdkv1alpha1.Succeeded {
		status.Status = msaStatus
		status.LastTransitionTime = transitionTime
		return status, nil
	}
	successTime := transitionTime

	if selectorRBACEnabled(set) {
		accessMSAStatus, accessMSATime, err := r.existingManagedServiceAccountRolloutStatus(
			ctx, set, buildControllerAccessManagedServiceAccount(set, clusterName, placementRefName))
		if err != nil || accessMSAStatus != clustersdkv1alpha1.Succeeded {
			status.Status = accessMSAStatus
			status.LastTransitionTime = accessMSATime
			return status, err
		}
		successTime = newerTime(successTime, accessMSATime)

		accessWorkStatus, accessWorkTime, err := r.existingManifestWorkRolloutStatus(
			ctx, set, buildControllerAccessManifestWork(set, clusterName, placementRefName))
		if err != nil || accessWorkStatus != clustersdkv1alpha1.Succeeded {
			status.Status = accessWorkStatus
			status.LastTransitionTime = accessWorkTime
			return status, err
		}
		successTime = newerTime(successTime, accessWorkTime)
	}

	if rbacEnabled(set) {
		rbacSlices, err := desiredRBACSlices(ctx, set, clusterName, r.NamespaceSelectorResolver)
		if err != nil {
			status.Status = clustersdkv1alpha1.Progressing
			status.LastTransitionTime = successTime
			return status, err
		}
		for _, slice := range rbacSlices {
			workStatus, workTime, err := r.existingManifestWorkRolloutStatus(
				ctx, set, buildManifestWork(set, clusterName, placementRefName, slice))
			if err != nil || workStatus != clustersdkv1alpha1.Succeeded {
				status.Status = workStatus
				status.LastTransitionTime = workTime
				return status, err
			}
			successTime = newerTime(successTime, workTime)
		}
	}

	status.Status = clustersdkv1alpha1.Succeeded
	status.LastTransitionTime = successTime
	return status, nil
}

func (r *ManagedServiceAccountReplicaSetReconciler) existingManagedServiceAccountRolloutStatus(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	desired *msav1beta1.ManagedServiceAccount,
) (clustersdkv1alpha1.RolloutStatus, *metav1.Time, error) {
	current := &msav1beta1.ManagedServiceAccount{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), current); apierrors.IsNotFound(err) {
		return clustersdkv1alpha1.ToApply, nil, nil
	} else if err != nil {
		return clustersdkv1alpha1.ToApply, nil, err
	}
	if !ownedBySet(set, current.GetLabels()) {
		return clustersdkv1alpha1.Failed, nil,
			fmt.Errorf("%w: ManagedServiceAccount %s/%s is not owned by this set", errChildConflict, current.Namespace, current.Name)
	}
	if !childMatches(current, desired, msaSpecOps) {
		return clustersdkv1alpha1.ToApply, nil, nil
	}
	status, transitionTime := managedServiceAccountRolloutStatus(current)
	return status, transitionTime, nil
}

func (r *ManagedServiceAccountReplicaSetReconciler) existingManifestWorkRolloutStatus(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	desired *workv1.ManifestWork,
) (clustersdkv1alpha1.RolloutStatus, *metav1.Time, error) {
	current := &workv1.ManifestWork{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), current); apierrors.IsNotFound(err) {
		return clustersdkv1alpha1.ToApply, nil, nil
	} else if err != nil {
		return clustersdkv1alpha1.ToApply, nil, err
	}
	if !ownedBySet(set, current.GetLabels()) {
		return clustersdkv1alpha1.Failed, nil,
			fmt.Errorf("%w: ManifestWork %s/%s is not owned by this set", errChildConflict, current.Namespace, current.Name)
	}
	if !childMatches(current, desired, manifestWorkSpecOps) {
		return clustersdkv1alpha1.ToApply, nil, nil
	}
	status, transitionTime := manifestWorkRolloutStatus(current)
	return status, transitionTime, nil
}

func managedServiceAccountRolloutStatus(msa *msav1beta1.ManagedServiceAccount) (clustersdkv1alpha1.RolloutStatus, *metav1.Time) {
	tokenReported := meta.FindStatusCondition(msa.Status.Conditions, msav1beta1.ConditionTypeTokenReported)
	secretCreated := meta.FindStatusCondition(msa.Status.Conditions, msav1beta1.ConditionTypeSecretCreated)
	if managedServiceAccountReady(msa) {
		return clustersdkv1alpha1.Succeeded, newerTime(conditionTime(tokenReported), conditionTime(secretCreated))
	}
	return clustersdkv1alpha1.Progressing, newerTime(conditionTime(tokenReported), conditionTime(secretCreated))
}

func manifestWorkRolloutStatus(work *workv1.ManifestWork) (clustersdkv1alpha1.RolloutStatus, *metav1.Time) {
	progressing := meta.FindStatusCondition(work.Status.Conditions, workv1.WorkProgressing)
	degraded := meta.FindStatusCondition(work.Status.Conditions, workv1.WorkDegraded)
	applied := meta.FindStatusCondition(work.Status.Conditions, workv1.WorkApplied)
	if manifestWorkNeedsApply(work.Generation, applied, progressing, degraded) {
		return clustersdkv1alpha1.ToApply, nil
	}
	switch {
	case progressing.Status == metav1.ConditionTrue && degraded != nil && degraded.Status == metav1.ConditionTrue:
		return clustersdkv1alpha1.Failed, conditionTime(degraded)
	case progressing.Status == metav1.ConditionTrue:
		return clustersdkv1alpha1.Progressing, conditionTime(progressing)
	case progressing.Status == metav1.ConditionFalse:
		return clustersdkv1alpha1.Succeeded, conditionTime(progressing)
	default:
		return clustersdkv1alpha1.Progressing, conditionTime(progressing)
	}
}

func manifestWorkNeedsApply(generation int64, applied, progressing, degraded *metav1.Condition) bool {
	if !conditionObserved(applied, generation, metav1.ConditionTrue) {
		return true
	}
	if progressing == nil || progressing.ObservedGeneration != generation {
		return true
	}
	return degraded != nil && degraded.ObservedGeneration != generation
}

func conditionObserved(condition *metav1.Condition, generation int64, status metav1.ConditionStatus) bool {
	return condition != nil && condition.ObservedGeneration == generation && condition.Status == status
}

func conditionTime(condition *metav1.Condition) *metav1.Time {
	if condition == nil || condition.LastTransitionTime.IsZero() {
		return nil
	}
	t := condition.LastTransitionTime
	return &t
}

func newerTime(a, b *metav1.Time) *metav1.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(a.Time):
		return b
	default:
		return a
	}
}

type childReconcileResult struct {
	ready          int32
	conflicts      int32
	failures       int32
	cleanupPending bool
	err            error
}

func (r *childReconcileResult) recordErr(err error) {
	if errors.Is(err, errChildConflict) {
		r.conflicts++
		return
	}
	r.failures++
	r.err = errors.Join(r.err, err)
}

func (r *ManagedServiceAccountReplicaSetReconciler) reconcileChildren(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	resolution placementResolution,
	rolloutClusterNames []string,
	cleanupClusters sets.Set[string],
	rolloutStatuses map[string]clustersdkv1alpha1.ClusterRolloutStatus,
	status *authv1alpha1.ManagedServiceAccountReplicaSetStatus,
) childReconcileResult {
	result := childReconcileResult{}
	clusterNames := resolution.clusters
	desiredClusters := sets.New[string](clusterNames...)
	rolloutClusters := sets.New[string](rolloutClusterNames...)
	rbac := rbacEnabled(set)
	selectorRBAC := selectorRBACEnabled(set)
	desiredMSANameSet := desiredManagedServiceAccountNames(set)
	accessWorkName := controllerAccessManifestWorkName(set)
	desiredWorkNames := map[string]sets.Set[string]{}
	placementByKey := indexPlacementsByKey(status.Placements)
	if selectorRBAC {
		var previousAccessConditions []metav1.Condition
		if set.Status.ControllerAccess != nil {
			previousAccessConditions = slices.Clone(set.Status.ControllerAccess.Conditions)
		}
		status.ControllerAccess = &authv1alpha1.ControllerAccessStatus{
			DesiredClusterCount: int32(len(clusterNames)),
			Conditions:          previousAccessConditions,
		}
	}

	for _, clusterName := range clusterNames {
		desiredWorkNames[clusterName] = sets.New[string]()
		if !rolloutClusters.Has(clusterName) {
			if rolloutStatuses[clusterName].Status == clustersdkv1alpha1.Succeeded {
				result.ready++
				if selectorRBAC {
					status.ControllerAccess.ReadyClusterCount++
				}
				r.aggregateExistingRBACSummary(ctx, set, clusterName, resolution.primaryRef(clusterName), resolution.clusterPlacementRefs[clusterName], placementByKey, status)
			}
			continue
		}
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: clusterName}, &corev1.Namespace{}); err != nil {
			result.recordErr(err)
			continue
		}

		primaryRef := resolution.primaryRef(clusterName)
		clusterReady := true
		controllerAccessReady := true
		if selectorRBAC {
			if accessMSA, err := r.ensureControllerAccessManagedServiceAccount(ctx, set, clusterName, primaryRef); err != nil {
				clusterReady = false
				controllerAccessReady = false
				result.recordErr(err)
			} else if !managedServiceAccountReady(accessMSA) {
				clusterReady = false
				controllerAccessReady = false
			}
			desiredWorkNames[clusterName].Insert(accessWorkName)
			if accessWork, err := r.ensureControllerAccessManifestWork(ctx, set, clusterName, primaryRef); err != nil {
				clusterReady = false
				controllerAccessReady = false
				result.recordErr(err)
			} else if !manifestWorkAvailable(accessWork) {
				clusterReady = false
				controllerAccessReady = false
			}
			if controllerAccessReady {
				status.ControllerAccess.ReadyClusterCount++
			}
		}
		if msa, err := r.ensureManagedServiceAccount(ctx, set, clusterName, primaryRef); err != nil {
			clusterReady = false
			result.recordErr(err)
		} else if !managedServiceAccountReady(msa) {
			clusterReady = false
		}

		if rbac {
			rbacSlices, err := desiredRBACSlices(ctx, set, clusterName, r.NamespaceSelectorResolver)
			if err != nil {
				clusterReady = false
				result.recordErr(err)
			}
			desired := int32(len(rbacSlices))
			ensureSummary(&status.Summary).DesiredTotal += desired
			for _, refKey := range resolution.clusterPlacementRefs[clusterName] {
				if p := placementByKey[refKey]; p != nil {
					ensureSummary(&p.Summary).DesiredTotal += desired
				}
			}
			for _, slice := range rbacSlices {
				desiredWorkNames[clusterName].Insert(slice.name)
				work, err := r.ensureManifestWork(ctx, set, clusterName, primaryRef, slice)
				if err != nil {
					clusterReady = false
					result.recordErr(err)
					continue
				}
				workSummary := summarizeManifestWork(work, slice.manifestsHash)
				addSummary(ensureSummary(&status.Summary), workSummary)
				for _, refKey := range resolution.clusterPlacementRefs[clusterName] {
					if p := placementByKey[refKey]; p != nil {
						addSummary(ensureSummary(&p.Summary), workSummary)
					}
				}
				if !manifestWorkAvailable(work) {
					clusterReady = false
				}
			}
		}
		if clusterReady {
			result.ready++
		}
	}
	if selectorRBAC {
		if status.ControllerAccess.ReadyClusterCount == status.ControllerAccess.DesiredClusterCount {
			setCondition(&status.ControllerAccess.Conditions, conditionReady, metav1.ConditionTrue, reasonReady, "controller namespace access is ready", set.Generation)
		} else {
			setCondition(&status.ControllerAccess.Conditions, conditionReady, metav1.ConditionFalse, reasonWaitingForCredentials, "waiting for controller namespace access", set.Generation)
		}
	}
	if result.err != nil {
		return result
	}
	cleanupPending, err := r.deleteStaleChildren(ctx, set, desiredClusters, cleanupClusters, desiredWorkNames, desiredMSANameSet)
	result.cleanupPending = cleanupPending
	if err != nil {
		result.err = errors.Join(result.err, err)
	}
	return result
}

func (r *ManagedServiceAccountReplicaSetReconciler) aggregateExistingRBACSummary(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	placementRefName string,
	placementRefKeys []string,
	placementByKey map[string]*authv1alpha1.PlacementStatus,
	status *authv1alpha1.ManagedServiceAccountReplicaSetStatus,
) {
	if !rbacEnabled(set) {
		return
	}
	rbacSlices, err := desiredRBACSlices(ctx, set, clusterName, r.NamespaceSelectorResolver)
	if err != nil {
		// Mirror the active-rollout path (lines ~829-836): desiredRBACSlices returns
		// whatever it successfully resolved (cluster-role grants and already-resolved
		// namespace grants) alongside the joined error. Continue aggregating so the
		// cluster's partial DesiredTotal contribution remains observable in
		// status.Summary instead of being silently dropped, per
		// api-conventions.md L357-362 (counts complement Conditions; both must
		// reflect the full denominator) and the OCM ManifestWorkReplicaSet
		// "continue + aggregate" precedent.
		// Actionable misconfigurations are surfaced via the active-rollout path's
		// reasonApplyFailed Warning; avoid per-cluster duplicate events that defeat dedup.
		ctrl.LoggerFrom(ctx).Info(
			"partial RBAC summary aggregation for non-rolling cluster: namespace selector resolution failed; counting resolved slices in DesiredTotal",
			"replicaset", client.ObjectKeyFromObject(set),
			"cluster", clusterName,
			"err", err.Error(),
		)
	}
	desired := int32(len(rbacSlices))
	ensureSummary(&status.Summary).DesiredTotal += desired
	for _, refKey := range placementRefKeys {
		if p := placementByKey[refKey]; p != nil {
			ensureSummary(&p.Summary).DesiredTotal += desired
		}
	}
	for _, slice := range rbacSlices {
		work := &workv1.ManifestWork{}
		desiredWork := buildManifestWork(set, clusterName, placementRefName, slice)
		if err := r.Get(ctx, client.ObjectKeyFromObject(desiredWork), work); err != nil {
			continue
		}
		if !ownedBySet(set, work.GetLabels()) || !childMatches(work, desiredWork, manifestWorkSpecOps) {
			continue
		}
		workSummary := summarizeManifestWork(work, slice.manifestsHash)
		addSummary(ensureSummary(&status.Summary), workSummary)
		for _, refKey := range placementRefKeys {
			if p := placementByKey[refKey]; p != nil {
				addSummary(ensureSummary(&p.Summary), workSummary)
			}
		}
	}
}

func ensureSummary(slot **authv1alpha1.Summary) *authv1alpha1.Summary {
	if *slot == nil {
		*slot = &authv1alpha1.Summary{}
	}
	return *slot
}

func indexPlacementsByKey(placements []authv1alpha1.PlacementStatus) map[string]*authv1alpha1.PlacementStatus {
	idx := make(map[string]*authv1alpha1.PlacementStatus, len(placements))
	for i := range placements {
		idx[placements[i].Name] = &placements[i]
	}
	return idx
}

func (r *ManagedServiceAccountReplicaSetReconciler) stopNamespaceCaches(set *authv1alpha1.ManagedServiceAccountReplicaSet, keepClusters sets.Set[string]) {
	if janitor, ok := r.NamespaceSelectorResolver.(NamespaceCacheJanitor); ok {
		janitor.StopNamespaceCaches(set, keepClusters)
	}
}

var errChildConflict = errors.New("generated child conflicts with an existing object")

type childSpecOps[T client.Object] struct {
	apply func(current, desired T)
	equal func(current, desired T) bool
}

func ensureChild[T client.Object](
	ctx context.Context,
	r *ManagedServiceAccountReplicaSetReconciler,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	desired T,
	current T,
	ops childSpecOps[T],
) (T, error) {
	var zero T
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		return desired, r.Create(ctx, desired)
	}
	if err != nil {
		return zero, err
	}
	if !ownedBySet(set, current.GetLabels()) {
		kind := desired.GetObjectKind().GroupVersionKind().Kind
		return zero, fmt.Errorf("%w: %s %s/%s is not owned by this set", errChildConflict, kind, current.GetNamespace(), current.GetName())
	}
	if childMatches(current, desired, ops) {
		return current, nil
	}
	base := current.DeepCopyObject().(client.Object)
	current.SetLabels(desired.GetLabels())
	current.SetAnnotations(desired.GetAnnotations())
	ops.apply(current, desired)
	if err := r.Patch(ctx, current, client.MergeFrom(base)); err != nil {
		return zero, err
	}
	return current, nil
}

func childMatches[T client.Object](current, desired T, ops childSpecOps[T]) bool {
	return equality.Semantic.DeepEqual(current.GetLabels(), desired.GetLabels()) &&
		equality.Semantic.DeepEqual(current.GetAnnotations(), desired.GetAnnotations()) &&
		ops.equal(current, desired)
}

var msaSpecOps = childSpecOps[*msav1beta1.ManagedServiceAccount]{
	apply: func(current, desired *msav1beta1.ManagedServiceAccount) { current.Spec = desired.Spec },
	equal: func(current, desired *msav1beta1.ManagedServiceAccount) bool {
		return equality.Semantic.DeepEqual(current.Spec, desired.Spec)
	},
}

var manifestWorkSpecOps = childSpecOps[*workv1.ManifestWork]{
	apply: func(current, desired *workv1.ManifestWork) { current.Spec = desired.Spec },
	equal: func(current, desired *workv1.ManifestWork) bool {
		return manifestWorkSpecsEqual(current.Spec, desired.Spec)
	},
}

// manifestWorkSpecsEqual compares two ManifestWorkSpecs structurally while
// reconciling the RawExtension representation gap: the desired side carries
// RawExtension{Object:...} (built by render*) while the stored side carries
// RawExtension{Raw:...} (deserialized from the API server). OCM SDK's
// workapplier.ManifestWorkEqual normalizes only via Raw and therefore
// false-negatives on Object-form desired manifests; apimachinery exposes no
// public RawExtension semantic equality. The JSON normalization below is the
// reason this helper exists at all.
func manifestWorkSpecsEqual(current, desired workv1.ManifestWorkSpec) bool {
	currentManifests := current.Workload.Manifests
	desiredManifests := desired.Workload.Manifests
	if len(currentManifests) != len(desiredManifests) {
		return false
	}

	current.Workload.Manifests = nil
	desired.Workload.Manifests = nil
	if !equality.Semantic.DeepEqual(current, desired) {
		return false
	}

	for i := range currentManifests {
		if !rawExtensionsEqual(currentManifests[i].RawExtension, desiredManifests[i].RawExtension) {
			return false
		}
	}
	return true
}

func rawExtensionsEqual(current, desired runtime.RawExtension) bool {
	currentJSON, ok := rawExtensionJSON(current)
	if !ok {
		return false
	}
	desiredJSON, ok := rawExtensionJSON(desired)
	if !ok {
		return false
	}
	if len(currentJSON) == 0 || len(desiredJSON) == 0 {
		return len(currentJSON) == len(desiredJSON)
	}
	// Fast path: identical bytes need no unmarshal. This skips the per-manifest
	// JSON parse + reflect.DeepEqual on the steady state where both sides came
	// from the API server (both Raw, byte-equal).
	if bytes.Equal(currentJSON, desiredJSON) {
		return true
	}

	var currentValue, desiredValue any
	if err := json.Unmarshal(currentJSON, &currentValue); err != nil {
		return false
	}
	if err := json.Unmarshal(desiredJSON, &desiredValue); err != nil {
		return false
	}
	return equality.Semantic.DeepEqual(currentValue, desiredValue)
}

func rawExtensionJSON(raw runtime.RawExtension) ([]byte, bool) {
	if len(raw.Raw) > 0 {
		return raw.Raw, true
	}
	if raw.Object == nil {
		return nil, true
	}
	data, err := json.Marshal(raw.Object)
	return data, err == nil
}

func (r *ManagedServiceAccountReplicaSetReconciler) ensureManagedServiceAccount(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	clusterName string,
	placementRefName string,
) (*msav1beta1.ManagedServiceAccount, error) {
	desired := buildManagedServiceAccount(set, clusterName, placementRefName)
	return ensureChild(ctx, r, set, desired, &msav1beta1.ManagedServiceAccount{}, msaSpecOps)
}

func buildManagedServiceAccount(set *authv1alpha1.ManagedServiceAccountReplicaSet, clusterName, placementRefName string) *msav1beta1.ManagedServiceAccount {
	validity := defaultTokenValidity
	if set.Spec.Template.Spec.Rotation.Validity != nil {
		validity = *set.Spec.Template.Spec.Rotation.Validity
	}
	return &msav1beta1.ManagedServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: msav1beta1.GroupVersion.String(),
			Kind:       "ManagedServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        set.Spec.Template.Metadata.Name,
			Namespace:   clusterName,
			Labels:      childLabels(set, placementRefName, set.Spec.Template.Metadata.Labels),
			Annotations: childAnnotations(set, set.Spec.Template.Metadata.Annotations),
		},
		Spec: msav1beta1.ManagedServiceAccountSpec{
			Rotation: msav1beta1.ManagedServiceAccountRotation{
				// Nil falls back to the CRD default (true) so SSA users that
				// omit the field keep rotation on; explicit false stays false.
				Enabled:  ptr.Deref(set.Spec.Template.Spec.Rotation.Enabled, true),
				Validity: validity,
			},
			TTLSecondsAfterCreation: set.Spec.Template.Spec.TTLSecondsAfterCreation,
		},
	}
}

// deleteStaleChildren gates rename cleanup by the current rollout window
// (cleanupClusters): clusters still in desiredClusters but outside the window
// keep their stale children until their next rollout window opens. Removal
// from desiredClusters is unconditional. Mirrors OCM ManifestWorkReplicaSet
// RolloutStrategy gating in ocm pkg/work/hub/controllers/manifestworkreplicasetcontroller.
func (r *ManagedServiceAccountReplicaSetReconciler) deleteStaleChildren(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	desiredClusters sets.Set[string],
	cleanupClusters sets.Set[string],
	desiredWorkNames map[string]sets.Set[string],
	desiredMSANames sets.Set[string],
) (bool, error) {
	var joined error
	var works workv1.ManifestWorkList
	if err := r.List(ctx, &works, generatedChildListOptions(set)...); err != nil {
		return false, err
	}
	sawStale := false
	for i := range works.Items {
		work := &works.Items[i]
		desiredNames := desiredWorkNames[work.Namespace]
		if !desiredClusters.Has(work.Namespace) || (cleanupClusters.Has(work.Namespace) && !desiredNames.Has(work.Name)) {
			sawStale = true
			if err := r.Delete(ctx, work); client.IgnoreNotFound(err) != nil {
				joined = errors.Join(joined, err)
			}
		}
	}
	if joined != nil || sawStale {
		return true, joined
	}

	var msas msav1beta1.ManagedServiceAccountList
	if err := r.List(ctx, &msas, generatedChildListOptions(set)...); err != nil {
		return false, err
	}
	for i := range msas.Items {
		msa := &msas.Items[i]
		if !desiredClusters.Has(msa.Namespace) || (cleanupClusters.Has(msa.Namespace) && !desiredMSANames.Has(msa.Name)) {
			if err := r.Delete(ctx, msa); client.IgnoreNotFound(err) != nil {
				joined = errors.Join(joined, err)
			}
		}
	}
	return false, joined
}

func generatedChildListOptions(set *authv1alpha1.ManagedServiceAccountReplicaSet) []client.ListOption {
	return []client.ListOption{client.MatchingLabelsSelector{Selector: ownedSelector(set)}}
}

func managedServiceAccountReady(msa *msav1beta1.ManagedServiceAccount) bool {
	return meta.IsStatusConditionTrue(msa.Status.Conditions, msav1beta1.ConditionTypeTokenReported) ||
		meta.IsStatusConditionTrue(msa.Status.Conditions, msav1beta1.ConditionTypeSecretCreated)
}

func (r *ManagedServiceAccountReplicaSetReconciler) reconcileDelete(ctx context.Context, set *authv1alpha1.ManagedServiceAccountReplicaSet) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	var works workv1.ManifestWorkList
	if err := r.List(ctx, &works, generatedChildListOptions(set)...); err != nil {
		return ctrl.Result{}, err
	}
	var joined error
	for i := range works.Items {
		if !works.Items[i].DeletionTimestamp.IsZero() {
			log.V(1).Info("skipping already terminating ManifestWork", "name", works.Items[i].Name)
			continue
		}
		if err := r.Delete(ctx, &works.Items[i]); client.IgnoreNotFound(err) != nil {
			joined = errors.Join(joined, err)
		}
	}
	if joined != nil {
		return ctrl.Result{}, joined
	}
	if len(works.Items) > 0 {
		log.V(1).Info("waiting for ManifestWork cleanup", "remaining", len(works.Items))
		return r.waitForCleanup(ctx, set, len(works.Items), 0)
	}

	var msas msav1beta1.ManagedServiceAccountList
	if err := r.List(ctx, &msas, generatedChildListOptions(set)...); err != nil {
		return ctrl.Result{}, err
	}
	for i := range msas.Items {
		if !msas.Items[i].DeletionTimestamp.IsZero() {
			log.V(1).Info("skipping already terminating ManagedServiceAccount", "name", msas.Items[i].Name)
			continue
		}
		if err := r.Delete(ctx, &msas.Items[i]); client.IgnoreNotFound(err) != nil {
			joined = errors.Join(joined, err)
		}
	}
	if joined != nil {
		return ctrl.Result{}, joined
	}
	if len(msas.Items) > 0 {
		log.V(1).Info("waiting for ManagedServiceAccount cleanup", "remaining", len(msas.Items))
		return r.waitForCleanup(ctx, set, 0, len(msas.Items))
	}

	base := set.DeepCopy()
	if !controllerutil.RemoveFinalizer(set, finalizerName) {
		return ctrl.Result{}, nil
	}
	// Symmetric optimistic lock to the add path: another controller may have
	// added or removed its own finalizer entry since we Get'd the object, and
	// a stale-RV merge patch would silently overwrite that.
	if err := r.Patch(ctx, set, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("cleanup complete, removed finalizer")
	return ctrl.Result{}, nil
}

func (r *ManagedServiceAccountReplicaSetReconciler) waitForCleanup(ctx context.Context, set *authv1alpha1.ManagedServiceAccountReplicaSet, remainingWorks, remainingMSAs int) (ctrl.Result, error) {
	status := newStatus(set)
	status.ObservedGeneration = set.Generation
	msg := fmt.Sprintf("waiting for cleanup of %d ManifestWork and %d ManagedServiceAccount objects", remainingWorks, remainingMSAs)
	setCondition(&status.Conditions, conditionCleanupBlocked, metav1.ConditionTrue, reasonWaitingForOCMCleanup, msg, set.Generation)
	setCondition(&status.Conditions, conditionReady, metav1.ConditionFalse, reasonCleanupBlocked, msg, set.Generation)
	if err := r.patchStatus(ctx, set, status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: cleanupRequeueInterval}, nil
}

func newStatus(set *authv1alpha1.ManagedServiceAccountReplicaSet) authv1alpha1.ManagedServiceAccountReplicaSetStatus {
	return authv1alpha1.ManagedServiceAccountReplicaSetStatus{
		ObservedGeneration: set.Status.ObservedGeneration,
		Conditions:         slices.Clone(set.Status.Conditions),
	}
}

func (r *ManagedServiceAccountReplicaSetReconciler) patchStatus(
	ctx context.Context,
	set *authv1alpha1.ManagedServiceAccountReplicaSet,
	status authv1alpha1.ManagedServiceAccountReplicaSetStatus,
) error {
	if equality.Semantic.DeepEqual(set.Status, status) {
		return nil
	}
	base := set.DeepCopy()
	set.Status = status
	return r.Status().Patch(ctx, set, client.MergeFrom(base))
}

func setCondition(conditions *[]metav1.Condition, conditionType string, conditionStatus metav1.ConditionStatus, reason, message string, generation int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func validateRuntimeSpec(set *authv1alpha1.ManagedServiceAccountReplicaSet) error {
	if len(set.Spec.PlacementRefs) == 0 {
		return errors.New("spec.placementRefs must be non-empty")
	}
	for _, ref := range set.Spec.PlacementRefs {
		if ref.Name == "" {
			return errors.New("spec.placementRefs[].name is required")
		}
		switch defaultRolloutStrategy(ref.RolloutStrategy).Type {
		case clusterv1alpha1.All, clusterv1alpha1.Progressive, clusterv1alpha1.ProgressivePerGroup:
		default:
			return fmt.Errorf("unsupported placementRef %q rolloutStrategy.type %q", ref.Name, ref.RolloutStrategy.Type)
		}
	}
	if set.Spec.Template.Metadata.Name == "" || set.Spec.Template.Metadata.Namespace == "" {
		return errors.New("spec.template.metadata.name and namespace are required")
	}
	if set.Spec.RBAC != nil {
		ids := sets.New[string]()
		for _, grant := range set.Spec.RBAC.Grants {
			if grant.ID == "" {
				return errors.New("spec.rbac.grants[].id is required")
			}
			if ids.Has(grant.ID) {
				return fmt.Errorf("spec.rbac.grants[].id %q must be unique", grant.ID)
			}
			ids.Insert(grant.ID)
			if grant.Metadata.Name == "" {
				return fmt.Errorf("spec.rbac.grants[%q].metadata.name is required", grant.ID)
			}
			if len(grant.Rules) == 0 {
				return fmt.Errorf("spec.rbac.grants[%q].rules must be non-empty", grant.ID)
			}
			if err := validateGrantMetadata(grant); err != nil {
				return err
			}
			namespaceModes := 0
			if grant.ForEachNamespace != nil {
				if grant.ForEachNamespace.Name != "" {
					namespaceModes++
				}
				if len(grant.ForEachNamespace.Names) > 0 {
					namespaceModes++
				}
				if grant.ForEachNamespace.Selector != nil {
					namespaceModes++
				}
			}
			switch grant.Type {
			case authv1alpha1.RBACGrantTypeRole:
				if namespaceModes != 1 {
					return fmt.Errorf("spec.rbac.grants[%q] type Role requires exactly one forEachNamespace mode", grant.ID)
				}
			case authv1alpha1.RBACGrantTypeClusterRole:
				if namespaceModes != 0 {
					return fmt.Errorf("spec.rbac.grants[%q] type ClusterRole must not set forEachNamespace", grant.ID)
				}
			default:
				return fmt.Errorf("spec.rbac.grants[%q].type %q is unsupported", grant.ID, grant.Type)
			}
		}
		if err := validateGrantObjectNames(set.Spec.RBAC.Grants); err != nil {
			return err
		}
	}
	return nil
}

func validateGrantObjectNames(grants []authv1alpha1.RBACGrant) error {
	clusterNames := sets.New[string]()
	namespacedNames := sets.New[string]()
	for _, grant := range grants {
		switch grant.Type {
		case authv1alpha1.RBACGrantTypeClusterRole:
			if clusterNames.Has(grant.Metadata.Name) {
				return fmt.Errorf("spec.rbac.grants contains duplicate ClusterRole metadata.name %q", grant.Metadata.Name)
			}
			clusterNames.Insert(grant.Metadata.Name)
		case authv1alpha1.RBACGrantTypeRole:
			if grant.ForEachNamespace == nil {
				continue
			}
			namespaces := sets.New[string]()
			if grant.ForEachNamespace.Name != "" {
				namespaces.Insert(grant.ForEachNamespace.Name)
			}
			namespaces.Insert(grant.ForEachNamespace.Names...)
			for _, namespace := range sets.List(namespaces) {
				key := namespace + "/" + grant.Metadata.Name
				if namespacedNames.Has(key) {
					return fmt.Errorf("spec.rbac.grants contains duplicate Role metadata.name %q for namespace %q", grant.Metadata.Name, namespace)
				}
				namespacedNames.Insert(key)
			}
		}
	}
	return nil
}

func validateGrantMetadata(grant authv1alpha1.RBACGrant) error {
	for key := range grant.Metadata.Labels {
		if reservedGrantMetadataKeys.Has(key) {
			return fmt.Errorf("spec.rbac.grants[%q].metadata.labels contains reserved key %q", grant.ID, key)
		}
	}
	for key := range grant.Metadata.Annotations {
		if reservedGrantMetadataKeys.Has(key) {
			return fmt.Errorf("spec.rbac.grants[%q].metadata.annotations contains reserved key %q", grant.ID, key)
		}
	}
	return nil
}

func setEarlyReturnConditions(status *authv1alpha1.ManagedServiceAccountReplicaSetStatus, reason, msg string, generation int64) {
	for _, t := range []string{
		conditionPlacementResolved,
		conditionManagedServiceAccountsReady,
		conditionRemotePermissionsApplied,
		conditionPlacementRolledOut,
		conditionReady,
	} {
		setCondition(&status.Conditions, t, metav1.ConditionFalse, reason, msg, generation)
	}
	setNotDeletingCondition(status, generation)
}

func setNotDeletingCondition(status *authv1alpha1.ManagedServiceAccountReplicaSetStatus, generation int64) {
	setCondition(&status.Conditions, conditionCleanupBlocked, metav1.ConditionFalse, reasonNotDeleting, "cleanup is not active", generation)
}

// A nil Summary means no RBAC children were aggregated this reconcile; treat that as not pending.
func summaryRBACPending(s *authv1alpha1.Summary) bool {
	if s == nil {
		return false
	}
	return s.Available < s.DesiredTotal
}

func applyChildResultConditions(
	status *authv1alpha1.ManagedServiceAccountReplicaSetStatus,
	result childReconcileResult,
	totalClusters int,
	generation int64,
	remoteEnabled bool,
) {
	setBoth := func(s metav1.ConditionStatus, reason, msg string) {
		setCondition(&status.Conditions, conditionManagedServiceAccountsReady, s, reason, msg, generation)
		setCondition(&status.Conditions, conditionReady, s, reason, msg, generation)
	}

	switch {
	case result.err != nil:
		setBoth(metav1.ConditionFalse, reasonApplyFailed, result.err.Error())
	case result.conflicts > 0:
		setBoth(metav1.ConditionFalse, reasonChildConflict, msgChildConflict)
	case result.failures > 0:
		setBoth(metav1.ConditionFalse, reasonClusterFailed, msgClusterFailed)
	case result.ready < int32(totalClusters):
		if remoteEnabled && summaryRBACPending(status.Summary) {
			setBoth(metav1.ConditionFalse, reasonWaitingForRBAC, msgWaitingForRBAC)
		} else {
			setBoth(metav1.ConditionFalse, reasonWaitingForCredentials, msgWaitingForCredentials)
		}
	default:
		setCondition(&status.Conditions, conditionManagedServiceAccountsReady, metav1.ConditionTrue, reasonReady, "generated ManagedServiceAccounts are ready", generation)
		setCondition(&status.Conditions, conditionReady, metav1.ConditionTrue, reasonReady, "all selected clusters are reconciled", generation)
	}

	switch {
	case !remoteEnabled:
		setCondition(&status.Conditions, conditionRemotePermissionsApplied, metav1.ConditionTrue, reasonDisabled, "remote permissions are disabled", generation)
	case result.err != nil || result.conflicts > 0 || result.failures > 0:
		setCondition(&status.Conditions, conditionRemotePermissionsApplied, metav1.ConditionFalse, reasonApplyFailed, "remote permissions were not applied to every selected cluster", generation)
	case summaryRBACPending(status.Summary):
		setCondition(&status.Conditions, conditionRemotePermissionsApplied, metav1.ConditionFalse, reasonWaitingForRBAC, msgWaitingForRBAC, generation)
	default:
		setCondition(&status.Conditions, conditionRemotePermissionsApplied, metav1.ConditionTrue, reasonApplied, "remote permissions were applied", generation)
	}
}

const indexPlacementRefNames = "placementRefNames"

func (r *ManagedServiceAccountReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder(controllerName)
	}
	if r.Scheme == nil {
		r.Scheme = mgr.GetScheme()
	}
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&authv1alpha1.ManagedServiceAccountReplicaSet{},
		indexPlacementRefNames,
		func(obj client.Object) []string {
			set, ok := obj.(*authv1alpha1.ManagedServiceAccountReplicaSet)
			if !ok {
				return nil
			}
			names := sets.New[string]()
			for _, ref := range set.Spec.PlacementRefs {
				names.Insert(ref.Name)
			}
			return sets.List(names)
		},
	); err != nil {
		return err
	}
	// Status-driven controller: status-only updates must re-trigger Reconcile so
	// stale transient conditions get cleared. Do NOT add GenerationChangedPredicate to For().
	b := ctrl.NewControllerManagedBy(mgr).
		For(&authv1alpha1.ManagedServiceAccountReplicaSet{}).
		Watches(&clusterv1beta1.Placement{}, handler.EnqueueRequestsFromMapFunc(r.requestsForOCMPlacement)).
		Watches(&clusterv1beta1.PlacementDecision{}, handler.EnqueueRequestsFromMapFunc(r.requestsForOCMPlacementDecision)).
		Watches(&msav1beta1.ManagedServiceAccount{}, handler.EnqueueRequestsFromMapFunc(requestsForGeneratedChild)).
		Watches(&workv1.ManifestWork{}, handler.EnqueueRequestsFromMapFunc(requestsForGeneratedChild)).
		Watches(&civ1alpha1.ClusterProfile{}, handler.EnqueueRequestsFromMapFunc(r.requestsForClusterProfile))
	if namespaceEvents, ok := r.NamespaceSelectorResolver.(NamespaceEventSource); ok {
		b = b.WatchesRawSource(source.Channel(namespaceEvents.NamespaceEvents(), &handler.EnqueueRequestForObject{}))
	}

	return b.Complete(r)
}

func (r *ManagedServiceAccountReplicaSetReconciler) requestsForOCMPlacement(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.requestsForPlacementRefName(ctx, obj.GetNamespace(), obj.GetName())
}

func (r *ManagedServiceAccountReplicaSetReconciler) requestsForOCMPlacementDecision(ctx context.Context, obj client.Object) []reconcile.Request {
	placementName := obj.GetLabels()[clusterv1beta1.PlacementLabel]
	if placementName == "" {
		return nil
	}
	return r.requestsForPlacementRefName(ctx, obj.GetNamespace(), placementName)
}

func (r *ManagedServiceAccountReplicaSetReconciler) requestsForPlacementRefName(ctx context.Context, namespace, placementName string) []reconcile.Request {
	var matched authv1alpha1.ManagedServiceAccountReplicaSetList
	if err := r.List(ctx, &matched,
		client.InNamespace(namespace),
		client.MatchingFields{indexPlacementRefNames: placementName},
	); err != nil {
		ctrl.LoggerFrom(ctx).V(1).Info("listing ManagedServiceAccountReplicaSets by placementRef name failed; lost reconcile trigger", "namespace", namespace, "placementName", placementName, "err", err)
		return nil
	}
	return replicaSetListToRequests(matched)
}

// requestsForClusterProfile enqueues ManagedServiceAccountReplicaSet objects
// when an OCM ClusterProfile changes, because Reconcile depends on
// ClusterProfile.Status.AccessProviders / CredentialProviders via the namespace
// resolver. Without this Watches, ClusterProfile changes would only propagate
// via the selectorRequeueInterval poll. ClusterProfile is cluster-scoped, so
// any ReplicaSet may be affected; enqueue all and let Reconcile filter.
func (r *ManagedServiceAccountReplicaSetReconciler) requestsForClusterProfile(ctx context.Context, obj client.Object) []reconcile.Request {
	if _, ok := obj.(*civ1alpha1.ClusterProfile); !ok {
		return nil
	}
	var matched authv1alpha1.ManagedServiceAccountReplicaSetList
	if err := r.List(ctx, &matched); err != nil {
		ctrl.LoggerFrom(ctx).V(1).Info("listing ManagedServiceAccountReplicaSets for ClusterProfile event failed; lost reconcile trigger", "clusterProfile", client.ObjectKeyFromObject(obj), "err", err)
		return nil
	}
	return replicaSetListToRequests(matched)
}

func replicaSetListToRequests(list authv1alpha1.ManagedServiceAccountReplicaSetList) []reconcile.Request {
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace,
			Name:      list.Items[i].Name,
		}})
	}
	return requests
}

func requestsForGeneratedChild(_ context.Context, obj client.Object) []reconcile.Request {
	key, ok := requestFromSourceLabels(obj.GetLabels())
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: key}}
}

func (r *ManagedServiceAccountReplicaSetReconciler) recordWarningEvent(obj runtime.Object, reason, action, message string) {
	if r.Recorder == nil {
		return
	}
	// %s avoids interpreting % in the message (often an err.Error()) as a format directive.
	r.Recorder.Eventf(obj, nil, corev1.EventTypeWarning, reason, action, "%s", message)
}
