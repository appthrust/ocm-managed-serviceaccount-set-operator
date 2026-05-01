# Admission Policy

This project does not install admission policy because tenant boundaries are
organization-specific. Production platforms should add validating admission for
`ManagedServiceAccountReplicaSet`.

Recommended checks:

- Deny wildcard verbs and wildcard resources unless explicitly approved.
- Deny `bind`, `escalate`, and `impersonate`.
- Deny writes to Secrets, RBAC resources, admission webhooks, CSRs, and token
  review APIs unless explicitly approved.
- Restrict which namespaces may create replica sets.
- Restrict which placement ref names each namespace may reference.
- Restrict namespace selector labels and operators for selector-targeted
  grants.
- Restrict who may set template
  `authentication.open-cluster-management.io/sync-to-clusterprofile=false`.

Example policy shape for Kubernetes `ValidatingAdmissionPolicy`:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: managedserviceaccountreplicaset-rbac-guardrails
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["authentication.appthrust.io"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["managedserviceaccountreplicasets"]
  validations:
    - expression: "!has(object.spec.rbac) || object.spec.rbac.grants.all(g, g.rules.all(rule, !('*' in rule.verbs) && !('*' in rule.resources)))"
      message: "wildcard verbs and resources are not allowed in RBAC grants"
    - expression: "!has(object.spec.rbac) || object.spec.rbac.grants.all(g, !has(g.forEachNamespace) || !has(g.forEachNamespace.selector) || ('workload-identity.appthrust.io/profile' in g.forEachNamespace.selector.matchLabels))"
      message: "selector grants must include the platform opt-in label"
```

Treat this as a starting point, not a complete production policy.
