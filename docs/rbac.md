# Remote RBAC

`ManagedServiceAccountReplicaSet` renders remote permissions from typed
`spec.rbac.grants` entries.

Each grant has an `id`, `type`, generated RBAC `metadata`, and
`rbac.authorization.k8s.io/v1` policy rules. `type: Role` grants expand through
`forEachNamespace.name`, `forEachNamespace.names`, or
`forEachNamespace.selector`. `type: ClusterRole` grants are cluster-scoped and
omit namespace fan-out. Users cannot provide subjects, roleRefs, raw templates,
or arbitrary `ManifestWork` payloads; the controller fixes subjects to the
generated remote ServiceAccount from `spec.template.metadata`.

The controller slices generated RBAC by purpose:

- `<replicaset-name>-rbac-cluster` contains generated `ClusterRole` and
  `ClusterRoleBinding` objects.
- `<replicaset-name>-rbac-ns-<namespace-hash>` contains generated `Role` and
  `RoleBinding` objects for exactly one target namespace.

Selector-targeted grants require controller namespace access through the
controller-owned ManagedServiceAccount and Cluster Inventory access-provider
path. Configure the manager with `--clusterprofile-provider-file=<path>` so
the controller can build a read-only remote Namespace watch cache from
ClusterProfile `accessProviders`. The controller injects the per-set
controller-access MSA name and selected ClusterProfile namespace into the exec
provider at reconcile time. Without that flag, selector grants report as
waiting for controller namespace access and the controller does not use
workload MSA credentials as a fallback.

For Helm installs, set `controller.clusterProfileProvider.enabled=true` to
mount the provider file and grant the controller `secrets get` so the
ClusterProfile credentials exec plugin can read synced ManagedServiceAccount
token Secrets. The default chart rendering does not grant Secret access.

When selector grants are present, the controller creates a separate
`<replicaset-name>-controller-access` ManagedServiceAccount and a
`<replicaset-name>-access-rbac` ManifestWork. That bootstrap work grants only
`namespaces get/list/watch` and is separate from workload RBAC.

Wildcard, escalation, and sensitive-resource policy checks are intentionally
left to the platform admission layer.

Generated RBAC `ManifestWork` objects use foreground deletion. During parent
deletion and stale cleanup, the controller deletes generated RBAC works before
deleting generated `ManagedServiceAccount` children.
