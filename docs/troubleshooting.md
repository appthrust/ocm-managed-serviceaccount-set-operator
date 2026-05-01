# Troubleshooting

Start with:

```sh
kubectl -n <namespace> describe managedserviceaccountreplicaset <name>
kubectl -n <namespace> get managedserviceaccountreplicaset <name> -o yaml
```

## Conditions

`PlacementResolved=False` with `PlacementUnavailable` means a placement ref
could not be resolved. Check the referenced OCM `Placement` and matching
`PlacementDecision` objects.

`PlacementEmpty=True` means the ref resolved successfully but selected zero
clusters.

`PlacementRolledOut=False` with `Progressing` means the rollout strategy has
not completed across the selected clusters. Check `status.rollout` for updating,
failed, and timed-out counts.

`ManagedServiceAccountsReady=False` with `WaitingForCredentials` means one or
more generated OCM `ManagedServiceAccount` children have not reported
`TokenReported=True` or `SecretCreated=True`.

`RemotePermissionsApplied=False` with `WaitingForRBAC` means one or more
generated RBAC `ManifestWork` children are not `Available=True`.

`Ready=False` with `ChildConflict` means an object with the expected generated
name already exists and does not carry the source labels for this replica set.

`CleanupBlocked=True` means deletion or stale cleanup is waiting for OCM to
remove generated children.

## Useful Commands

List OCM placement decisions:

```sh
kubectl -n <namespace> get placement.cluster.open-cluster-management.io <placement-name>
kubectl -n <namespace> get placementdecisions.cluster.open-cluster-management.io \
  -l cluster.open-cluster-management.io/placement=<placement-name>
```

List generated children:

```sh
kubectl get managedserviceaccounts.authentication.open-cluster-management.io -A \
  -l authentication.appthrust.io/set-name=<replicaset-name>

kubectl get manifestworks.work.open-cluster-management.io -A \
  -l authentication.appthrust.io/set-name=<replicaset-name>
```

Inspect RBAC work status:

```sh
kubectl -n <managed-cluster-name> get manifestwork \
  -l authentication.appthrust.io/set-name=<replicaset-name>,authentication.appthrust.io/slice-type
```

Recover from `ChildConflict` by either deleting or renaming the conflicting
object, or by changing the replica set template name so the generated child name
does not collide.
