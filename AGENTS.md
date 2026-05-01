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

## Deletion Guardrails

- Delete generated remote-permission `ManifestWork` objects before deleting
  generated `ManagedServiceAccount` objects.
- If OCM cannot complete remote cleanup, keep deletion blocked. A future
  force-delete path requires privileged validating admission before the
  controller is allowed to honor any annotation or field.
- Generated hub-side children must carry source labels and annotations so stale
  children can be found without reading remote clusters.
