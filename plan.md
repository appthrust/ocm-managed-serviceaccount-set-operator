# OCM ManagedServiceAccount Set Operator Plan

## Goal

Provide a standalone appthrust operator and API for declaring one managed
service account intent and having it materialized for every OCM managed cluster
selected from a cluster set.

The direct upstream shape is not usable for this job. A
`ManagedServiceAccount` must be created in each managed cluster namespace; the
API does not support creating one object in a cluster-set namespace and having
the add-on expand it. The upstream issue comment
`1777005621` on open-cluster-management-io/managed-serviceaccount#90 says the
API does not support cluster-set namespaces, likely will not support them, and
would need another high-level API for service accounts that access multiple
managed clusters.

This repository owns that high-level API. It does not replace OCM
`ManagedServiceAccount`, OCM `Placement`, OCM `ManifestWork`, or OCM
`ClusterProfile`; it composes them.

## Non-Goals

- Do not change upstream OCM `ManagedServiceAccount` semantics.
- Do not create or patch CAPI `Cluster` objects.
- Do not create `ClusterProfile` objects.
- Do not write `ClusterProfile.status`.
- Do not read remote kubeconfig Secrets from reconcilers.
- Do not expose arbitrary `ManifestWork` templates as a generic remote code
  execution surface.
- Do not create tenant-facing credential Secrets.

## Hard Constraints

- OCM `ManagedServiceAccount` is namespace-scoped and the namespace is the
  managed cluster name. The managed-serviceaccount ClusterProfile credential
  syncer maps `ManagedServiceAccount.namespace` to `ClusterProfile.name`.
  Therefore a cluster-set-wide intent must fan out one child
  `ManagedServiceAccount` per selected managed cluster namespace.
  References:
  - `refs/open-cluster-management-io/managed-serviceaccount/apis/authentication/v1beta1/managedserviceaccount_types.go`
  - `refs/open-cluster-management-io/managed-serviceaccount/pkg/addon/manager/controller/clusterprofile_cred_syncer.go`
- OCM `ManagedServiceAccount` status is owned by the managed-serviceaccount
  add-on. This operator may read it for aggregation, but must not write it.
- OCM `ClusterProfile.status.accessProviders` is owned by OCM access provider
  controllers. This operator must not write it.
- OCM `Placement` is the native API for selecting clusters from
  `ManagedClusterSet` bindings. It respects namespace bindings,
  `spec.clusterSets`, label/claim predicates, taints, and scheduler behavior.
  This operator should consume `PlacementDecision` output rather than
  reimplementing cluster-set selection from labels.
  Reference:
  - `refs/open-cluster-management-io/ocm/vendor/open-cluster-management.io/api/cluster/v1beta1/types_placement.go`
- OCM `ManagedClusterSet` can select clusters by the exclusive cluster-set
  label or by `spec.clusterSelector.labelSelector`. Directly listing
  `ManagedCluster` objects with
  `cluster.open-cluster-management.io/clusterset=<set>` is incomplete.
  Reference:
  - `refs/open-cluster-management-io/ocm/vendor/open-cluster-management.io/api/cluster/v1beta2/types_managedclusterset.go`
- A namespaced owner cannot own dependents in another namespace through
  Kubernetes garbage collection. The API is intentionally namespaced to align
  with OCM `Placement`, so generated children in managed cluster namespaces
  require explicit finalizer cleanup.
  Reference:
  - https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/
- Generated remote permissions are hub-side `ManifestWork` objects. The
  operator must not connect to remote clusters directly to create RBAC.
- The owner controller writes only `ManagedServiceAccountSet.status`. Remote
  controllers and OCM add-ons write their own object status.
- Credential publication to `ClusterProfile` is controlled by the upstream
  label `authentication.open-cluster-management.io/sync-to-clusterprofile=true`.
  When enabled, upstream sync copies the token Secret into every matching
  OCM-managed `ClusterProfile` namespace whose `ClusterProfile.name` matches the
  managed cluster name. This operator cannot narrow that sync per
  `ClusterProfile` namespace.

## Current Gaps / Migration Work

- `aws-workload-identity-operator` can render static chart-created
  `ManagedServiceAccount` and `ManifestWork` objects only for an explicit list
  of managed cluster namespaces. It cannot track a cluster set as clusters are
  added or removed.
- The OCM CAPI / ClusterProfile migration plan moves credential consumption
  toward OCM-generated `ClusterProfile` objects and Cluster Inventory access
  providers. That makes dynamic `ManagedServiceAccount` fan-out a prerequisite
  for a usable appthrust multicluster install.
- Existing appthrust consumers should stop carrying their own broad static MSA
  creation lists. They should either reference this API or own a narrow
  `ManagedServiceAccountSet` for their controller-specific identity.
- The initial repository is a design-phase scaffold. Implementation must add
  API types, controller-runtime reconcilers, CRD generation, admission
  webhooks, generated chart CRDs, and focused tests.
- There is no supported migration path that "moves" a clusterset-namespace
  `ManagedServiceAccount` into this API because that source shape never worked
  as an upstream contract. Migration is from explicit per-cluster children or
  chart value lists into a `ManagedServiceAccountSet`.

## Target API

API group: `authentication.appthrust.io/v1alpha1`

Kind: `ManagedServiceAccountSet`

Scope: namespaced.

The object lives in the OCM workload namespace where the relevant
`ManagedClusterSetBinding` exists. This follows OCM `Placement` authorization:
users who can create the API object in a namespace can target only cluster sets
bound into that namespace.

Example:

```yaml
apiVersion: authentication.appthrust.io/v1alpha1
kind: ManagedServiceAccountSet
metadata:
  name: aws-workload-identity-operator
  namespace: appthrust-system
spec:
  placement:
    clusterSets:
      - workload
    selector:
      matchLabels:
        role.appthrust.io/workload-cluster: "true"
  managedServiceAccount:
    name: aws-workload-identity-operator
    credentialPublication: ClusterProfile
    rotation:
      enabled: true
      validity: 8640h
  remotePermissions:
    enabled: true
    profileRefs:
      - name: aws-workload-identity-selfhosted-irsa
    remoteServiceAccountNamespace: open-cluster-management-managed-serviceaccount
```

### Spec

`spec.placement`:

- Required unless `spec.placementRef` is set.
- `clusterSets` is required and non-empty in v1alpha1. The API does not default
  to all bound cluster sets because that would make credential fan-out too easy
  to broaden accidentally.
- Contains the v1alpha1 subset of OCM `PlacementSpec` needed for cluster-set
  fan-out: `clusterSets` plus a `LabelSelector`. Future versions may add OCM
  claim predicates, tolerations, and decision strategy after the controller
  implementation proves the smaller API.
- The controller creates a same-namespace owned OCM `Placement` named from the
  `ManagedServiceAccountSet`.
- The generated `Placement` uses `sortBy: ClusterName` unless the user sets a
  stronger OCM placement policy.

`spec.placementRef`:

- Optional mutually exclusive reference to an existing same-namespace OCM
  `Placement`.
- Used when another controller already owns placement policy.
- The operator must not patch a referenced placement.

`spec.managedServiceAccount`:

- `name`: required DNS label. Immutable because it is the remote identity and
  part of generated Secret names after ClusterProfile sync.
- `credentialPublication`: required enum:
  - `None`: create per-cluster `ManagedServiceAccount` only.
  - `ClusterProfile`: set
    `authentication.open-cluster-management.io/sync-to-clusterprofile=true` on
    generated `ManagedServiceAccount` children.
- `rotation`: mirrors upstream `ManagedServiceAccount.spec.rotation`.
- `ttlSecondsAfterCreation`: optional, passed through only when explicitly set.
- `labels` and `annotations`: optional metadata copied to generated
  `ManagedServiceAccount` children after reserved keys are removed.

Reserved child metadata keys:

- `app.kubernetes.io/managed-by`
- `app.kubernetes.io/part-of`
- `authentication.appthrust.io/set-name`
- `authentication.appthrust.io/set-namespace`
- `authentication.appthrust.io/set-uid`
- `authentication.open-cluster-management.io/sync-to-clusterprofile`

`spec.remotePermissions`:

- Optional profile-based RBAC delivery.
- `enabled`: defaults to false.
- `profileRefs`: required when enabled. Each entry names an allowlisted
  permission profile installed by the operator chart or a future
  operator-config API.
- `remoteServiceAccountNamespace`: defaults to
  `open-cluster-management-managed-serviceaccount`.
- The controller expands each profile to fixed, reviewed RBAC manifests. A
  profile defines the generated `ManifestWork` name suffix, namespaces, roles,
  bindings, and rules.
- User input cannot set `PolicyRule`, `roleRef`, `subjects`, arbitrary
  manifests, or arbitrary `ManifestWork` fields. The subject is always the
  remote service account created by upstream managed-serviceaccount.

Validation:

- `spec.placement` and `spec.placementRef` are mutually exclusive.
- `spec.placement.clusterSets` must be non-empty for inline placement.
- `spec.placementRef` must be a same-namespace reference. Cross-namespace
  placement references are rejected.
- `spec.managedServiceAccount.name` is immutable.
- `spec.managedServiceAccount.credentialPublication` is immutable for v1alpha1.
- `spec.remotePermissions.profileRefs` and
  `spec.remotePermissions.remoteServiceAccountNamespace` are immutable once
  remote permissions are enabled.
- Unknown permission profiles are rejected.
- Permission profiles are the only v1alpha1 remote RBAC extension point. Raw
  `PolicyRule` lists are intentionally not part of the public API.
- Profile validation rejects wildcard verbs, wildcard resources,
  `cluster-admin`, `bind`, `escalate`, `impersonate`, ServiceAccount token
  subresources, RBAC resources, CSR resources, admission webhook resources, and
  `secrets` unless a specific profile has a security-reviewed exception
  documented in the repo.
- Reserved labels and annotations are rejected or overwritten deterministically.
- `credentialPublication: ClusterProfile` requires an explicit user choice; it
  must not be silently implied by labels.
- Force-delete is not part of the v1alpha1 API scaffold. The controller
  implementation must ignore any force-delete annotation until privileged
  validating admission exists. This keeps deletion fail-closed during the design
  and initial controller phases.

## Generated Children

For each selected managed cluster `<cluster>`:

```yaml
apiVersion: authentication.open-cluster-management.io/v1beta1
kind: ManagedServiceAccount
metadata:
  name: <spec.managedServiceAccount.name>
  namespace: <cluster>
  labels:
    app.kubernetes.io/managed-by: ocm-managed-serviceaccount-set-operator
    app.kubernetes.io/part-of: <set name>
    authentication.appthrust.io/set-name: <set name>
    authentication.appthrust.io/set-namespace: <set namespace>
    authentication.appthrust.io/set-uid: <set uid>
    authentication.open-cluster-management.io/sync-to-clusterprofile: "true"
spec:
  rotation:
    enabled: true
    validity: 8640h
```

When remote permissions are enabled, the controller also creates:

```yaml
apiVersion: work.open-cluster-management.io/v1
kind: ManifestWork
metadata:
  name: <set name>-<profile name>
  namespace: <cluster>
  labels:
    app.kubernetes.io/managed-by: ocm-managed-serviceaccount-set-operator
    authentication.appthrust.io/set-name: <set name>
    authentication.appthrust.io/set-namespace: <set namespace>
    authentication.appthrust.io/set-uid: <set uid>
spec:
  workload:
    manifests:
      - <fixed RBAC rendered from reviewed permission profile>
```

The chart never renders these per-cluster children statically. Fan-out is a
runtime controller responsibility.

## Reconciliation

Watch:

- `ManagedServiceAccountSet`
- generated OCM `Placement`
- same-namespace `PlacementDecision` objects labeled with the placement name
- generated `ManagedServiceAccount` children
- generated `ManifestWork` children

Flow:

1. Add finalizer `authentication.appthrust.io/managedserviceaccountset-cleanup`.
2. If inline placement is used, server-side apply the owned OCM `Placement` in
   the same namespace.
3. List same-namespace `PlacementDecision` objects for the resolved placement.
   A decision is trusted only when all of these are true:
   - it has OCM's placement-name label for the resolved placement name
   - it has an owner reference whose UID matches the resolved `Placement.UID`
   - the owner reference kind/apiVersion match OCM `Placement`
   - the decision namespace equals the `ManagedServiceAccountSet` namespace
4. Ignore and warn on any mismatched or ownerless decision. Never use an
   unowned decision as fan-out input.
5. Build desired cluster names from trusted `status.decisions[].clusterName`.
6. For each desired cluster:
   - verify the managed cluster namespace exists; do not create it
   - create or patch the generated `ManagedServiceAccount`
   - create or patch the generated remote-permission `ManifestWork` when enabled
7. List generated children by source labels to detect stale clusters.
8. For stale clusters, issue deletes for generated `ManifestWork` first, then
   immediately issue deletes for generated `ManagedServiceAccount`. Do not keep
   credentials live while waiting for remote RBAC cleanup to finish.
9. Update only `ManagedServiceAccountSet.status`.

Conflict policy:

- If a target child exists without this set's source labels, do not adopt or
  overwrite it.
- If a target child exists with this set name/namespace but a different UID, do
  not adopt by default. Surface `ChildConflict`.
- A future explicit adoption annotation may be added after implementation
  proves safe migration semantics.
- The controller uses server-side apply for fields it owns, but it does not use
  force conflicts by default. Conflict is a condition, not an overwrite.

## Deletion

Deletion uses the same finalizer because generated children live in managed
cluster namespaces and cannot be represented by same-namespace owner references.

Order:

1. Freeze cleanup scope by listing generated children with
   `authentication.appthrust.io/set-uid=<uid>`. Do not depend on the current
   `PlacementDecision`, because placement output may be stale, empty, or already
   deleted.
2. Stop creating new children.
3. Issue deletes for generated remote-permission `ManifestWork` children. This
   starts remote RBAC cleanup first.
4. Immediately issue deletes for generated `ManagedServiceAccount` children;
   do not wait for `ManifestWork` absence before revoking the credential source.
   Deleting the child MSA is
   the hub-side source-of-truth action that causes upstream
   managed-serviceaccount ClusterProfile credential sync to remove orphaned
   synced credentials.
5. Wait for both generated `ManifestWork` and `ManagedServiceAccount` objects
   to be absent.
6. If `ManifestWork` deletion remains stuck because remote cleanup cannot
   complete, keep deletion blocked unless privileged force-delete is set. At
   this point the generated MSA delete has still been issued so credentials are
   no longer intentionally kept live by this controller.
7. Delete generated inline `Placement`, or rely on same-namespace owner
   reference garbage collection.
8. Remove the finalizer.

If a `ManifestWork` remains stuck because remote cleanup cannot complete, the
operator keeps deletion blocked and reports `CleanupBlocked`.

Future force delete:

- No force-delete behavior is implemented in v1alpha1.
- A future annotation such as
  `authentication.appthrust.io/force-delete: "true"` may be added only together
  with privileged validating admission, tests, and a threat-model update.
- Normal writers of `ManagedServiceAccountSet.spec` must not automatically
  receive force-delete power.
- When implemented, force delete may remove the finalizer only after issuing
  deletes for generated children and recording a warning condition/event.
- Force delete deliberately accepts that remote RBAC may remain on unreachable
  clusters, and must not delete or alter children owned by another set.

## Status

`ManagedServiceAccountSet.status` is intentionally summary-oriented so it does
not grow without bound on large fleets.

Fields:

- `observedGeneration`
- `placementRef`
- `selectedClusterCount`
- `desiredClusterCount`
- `appliedClusterCount`
- `readyClusterCount`
- `staleClusterCount`
- `conflictCount`
- `failureCount`
- `failedClusters`: bounded list of the first 50 cluster names and reasons
- `clusters`: optional bounded list of the first 100 per-cluster summaries for
  debugging. Each entry contains cluster name, child object refs, phase, and
  reason only. It never contains token Secret names, token data, kubeconfig
  data, or credential material.
- `conditions`:
  - `PlacementResolved`
  - `ManagedServiceAccountsReady`
  - `RemotePermissionsApplied`
  - `CleanupBlocked`
  - `Ready`

The operator does not copy token data, Secret names, or credential material into
status.

## Security Model

- Creating a `ManagedServiceAccountSet` grants the ability to request a remote
  service account across clusters selected by the namespace's OCM placement
  authority. RBAC to create this API must be restricted to trusted platform
  operators or controller installers.
- The API never accepts arbitrary `ManifestWork` payloads. It accepts typed RBAC
  permission profile references and renders fixed Role/Binding shapes with a
  fixed subject.
- The API rejects raw RBAC rules, wildcard verbs/resources, and sensitive
  resources in v1alpha1 unless a named permission profile documents and tests a
  narrowly reviewed exception.
- The operator does not need Secret read permissions. It reads
  `ManagedServiceAccount` objects and status only.
- `credentialPublication: ClusterProfile` is explicit because upstream OCM
  syncs credentials to every matching OCM-managed `ClusterProfile` namespace.
- Consumers should use Cluster Inventory access providers and
  `ClusterProfile.status.accessProviders`; they should not read generated token
  Secrets directly from reconcilers.
- Generated remote RBAC must be least-privilege and controller-specific. A
  shared broad `cluster-admin` identity is not allowed.
- The chart's manager RBAC must not include Secret read permissions. If a future
  feature appears to need Secret access, it requires a separate design review
  and a new threat model.
- The chart does not expose generic `rbac.extraRules`. Additional manager RBAC
  must be added through reviewed chart changes so Secret reads, RBAC
  escalation, and foreign status writes cannot slip through values.

## Repository, Release, and Test Structure

Initial scaffold mirrors the release/test shape of
`aws-workload-identity-operator`:

- `AGENTS.md`: repository guardrails
- `README.md`, `CONTRIBUTING.md`, `CHANGELOG.md`, `LICENSE`: project metadata
- `cmd/manager/`: manager entrypoint
- `api/`: `ManagedServiceAccountSet` Go API types and generated deepcopy code
- `internal/controller/`: future reconcilers and admission logic
- `config/crd/`: generated CRDs
- `config/rbac/`: generated RBAC
- `config/default/`, `config/manager/`, `config/samples/`, `config/webhook/`:
  kustomize install, sample, and future webhook manifests
- `charts/ocm-managed-serviceaccount-set-operator/`: Helm chart
- `test/kest/`: Bun/kest chart and API-shape tests
- `tools/go.mod`: future pinned Go tools for controller-gen and golangci-lint
- `.github/workflows/verify-operator-source.yaml`: Go tests, vet, static
  guardrails, generated output check
- `.github/workflows/verify-rendered-chart-manifests.yaml`: Helm lint,
  template, Bun/kest tests, chart packaging
- `.github/workflows/verify-operator-image-build.yaml`: multi-arch Docker
  build
- `.github/workflows/publish-release-artifacts.yaml`: tagpr, GHCR image and
  OCI Helm chart publish, cosign signing
- `renovate.json`: digest and dependency maintenance

Current enforcement status:

- CRD CEL enforces placement/reference mutual exclusion, managed service account
  identity immutability, remote permission profile immutability after enablement,
  and known profile names.
- Force-delete has no v1alpha1 API surface and must be ignored by the future
  controller until privileged validating admission is implemented.
- Chart schema rejects invalid pull policies, invalid namespace overrides, and
  generic RBAC extension keys.
- `make verify-static` rejects CAPI imports, direct remote kubeconfig helpers,
  foreign status update patterns, chart-rendered per-cluster MSA/ManifestWork,
  Secret read RBAC, and wildcard chart RBAC.

## Release Design

- Release source of truth is `charts/ocm-managed-serviceaccount-set-operator/Chart.yaml`.
- `tagpr` creates draft releases from `main`.
- Image tags:
  - exact SemVer without `v`
  - major.minor when applicable
  - `latest`
- Image repository:
  - `ghcr.io/appthrust/ocm-managed-serviceaccount-set-operator`
- Chart repository:
  - `oci://ghcr.io/appthrust/helm-charts/ocm-managed-serviceaccount-set-operator`
- Image and chart digests are signed with keyless cosign in release workflows.

## Test Plan

Unit tests:

- placement mode validation and mutual exclusion
- placement decision trust validation by placement UID owner reference
- generated child metadata and reserved label handling
- `credentialPublication` behavior
- permission profile lookup and sensitive-rule rejection
- stale child detection by source labels
- conflict handling for preexisting children
- deletion order: `ManifestWork` before `ManagedServiceAccount`
- deletion does not wait for `ManifestWork` absence before issuing
  `ManagedServiceAccount` delete
- force-delete remains unsupported until privileged admission is implemented
- status summary bounding

Controller tests:

- inline placement creates an owned `Placement`
- placement decision changes add and remove generated children
- fake same-namespace placement decisions without the matching placement owner
  reference are ignored and reported
- missing managed cluster namespace reports a failure and does not create the
  namespace
- existing foreign `ManagedServiceAccount` blocks reconciliation
- existing same-name `ManifestWork` owned by another set blocks reconciliation
- generated `ManifestWork` subject is fixed to the remote managed service
  account
- unknown permission profiles and unsafe profile definitions are rejected by
  admission

Chart and static tests:

- Helm lint default and test values
- Helm template default and test values
- Bun/kest chart tests assert that the chart installs only the operator and
  does not statically render per-cluster `ManagedServiceAccount` or
  `ManifestWork` objects
- chart RBAC does not grant Secret read
- chart RBAC does not grant namespace create/update/patch/delete
- chart values do not expose generic `rbac.extraRules`
- static guardrail rejects CAPI imports and direct kubeconfig helper usage
- generated CRDs in `config/crd/bases` match chart CRD templates once APIs are
  implemented
- future admission/webhook tests reject forbidden force-delete annotation
  changes for unprivileged users before any force-delete behavior is enabled

E2E:

- create `ManagedClusterSet`, `ManagedClusterSetBinding`, `Placement`, and fake
  managed cluster namespaces on a hub test cluster
- create a `ManagedServiceAccountSet` with inline placement
- verify one `ManagedServiceAccount` per selected cluster namespace
- add a managed cluster to the placement and verify fan-out
- remove a managed cluster from the placement and verify stale child cleanup
- enable remote permissions and verify generated `ManifestWork` shape
- create a fake `PlacementDecision` with the right label but wrong owner UID and
  verify no child is created for that cluster
- delete the `ManagedServiceAccountSet` and verify cleanup order
- run with `credentialPublication: ClusterProfile` and verify the sync label is
  present without this operator reading token Secrets

## Diagnostics / Runbook

Collectors should include:

- `kubectl get managedserviceaccountsets.authentication.appthrust.io -A -o yaml`
- `kubectl get placements.cluster.open-cluster-management.io -A -o yaml`
- `kubectl get placementdecisions.cluster.open-cluster-management.io -A -o yaml`
- `kubectl get managedclusters.cluster.open-cluster-management.io --show-labels`
- `kubectl get managedserviceaccounts.authentication.open-cluster-management.io -A --show-labels`
- `kubectl get manifestworks.work.open-cluster-management.io -A --show-labels`
- events from this controller namespace
- generated child labels filtered by `authentication.appthrust.io/set-uid`

Diagnostics must not print token Secret data.

## Open Decisions

- Whether v1alpha1 should expose `placementRef` immediately or start with
  inline placement only.
- Whether wildcard RBAC should remain permanently forbidden or become an
  operator-config-gated exception for selected platform controllers.
- Whether status should expose per-cluster status behind a separate debug API
  instead of a bounded `failedClusters` list.
- Exact implementation dependency versions for OCM API, controller-runtime,
  and Kubernetes after the first code pass.

## Review Gates

Before implementation starts, the design requires sign-off from:

- API reviewer: confirms OCM Placement is the selection source, API scope is
  namespaced, and v1alpha1 validation is fail-closed.
- Controller/RBAC reviewer: confirms finalizer ordering, ownership labels,
  conflict handling, and typed remote RBAC rendering.
- Security reviewer: confirms no Secret reads, no arbitrary ManifestWork input,
  no tenant-bypass cluster selection, and privileged force-delete handling.
- Repo/release/test reviewer: confirms source, chart, image, and release checks
  match appthrust operator conventions.

## Review Log

- Round 0 draft:
  - created from the upstream issue comment, the OCM CAPI / ClusterProfile
    migration plan, upstream managed-serviceaccount code, OCM placement API
    code, and the `aws-workload-identity-operator` repo/release/test shape
  - chose namespaced `ManagedServiceAccountSet` to align with OCM Placement and
    ManagedClusterSetBinding authorization
  - chose OCM `PlacementDecision` as the cluster selection source instead of
    direct ManagedCluster label matching
  - made `credentialPublication` explicit because ClusterProfile credential
    sync has broad namespace effects
  - scoped remote permissions to typed RBAC instead of arbitrary ManifestWork
    templates
- Round 1 agent-team review incorporated:
  - security/lifecycle review required per-cluster fan-out, no token Secret
    reads, strict typed remote RBAC, tenant-bounded selection, explicit deletion
    order, privileged force-delete, source UID child labels, and mandatory
    admission validation
  - API/controller review approved namespaced `ManagedServiceAccountSet` and
    OCM `PlacementDecision` as the source of truth, and rejected direct
    ManagedCluster label matching
  - repo/release/test review required a scaffold that carries AGENTS, project
    metadata, Helm chart, GitHub workflows, release signing, static guardrails,
    and kest chart tests from the beginning
- Round 2 targeted review fixes:
  - trust only `PlacementDecision` objects controlled by the exact resolved
    `Placement` UID
  - remove namespace write permissions from chart RBAC
  - grant write permissions for owned inline `Placement` while keeping
    `PlacementDecision` read-only
  - replace raw remote RBAC rule input with named reviewed permission profiles
  - issue MSA deletion immediately after remote-permission delete so
    credentials are not kept live behind stuck remote cleanup
- Round 3 repo/release/test fixes:
  - added real `ManagedServiceAccountSet` Go API types and generated deepcopy
    code
  - made `controller-gen` the source of truth for CRDs and synchronized the
    Helm CRD copy in `make generate`
  - added kustomize config, sample manifests, and implementation-ready Docker
    build inputs
  - strengthened Helm schema validation, non-default test values, CRD drift
    checks, static guardrails, and kest chart tests
