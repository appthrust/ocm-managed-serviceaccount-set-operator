# Operations

## Reconciliation

For every selected cluster, the controller:

1. Checks that the managed cluster namespace exists on the hub.
2. Creates or patches the generated OCM `ManagedServiceAccount`.
3. Creates or patches RBAC `ManifestWork` slices when `spec.rbac.grants` is
   enabled.
4. Deletes stale generated children that are no longer selected or desired.
5. Writes only `ManagedServiceAccountReplicaSet.status`.

The controller does not write `ManagedServiceAccount.status`,
`ManifestWork.status`, or `ClusterProfile.status`.

## Placement Changes

When placement output adds a cluster, the controller creates the required
children in that managed cluster namespace.

When placement output removes a cluster, stale cleanup is phased:

1. Delete stale generated RBAC `ManifestWork` objects.
2. Requeue while those works are being removed by OCM.
3. Delete stale generated `ManagedServiceAccount` objects only after the stale
   `ManifestWork` objects are gone.

This preserves remote permission cleanup before credential cleanup.

## RBAC Changes

Changing `spec.rbac.grants` patches the affected generated `ManifestWork`
slices and updates their spec-hash annotations. Removing a namespace from a
fixed namespace list deletes that namespace slice. Removing or emptying
`spec.rbac` deletes generated RBAC `ManifestWork` objects before any stale
`ManagedServiceAccount` cleanup.

Selector-targeted namespace grants depend on controller namespace access. While
that access is unavailable, the controller does not read remote kubeconfig
Secrets and does not use workload MSA credentials as a fallback.
`status.controllerAccess` reports the desired and ready cluster counts for that
internal bootstrap path separately from workload RBAC `status.summary`.

Enable selector resolution by passing
`--clusterprofile-provider-file=<path-to-access-provider-json>` to the manager.
The access-provider file should select the
`open-cluster-management` ClusterProfile provider and configure the Managed
ServiceAccount ClusterProfile credentials plugin. The controller injects the
deterministic controller-access MSA name and selected ClusterProfile namespace
into the exec provider per replica set.
With the Helm chart, use `controller.clusterProfileProvider.enabled=true` for
this path. That opt-in also grants `secrets get` to the controller
ServiceAccount so the exec plugin can read synced ManagedServiceAccount token
Secrets from selected ClusterProfile namespaces.
Selector output is read from a per replica set and cluster remote Namespace
watch cache. Namespace creation, deletion, and relabeling enqueue the parent for
reconciliation after the controller can establish the controller-access path.
The controller also keeps a periodic selector requeue as a reconnect backstop.

Platform admission policy should set any installation-specific limits for
selector breadth or fixed namespace-list length. The controller does not impose
a hard `forEachNamespace.names` item limit; Kubernetes object-size limits,
platform quotas, and OCM work scalability are the operational boundary.

## Deletion

The parent has a finalizer. Parent deletion is blocked until generated
`ManifestWork` objects are gone and then generated `ManagedServiceAccount`
objects are gone. If OCM cannot complete remote cleanup, deletion remains
blocked with `CleanupBlocked=True`.

There is no force-delete path. Any future force-delete mechanism must be guarded
by privileged validating admission before the controller honors it.

## Backup and Restore

Back up `ManagedServiceAccountReplicaSet` objects and OCM placement inputs.
Generated children can be reconstructed from the parent plus placement output.
Do not back up or restore remote kubeconfig Secrets as controller input.
