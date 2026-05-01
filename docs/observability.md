# Observability

The manager exposes controller-runtime metrics on `--metrics-bind-address`,
defaulting to `:8080`. The chart can create a metrics `Service` and optional
Prometheus Operator `ServiceMonitor`.

Health and readiness probes are exposed on `--health-probe-bind-address`,
defaulting to `:8081`.

## Chart Values

- `metrics.service.enabled`: create a metrics Service.
- `serviceMonitor.enabled`: create a ServiceMonitor.
- `serviceMonitor.scheme`/`bearerTokenFile`/`tlsConfig`: default to `https` with the in-cluster SA token and `insecureSkipVerify: true`, matching the controller-runtime self-signed metrics endpoint hardened in cycle 6 (TLS + Bearer required).
- Prometheus' ServiceAccount must be authorized for `nonResourceURLs: ["/metrics"]`, `verbs: ["get"]` via ClusterRole/Binding so TokenReview/SubjectAccessReview succeeds.
- `serviceMonitor.interval`: scrape interval.
- `controller.extraArgs`: pass log flags such as `--zap-log-level=debug`.

## Signals

Use Kubernetes object status for user-facing readiness:

- `Ready=True`: all selected clusters have current children.
- `PlacementResolved=False`: the referenced OCM Placement or its decision
  output cannot be consumed.
- `PlacementRolledOut=False`: rollout is still progressing across selected
  clusters.
- `status.rollout`: OCM-style rollout totals for updating, succeeded, failed,
  and timed-out clusters.
- `RemotePermissionsApplied=False`: generated RBAC work is not available.
- `CleanupBlocked=True`: deletion or stale cleanup is blocked.
- `status.summary.desiredTotal` and per-placement `summary.desiredTotal`
  reflect partial counts during partial aggregation (see "Partial RBAC
  summary aggregation" below); they may temporarily under-count slices for
  clusters whose namespace selectors have not yet resolved.
- `summary.desiredTotal` markedly smaller than the expected fleet size while
  `PlacementResolved=True` is a diagnostic signal that the resolver is
  degraded for a subset of clusters even though the placement itself is
  consumable.

## Partial RBAC summary aggregation

When a non-rolling cluster fails to resolve its `NamespaceSelectorResolver`,
the controller keeps the cluster-role grants and any namespace grants that
already resolved, and continues to count those resolved slices into the
cluster's `summary.desiredTotal` contribution. Per Kubernetes
api-conventions, Conditions complement detailed status fields such as
counts; keeping `summary.desiredTotal` as the full denominator preserves
that complement so counts do not silently diverge from `PlacementResolved`
/ `RemotePermissionsApplied` (`observedGeneration` describes the spec the
status was computed against).

- The controller emits an Info-level log:
  `partial RBAC summary aggregation for non-rolling cluster: namespace selector resolution failed; counting resolved slices in DesiredTotal`
  with structured keys `replicaset`, `cluster`, `err`.
  Note: this log is at Info level. Log pipelines that drop Info-and-below
  must whitelist the message text above, or rely on the future
  resolver-failures counter described under "Suggested alerts" below.
- A Warning Event is intentionally not emitted on this path. Per-cluster
  Warning emission would defeat Event dedup at fleet scale (per
  api-conventions: Events should accumulate to reduce noise).
  Actionable misconfigurations are still surfaced via the active-rollout
  path's `ApplyFailed` Warning Event, which is recorded once per
  reconcile rather than once per cluster.

### Triage steps

1. Inspect the `err` field of the log line for the failing namespace
   selector pattern.
2. Verify `ClusterProfile` resolution for the affected cluster:
   `kubectl get clusterprofile -l open-cluster-management.io/cluster-name=<cluster>`.
3. Re-run the controller with `--zap-log-level=debug` to inspect
   `NamespaceSelectorResolver` cache state.
4. See [troubleshooting.md](./troubleshooting.md) for related condition
   reasons such as `WaitingForRBAC` and `PlacementUnavailable`.

## Suggested alerts

- `CleanupBlocked=True` for more than 15 minutes.
- `Ready=False` for more than 30 minutes on production replica sets.
- Controller pod restarts or readiness probe failures.
- A sustained Loki/grep rate greater than zero for `partial RBAC summary
  aggregation for non-rolling cluster` while `PlacementResolved=True` for
  the same `ManagedServiceAccountReplicaSet`, observed for more than 30
  minutes (longer than typical informer resync windows; gating on
  `PlacementResolved` excludes transient placement-side failures already
  covered by the `PlacementResolved` alert). This alert is the
  resolver-specific precursor to the `Ready=False` alert above; it
  pinpoints the resolver root cause before the longer Ready-readiness
  window expires.
- Future replacement: a
  `clusterprofile_resolver_failures_total{namespace,replicaset}` counter.
  The `cluster` label is intentionally excluded because cardinality scales
  with fleet size.

## Logs

Use structured controller-runtime logs. Increase verbosity during debugging with
chart value:

```yaml
controller:
  extraArgs:
    - --zap-log-level=debug
```
