# Security Model

This controller centralizes managed service account fan-out on the hub. The hub
cluster is trusted to run OCM, this controller, and any admission policy that
controls who may create `ManagedServiceAccountReplicaSet` objects.

## Boundaries

The controller:

- Reads local `ManagedServiceAccountReplicaSet` objects.
- Reads OCM `Placement` and `PlacementDecision` output.
- Optionally reads Cluster Inventory `ClusterProfile` objects for
  selector-targeted namespace grants.
- Writes generated OCM `ManagedServiceAccount` and RBAC `ManifestWork`
  children.
- May read remote `Namespace` labels only through a controller-owned
  ManagedServiceAccount access path surfaced by Cluster Inventory access
  providers when selector-targeted grants are enabled.
- When that access path is enabled, the bundled ClusterProfile credentials exec
  plugin reads synced ManagedServiceAccount token Secrets from the selected
  ClusterProfile namespace. Reconcilers still do not read remote kubeconfig
  Secrets directly.
- The Helm chart grants `secrets get` to the controller ServiceAccount only
  when `controller.clusterProfileProvider.enabled=true`; the default install
  does not grant Secret access.
- Writes only `ManagedServiceAccountReplicaSet.status`.

The controller does not:

- Read Cluster API `Cluster` objects.
- Read remote kubeconfig Secrets.
- Use workload managed service account credentials for controller internals.
- Accept arbitrary user-supplied `ManifestWork` payloads.
- Write child status subresources.
- Force-delete children when OCM cleanup is incomplete.

## Remote RBAC

Users provide Kubernetes RBAC `PolicyRule` values, but the controller controls
the rendered object kinds and subjects. Subjects always point to the generated
managed service account. This prevents users from smuggling arbitrary remote
workloads through the API, but it does not by itself prevent privilege
escalation through powerful RBAC rules.

Grant metadata cannot use controller-reserved source, slice, or OCM sync keys.
Selector-targeted namespace grants should be constrained by installation
admission policy; the controller evaluates valid Kubernetes label selectors
without embedding tenant policy.

Admission policy should restrict:

- Wildcard verbs or resources.
- `bind`, `escalate`, and `impersonate`.
- Writes to Secrets, RBAC resources, webhooks, CSRs, and other sensitive APIs.
- Who may set `authentication.open-cluster-management.io/sync-to-clusterprofile`.
- Which namespaces may create replica sets for which placement outputs.

See [Threat Model](threat-model.md) for abuse cases and [Remote RBAC](rbac.md)
for rendering details. See [Admission Policy](admission-policy.md) for example
policy shape.
