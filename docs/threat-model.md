# Threat Model

## Assets

- Managed service account credentials produced by OCM.
- Remote permissions delivered through generated `ManifestWork` objects.
- Hub-side OCM placement and placement-decision output.
- Generated child metadata used for cleanup.

## Trust Assumptions

- The hub cluster control plane and OCM controllers are trusted.
- Managed cluster namespaces on the hub correspond to OCM managed clusters.
- Admission policy controls tenant access to this API.
- Tenants are not allowed to mutate generated children unless explicitly
  delegated by the platform team.

## Abuse Cases

Privilege escalation through remote RBAC is the primary risk. A tenant with
permission to create a replica set could request broad verbs, wildcard
resources, or permissions over sensitive objects. The API remains typed, but
admission must enforce local policy.

Placement abuse is another risk. A tenant could reference placement output that
selects clusters outside their intended boundary. Namespace and placement
ownership must be controlled by hub RBAC and admission.

Cleanup bypass is a risk during deletion. The controller blocks deletion until
OCM removes generated `ManifestWork` children before deleting generated
`ManagedServiceAccount` children. It does not honor force-delete annotations or
fields.

Generated child takeover is handled by source labels. If an expected child name
exists without matching source labels, the controller reports `ChildConflict`
instead of adopting it.

## Mitigations

- Keep controller RBAC least-privilege.
- Use admission policy for remote RBAC rule limits and namespace boundaries.
- Keep generated child labels and annotations immutable to tenants.
- Alert on long-lived `CleanupBlocked=True`.
- Review release signatures before installing production artifacts.
