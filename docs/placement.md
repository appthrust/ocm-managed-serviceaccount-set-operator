# Placement Behavior

The controller delegates cluster selection to placement APIs. It does not read
Cluster API `Cluster` objects and does not reimplement cluster set selection.

## Placement Refs

Use:

```yaml
name: prod-clusters
```

Each ref names an OCM `Placement` in the replica set namespace. The controller
reads that `Placement` and its generated OCM `PlacementDecision` objects with:

```yaml
cluster.open-cluster-management.io/placement: prod-clusters
```

The controller feeds those objects through the OCM placement SDK tracker, so
the API behavior matches OCM rollout controllers. If the `Placement` is missing
or the decision output cannot be listed, the ref reports
`PlacementResolved=False`. If the `Placement` resolves successfully but selects
zero clusters, the ref reports `PlacementEmpty=True`.

OCM decision group labels are required on generated decisions and honored, so
`rolloutStrategy.type: ProgressivePerGroup` and mandatory decision groups use
the same group names and indexes emitted on OCM `PlacementDecision` objects.

Per-ref status includes `availableDecisionGroups`, matching OCM
ManifestWorkReplicaSet's progress message shape, and `rollout` counts for
updating, succeeded, failed, and timed-out clusters.

## Deduplication

The selected cluster set is the union of all refs. Duplicate clusters are
reconciled once. Generated child labels record the first placement ref that
selected the cluster, while per-ref status summaries include overlapping refs.
