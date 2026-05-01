# OCM ManagedServiceAccount Set Operator Chart

This chart installs the operator manager, service account, and RBAC.

It does not render per-cluster OCM `ManagedServiceAccount` or `ManifestWork`
objects. Those children are created by the controller from
`ManagedServiceAccountSet` resources at runtime.

