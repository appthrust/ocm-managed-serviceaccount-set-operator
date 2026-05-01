# Governance

This project is currently maintained by the approvers and reviewers listed in
`OWNERS`.

## Roles

Reviewers provide technical review and may approve routine changes after
maintainer agreement.

Approvers can approve and merge changes, cut releases, and update project
policy.

Maintainers are expected to protect the project guardrails in `AGENTS.md`,
especially credential handling, child status ownership, and deletion ordering.

## Decision Making

Routine technical decisions are made by lazy consensus in pull requests. For
larger API, security, or compatibility decisions, maintainers should document
the rationale in an issue or design document before merging.

## Role Changes

New reviewers or approvers should demonstrate sustained, high-quality
contributions. Removing inactive maintainers requires public notice in an issue
or pull request and approval from remaining approvers.

## Conflict Resolution

If maintainers disagree, prioritize security, least privilege, and compatibility
with OCM APIs. Unresolved decisions should remain unmerged until consensus or a
documented maintainer decision exists.
