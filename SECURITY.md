# Security Policy

## Supported Versions

This project is alpha. Security fixes are provided for the latest released
minor version only unless maintainers announce otherwise in the release notes.

## Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability.

Report privately to the maintainers listed in `OWNERS`. Include:

- Affected version or commit.
- Impact and affected component.
- Reproduction steps or proof of concept.
- Whether credentials, remote RBAC, or cleanup behavior are involved.

Maintainers should acknowledge reports within 3 business days and provide an
initial assessment within 10 business days.

## Disclosure

Maintainers coordinate fixes privately, prepare release notes, and publish a
fixed release before public disclosure when practical. Public advisories should
include impact, affected versions, mitigations, and upgrade guidance.

## Security Scope

Security-sensitive areas include:

- Remote RBAC rendering.
- OCM `ManagedServiceAccount` fan-out.
- Generated child ownership and cleanup.
- Controller RBAC.
- Admission policy recommendations.
- Release artifact integrity.

See [docs/security.md](docs/security.md) and
[docs/threat-model.md](docs/threat-model.md).
