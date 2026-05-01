# Support

This project is alpha. Production users should pin exact controller and chart
versions and test upgrades in a non-production hub first.

## Supported Versions

Only the latest released minor version receives routine fixes. Security fixes
may be backported at maintainer discretion.

## Compatibility

See [docs/compatibility.md](docs/compatibility.md).

## Getting Help

Open a GitHub issue for bugs, feature requests, and documentation gaps. Include:

- Controller version and chart version.
- Kubernetes and OCM versions.
- Relevant `ManagedServiceAccountReplicaSet` YAML.
- Parent status conditions.
- Generated child status when available.

Do not include tokens, kubeconfigs, or Secret data.
