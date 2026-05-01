# OCM ManagedServiceAccount ReplicaSet Controller

OCM ManagedServiceAccount ReplicaSet Controller is a hub-side controller for
creating OCM `ManagedServiceAccount` resources across clusters selected by OCM
Placement output.

The upstream `ManagedServiceAccount` API is per managed cluster namespace. It
does not support creating one `ManagedServiceAccount` in a cluster-set namespace
and having it apply to every cluster in the set. This controller fills that gap
with `ManagedServiceAccountReplicaSet`.

## What It Does

`ManagedServiceAccountReplicaSet` provides an OCM-native workflow for
replicating managed service accounts across a selected fleet:

- Resolves target clusters from OCM `Placement` and `PlacementDecision`
  output using the OCM placement SDK tracker.
- Supports OCM-style `rolloutStrategy` per placement ref.
- Reports OCM-style rollout progress counts for updating, succeeded, failed,
  and timed-out clusters.
- Creates one generated OCM `ManagedServiceAccount` per selected managed
  cluster namespace.
- Optionally delivers typed, least-privilege remote RBAC through generated OCM
  `ManifestWork` objects.
- Cleans generated cross-namespace children with explicit finalizers.

## Get Started

Install the controller on an OCM hub, point a
`ManagedServiceAccountReplicaSet` at an existing OCM `Placement`, and let the
controller reconcile generated hub-side children for each selected managed
cluster. The included sample shows a complete replica set with service account
rotation and typed remote RBAC grants:

- [Getting started](docs/getting-started.md) for the end-to-end flow.
- [Helm installation](docs/helm.md) for chart options and production values.
- [Sample ManagedServiceAccountReplicaSet](config/samples/authentication_v1alpha1_managedserviceaccountreplicaset.yaml)

## Additional Documentation

- [API reference](docs/api-reference.md)
- [Placement behavior](docs/placement.md)
- [Operations](docs/operations.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Security model](docs/security.md)
- [Admission policy](docs/admission-policy.md)
- [Supply chain](docs/supply-chain.md)
- [Observability](docs/observability.md)
- [Compatibility](docs/compatibility.md)

## Controller Guarantees

The controller intentionally avoids direct remote-cluster access. It does not
read Cluster API `Cluster` objects, remote kubeconfig Secrets, or child status
subresources, and it only writes the status of `ManagedServiceAccountReplicaSet`.

## Checks

```sh
make test
make lint
make helm-lint
make test-chart
make test-e2e
```
