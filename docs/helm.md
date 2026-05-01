# Helm

The chart installs:

- `ManagedServiceAccountReplicaSet` CRD.
- Controller `Deployment`.
- Controller `ServiceAccount`.
- Least-privilege controller `ClusterRole` and `ClusterRoleBinding`.
- Optional metrics `Service`.
- Optional `ServiceMonitor`.

It does not render per-cluster OCM `ManagedServiceAccount` or `ManifestWork`
children. Those are created at runtime from
`ManagedServiceAccountReplicaSet` objects.

By default, the chart does not grant the controller access to Secrets. Enabling
`controller.clusterProfileProvider.enabled=true` mounts the ClusterProfile
provider file and grants `secrets get`, which is required for selector-targeted
namespace grants that read synced ManagedServiceAccount token Secrets through
the OCM ClusterProfile credentials plugin.

## Install

```sh
helm install msars charts/ocm-managed-serviceaccount-replicaset-controller \
  --namespace ocm-managed-serviceaccount-replicaset-controller-system \
  --create-namespace
```

## Upgrade

```sh
helm upgrade msars charts/ocm-managed-serviceaccount-replicaset-controller \
  --namespace ocm-managed-serviceaccount-replicaset-controller-system
```

Review [Compatibility](compatibility.md) and [CHANGELOG](../CHANGELOG.md)
before upgrading.

## Uninstall

Delete or migrate `ManagedServiceAccountReplicaSet` objects first so finalizers
can clean generated children:

```sh
kubectl get managedserviceaccountreplicasets.authentication.appthrust.io -A
helm uninstall msars -n ocm-managed-serviceaccount-replicaset-controller-system
```

CRDs installed from the chart may remain after uninstall depending on Helm CRD
behavior and release history. Remove CRDs only after all parent and generated
children are gone.

## Production Values

Recommended defaults are enabled: non-root container, read-only root filesystem,
resource requests, memory limit, leader election, and pinned image repository
fields. Enable metrics explicitly:

```yaml
metrics:
  service:
    enabled: true
serviceMonitor:
  enabled: true
```
