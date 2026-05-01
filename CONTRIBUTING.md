# Contributing

This repository is currently design-first. Changes should keep the design,
chart scaffold, release workflow, and tests aligned.

Read [GOVERNANCE.md](GOVERNANCE.md), [SECURITY.md](SECURITY.md), and
[ROADMAP.md](ROADMAP.md) before proposing API or security-sensitive changes.

Before sending a change, run:

```sh
make test
make lint
make verify-static
make helm-lint
make test-chart
make test-e2e
```

When API types are added, run the generated manifest check before committing:

```sh
make verify-generated
```

Do not include tokens, kubeconfigs, or Secret data in issues, tests, fixtures,
or logs.

Pull requests that change credential handling, remote RBAC, placement
resolution, generated child ownership, deletion ordering, or controller RBAC
must include focused tests and documentation updates.
