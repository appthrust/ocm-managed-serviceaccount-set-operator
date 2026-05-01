# Release Process

Releases are tagpr-managed. Release artifacts are published by GitHub Actions
from the release tag.

## Versioning

Use semantic versions. While the API is alpha, breaking API changes are allowed
only with changelog entries and migration notes.

## Artifacts

Each release publishes:

- Multi-arch controller image to GHCR.
- Helm chart to GHCR OCI registry.
- Cosign signatures for image and chart artifacts.

## Checklist

Before merging a release PR:

- `CHANGELOG.md` describes user-visible changes.
- Generated CRDs and chart CRDs are in sync.
- `make test`, `make lint`, `make verify-static`, `make verify-generated`,
  `make helm-lint`, and `make test-chart` pass.
- Compatibility or migration notes are updated for API or behavior changes.
- Security notes are included for RBAC, credential, or cleanup changes.

## Verification

Users should verify signed artifacts with cosign before production deployment.
Use the image or chart digest from the registry and the GitHub Actions
certificate identity for this repository.

## Rollback

Rollback by installing a previous chart and controller image version that is
compatible with the installed CRD. Do not downgrade across CRD schema changes
unless the release notes explicitly allow it.

## Yanking

If a release is unsafe, maintainers publish a follow-up release and mark the bad
version in `CHANGELOG.md`. Registry artifacts should remain immutable unless
they contain leaked secrets or legally sensitive material.
