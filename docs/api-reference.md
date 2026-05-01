# API Reference

`ManagedServiceAccountReplicaSet` is a namespaced alpha API:

```yaml
apiVersion: authentication.appthrust.io/v1alpha1
kind: ManagedServiceAccountReplicaSet
```

## Spec

`spec.placementRefs` selects target clusters from OCM `Placement` and
`PlacementDecision` output in the same namespace as the replica set. At least
one ref is required and at most 16 refs are allowed. Each ref names an OCM
`Placement`; the controller reads that `Placement` and its labeled
`PlacementDecision` objects through the OCM placement SDK tracker.

The controller union-deduplicates selected cluster names across all refs.

Each placement ref may set `rolloutStrategy`, using the OCM
`cluster.open-cluster-management.io/v1alpha1` rollout shape:

```yaml
placementRefs:
  - name: prod-clusters
    rolloutStrategy:
      type: Progressive
      progressive:
        maxConcurrency: 10%
        maxFailures: 1
        progressDeadline: 30m
```

When omitted, rollout defaults to `type: All`. `Progressive` and
`ProgressivePerGroup` are evaluated from the selected placement-decision
clusters and OCM decision group labels.

`status.rollout` and `status.placements[*].rollout` report OCM-style rollout
counts: `total`, `updating`, `succeeded`, `failed`, `timedOut`, plus a
`Progressing` condition. Per-placement status also includes
`availableDecisionGroups` in the same progress message shape used by OCM
ManifestWorkReplicaSet.

`spec.template.metadata.name` is the generated hub-side
`ManagedServiceAccount` name in every managed cluster namespace.

`spec.template.metadata.namespace` is the namespace where the service account is
installed on each managed cluster. It is not the hub namespace where the OCM
`ManagedServiceAccount` child lives.

`spec.template.metadata.labels` and `annotations` are copied to generated
children after reserved controller keys are removed. The controller always adds
source labels and annotations so stale children can be found without reading
remote clusters. Unless explicitly set by the user, it adds
`authentication.open-cluster-management.io/sync-to-clusterprofile=true`.

`spec.template.spec.rotation.enabled` is passed through to the OCM
`ManagedServiceAccount`. When validity is omitted, the controller requests
360 days.

`spec.template.spec.ttlSecondsAfterCreation` is passed through only when set.

`spec.rbac` is optional. It is disabled when omitted or when `grants` is empty.

`spec.rbac.grants` is a typed grant list. Each entry has a stable `id`,
`type`, generated RBAC `metadata`, and `rules`. `type: Role` requires
`forEachNamespace` with exactly one of `name`, `names`, or `selector`.
`type: ClusterRole` must omit `forEachNamespace`.

Role grants render one remote `Role` and matching `RoleBinding` per target
namespace. ClusterRole grants render one remote `ClusterRole` and matching
`ClusterRoleBinding`. The subject is always the generated managed service
account.

Selector-targeted Role grants require the manager to be started with
`--clusterprofile-provider-file`; fixed `name` and `names` grants do not.

## Generated Children

For each selected cluster, the controller creates:

- `ManagedServiceAccount` named `spec.template.metadata.name` in the managed
  cluster namespace on the hub.
- `ManagedServiceAccount` named `<replicaset-name>-controller-access` only when
  selector-targeted grants require controller namespace reads.
- `ManifestWork` named `<replicaset-name>-access-rbac` only when selector
  grants are present. It grants the controller-access MSA only
  `namespaces get/list/watch`.
- `ManifestWork` named `<replicaset-name>-rbac-cluster` when cluster-scoped
  grants are desired.
- `ManifestWork` named `<replicaset-name>-rbac-ns-<namespace-hash>` for each
  target namespace with namespaced grants.

Generated children carry these labels:

- `app.kubernetes.io/managed-by`
- `app.kubernetes.io/part-of`
- `authentication.appthrust.io/set-name`
- `authentication.appthrust.io/set-namespace`
- `authentication.appthrust.io/set-uid`
- `authentication.appthrust.io/placement-ref-name`
- `authentication.appthrust.io/slice-type` on RBAC `ManifestWork` slices
- `authentication.appthrust.io/grant-id` on generated remote RBAC objects

Generated children carry source annotations for set name, namespace, and UID.
RBAC `ManifestWork` children also carry `authentication.appthrust.io/spec-hash`
and namespace slices carry `authentication.appthrust.io/target-namespace`.

## Status

`status.observedGeneration` is the last reconciled generation.

`status.selectedClusterCount` is the union-deduplicated cluster count.

`status.readyClusterCount` counts selected clusters whose
`ManagedServiceAccount` is ready and whose RBAC `ManifestWork`, when present, is
available.

`status.summary` aggregates RBAC `ManifestWork` state.

`status.controllerAccess` is present only when selector-targeted grants require
the internal namespace-reader bootstrap access. It reports desired and ready
cluster counts plus a `Ready` condition for that controller-only access path.

`status.placements` reports resolution and RBAC summary per placement ref. When
refs overlap, each ref's summary includes the shared cluster's RBAC work.

Top-level conditions:

- `PlacementResolved`
- `PlacementRolledOut`
- `ManagedServiceAccountsReady`
- `RemotePermissionsApplied`
- `CleanupBlocked`
- `Ready`
