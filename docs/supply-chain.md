# Supply Chain

Release workflows publish controller images and Helm charts to GHCR and sign
both with cosign.

## Current Controls

- GitHub Actions are pinned by commit SHA.
- Controller image builds are multi-arch.
- Helm chart rendering is tested.
- Static guardrails check for forbidden API access and unsafe chart RBAC.
- Release image and chart artifacts are signed.

## User Verification

Verify artifacts by digest before production deployment:

```sh
cosign verify ghcr.io/appthrust/ocm-managed-serviceaccount-replicaset-controller@sha256:<digest>
cosign verify ghcr.io/appthrust/helm-charts/ocm-managed-serviceaccount-replicaset-controller@sha256:<digest>
```

Match the certificate identity to this repository's GitHub Actions workflow.

## Roadmap

- Publish SBOMs for controller images.
- Publish SLSA provenance for release artifacts.
- Add dependency vulnerability scanning.
- Document artifact retention and rebuild policy.
