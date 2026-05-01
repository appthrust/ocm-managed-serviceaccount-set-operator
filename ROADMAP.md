# Roadmap

## Alpha

- Keep API surface small and typed.
- Preserve least-privilege controller RBAC.
- Document placement, security, cleanup, and troubleshooting behavior.
- Add focused tests for deletion ordering, placement resolution, and generated
  child ownership.

## Beta Criteria

- Compatibility matrix validated in CI or e2e tests.
- Upgrade and migration guidance for all released alpha fields.
- Admission policy examples for common tenant boundaries.
- Observability docs with actionable alerts.
- At least one production-style adopter report or case study.

## Stable Criteria

- No known API behavior ambiguity.
- Backward-compatible API evolution policy.
- Documented support window.
- Mature security response process.
- Maintainer coverage beyond a single approver.

## Non-goals

- Reading Cluster API `Cluster` objects.
- Reading remote kubeconfig Secrets in reconcilers.
- Accepting arbitrary `ManifestWork` payloads for remote permissions.
- Force-delete cleanup bypass without privileged admission.
