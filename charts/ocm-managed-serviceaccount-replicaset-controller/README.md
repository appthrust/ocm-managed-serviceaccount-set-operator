# OCM ManagedServiceAccount ReplicaSet Controller Chart

This chart installs the controller manager, CRD, service account, and
least-privilege controller RBAC.

It does not render per-cluster OCM `ManagedServiceAccount` or `ManifestWork`
objects. Those children are created by the controller from
`ManagedServiceAccountReplicaSet` resources at runtime.

See [Helm documentation](../../docs/helm.md) for install, upgrade, uninstall,
CRD, metrics, and production values guidance.
