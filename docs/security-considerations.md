# Security Considerations

This page is retained as a short checklist. See [Security Model](security.md)
and [Threat Model](threat-model.md) for the complete security documentation.

The API accepts typed RBAC policy rules so platform teams can model remote
permissions without shipping controller code for each new role.

Admission policy should enforce the local organization boundary for:

- wildcard verbs or resources
- escalation verbs such as `bind`, `escalate`, and `impersonate`
- writes to sensitive resources such as Secrets, RBAC resources, webhook
  configurations, and certificate signing requests
- allowed values for
  `authentication.open-cluster-management.io/sync-to-clusterprofile`

The chart does not install Kyverno, Gatekeeper, or ValidatingAdmissionPolicy
objects because those policies are organization-specific.
