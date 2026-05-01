# Agent Instructions

## Scope

This repository designs and will implement a standalone OCM controller that
fans out OCM `ManagedServiceAccount` resources to managed clusters selected by
OCM `Placement`.

## Guardrails

- Do not read Cluster API `Cluster` objects.
- Do not read remote kubeconfig Secrets directly in reconcilers.
- Do not write `ClusterProfile.status`.
- Do not write `ManagedServiceAccount.status` or `ManifestWork.status`.
- Resolve cluster selection through OCM `Placement` and `PlacementDecision`
  objects rather than reimplementing cluster set selection locally.
- Treat OCM `ManagedServiceAccount` CRs as the source of truth for managed
  service account credentials.
- Keep remote workload RBAC delivery typed and least-privilege. Do not accept
  arbitrary `ManifestWork` payloads for remote permissions.
- The owner controller writes only the status of its local API object.
- Do not gate Watches in `SetupWithManager` with `predicate.GenerationChangedPredicate`.
  The reconciler is status-driven: child `ManagedServiceAccount.status.tokenSecretRef`
  and `ManifestWork.status.conditions` updates, and parent transient placement-status
  conditions, all leave `metadata.generation` unchanged and would be silently dropped.
  Enforced by `TestPrimaryWatchOmitsGenerationChangedPredicate` and
  `TestChildWatchesOmitGenerationChangedPredicate`.
- The manager binary defaults `--leader-elect=false` (`cmd/manager/main.go`) so single-process
  local runs match kubebuilder scaffold ergonomics. The Helm chart overrides this for production
  via `controller.leaderElection.enabled: true` (`charts/ocm-managed-serviceaccount-replicaset-controller/values.yaml`)
  and conditionally appends `--leader-elect` in `templates/deployment.yaml`. Keep this dual default;
  flipping the binary default would break local single-replica testing. The `LeaderElectionID`
  constant at `cmd/manager/main.go:61` is also load-bearing across versions: changing it would
  let old- and new-version replicas hold disjoint leases concurrently during rolling upgrades,
  so treat it as a stable hardcoded identifier and do not rename it in refactors.
- The leader-election Lease (`coordination.k8s.io/leases`) requires `[get, create, update]` and is the
  only RBAC where `update` is permitted in this controller. Keep this grant in the **namespaced**
  `config/rbac/leader_election_role.yaml` and `charts/.../templates/rbac-leader-election.yaml`,
  separate from the cluster-wide ClusterRole, so least-privilege scope (one namespace) and the
  `Makefile` `verify-static` `- update` ban (which excludes these files via
  `--glob '!leader_election*'` / `--glob '!rbac-leader-election*'`) are enforced together.
  Without this grant the chart default `controller.leaderElection.enabled: true` makes the
  controller crashloop on Lease creation 403.

## Deletion Guardrails

- Delete generated remote-permission `ManifestWork` objects before deleting
  generated `ManagedServiceAccount` objects.
- If OCM cannot complete remote cleanup, keep deletion blocked. A future
  force-delete path requires privileged validating admission before the
  controller is allowed to honor any annotation or field.
- Generated hub-side children must carry source labels and annotations so stale
  children can be found without reading remote clusters.
