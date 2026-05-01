# OCM ManagedServiceAccount Set Operator

Design-phase repository for a standalone appthrust operator that provides a
high-level API for creating OCM `ManagedServiceAccount` resources across an OCM
cluster set.

The upstream `ManagedServiceAccount` API is per managed cluster namespace. It
does not support creating one `ManagedServiceAccount` in a cluster-set namespace
and having it apply to every cluster in the set. This repository captures the
operator design that fills that gap.

See [plan.md](plan.md) for the API, controller, release, and test design.

## Current State

This repository intentionally contains a minimal operator scaffold so release,
image, Helm, and test wiring can be reviewed before controller implementation.

## Checks

```sh
make test
make lint
make helm-lint
make test-kest
```

