# Getting Started

This controller runs on an OCM hub cluster and creates one hub-side OCM
`ManagedServiceAccount` per selected managed cluster. Optional typed RBAC is
delivered to the managed cluster through one generated OCM `ManifestWork` per
cluster.

## Prerequisites

- Kubernetes 1.29 or newer on the hub.
- OCM hub APIs for `Placement`, `PlacementDecision`, `ManifestWork`, and
  `ManagedServiceAccount`.
- OCM managed cluster namespaces with the same names as the selected clusters.
- Optional: Cluster Inventory `ClusterProfile` API when using
  selector-targeted namespace grants.
- Helm 3.13 or newer for chart installation.

## Install

```sh
helm install msars \
  oci://ghcr.io/appthrust/helm-charts/ocm-managed-serviceaccount-replicaset-controller \
  --namespace ocm-managed-serviceaccount-replicaset-controller-system \
  --create-namespace
```

For local development from this repository:

```sh
helm install msars charts/ocm-managed-serviceaccount-replicaset-controller \
  --namespace ocm-managed-serviceaccount-replicaset-controller-system \
  --create-namespace
```

## Create Placement Output

Create an OCM `Placement` through your normal OCM workflow. The controller does
read the `Placement` object and the OCM `PlacementDecision` objects generated
by OCM in the same namespace. It uses the same placement SDK tracker as OCM
rollout controllers, including decision group labels.

Check that OCM has produced decisions:

```sh
kubectl -n appthrust-system get placement.cluster.open-cluster-management.io prod-clusters
kubectl -n appthrust-system get placementdecisions.cluster.open-cluster-management.io \
  -l cluster.open-cluster-management.io/placement=prod-clusters
```

## Create a ReplicaSet

Apply a `ManagedServiceAccountReplicaSet`:

```sh
kubectl apply -f config/samples/authentication_v1alpha1_managedserviceaccountreplicaset.yaml
```

Verify the parent status:

```sh
kubectl -n appthrust-system get managedserviceaccountreplicasets.authentication.appthrust.io
kubectl -n appthrust-system describe managedserviceaccountreplicaset irsa-webhook
```

Verify generated children on the hub:

```sh
kubectl -n <managed-cluster-name> get managedserviceaccounts.authentication.open-cluster-management.io
kubectl -n <managed-cluster-name> get manifestworks.work.open-cluster-management.io
```

The generated `ManagedServiceAccount` is ready when OCM reports either
`TokenReported=True` or `SecretCreated=True`. If remote RBAC is configured, the
generated `ManifestWork` must also report `Available=True`.
