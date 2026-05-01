# Contributing

This repository is currently design-first. Changes should keep the design,
chart scaffold, release workflow, and tests aligned.

Before sending a change, run:

```sh
make test
make lint
make verify-static
make helm-lint
make test-kest
```

When API types are added, run the generated manifest check before committing:

```sh
make verify-generated
```

