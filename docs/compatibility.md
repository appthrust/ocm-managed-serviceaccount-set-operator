# Compatibility

This project is alpha. Compatibility promises are intentionally narrow until the
API graduates.

| Component | Supported |
| --- | --- |
| Kubernetes | 1.29 or newer |
| OCM API | `open-cluster-management.io/api` v1.3.x compatible APIs |
| OCM ManagedServiceAccount | `authentication.open-cluster-management.io/v1beta1` |
| OCM ManifestWork | `work.open-cluster-management.io/v1` |
| OCM Placement | `cluster.open-cluster-management.io/v1beta1` |
| OCM PlacementDecision | `cluster.open-cluster-management.io/v1beta1` |
| Cluster Inventory ClusterProfile API | `multicluster.x-k8s.io/v1alpha1` when selector-targeted grants are used |
| Helm | 3.13 or newer |

The chart declares `kubeVersion: >=1.29.0-0`.

## Upgrade Policy

Alpha API fields may change before beta. Breaking API changes require a
changelog entry and migration notes. Once a beta API exists, supported upgrade
paths must be documented before release.

## Optional APIs

OCM placement support is required. Cluster Inventory `ClusterProfile` support
is optional and used only by the selector-targeted namespace grant access path.
