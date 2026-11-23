# Contributing

This page explains how to propose and submit changes to Harbormaster. It is for anyone who wants to fix a bug, add a feature or improve the docs.

## Before you begin

- Read the [Code of conduct](code-of-conduct.md).
- Set up your machine with the [development environment](../development/dev-environment.md).
- Report security problems privately, not as issues. See [Reporting vulnerabilities](../security/reporting-vulnerabilities.md).

## Propose a change

1. **Open an issue first for anything big.** New features, behavior changes, new dependencies and large refactors need agreement before you write code. Small fixes and doc corrections can go straight to a pull request.
2. **Fork the repository** at <https://github.com/rcss-labs/harbormaster> and create a branch from `main`.
3. **Write the change with a test.** Bug fixes start with a test that fails for the right reason. New or changed behavior needs a test. Docs and formatting changes do not. See [Testing](../development/testing.md).
4. **Keep it focused.** One concern per pull request.
5. **Run the checks:**

    ```sh
    make lint test
    ```

    Run the extra suites that touch your change (for example `make e2e` for UI flows, `make tf-check` for Terraform).
6. **Commit with conventional commits:** `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `perf:`. Say why in the message body.
7. **Open a pull request** against `main` and describe what changed, why, and how you tested it. CI runs on every pull request; `main` only accepts changes through a pull request whose `lint`, `go-test`, `web-test` and `tf-check` checks pass. See [Continuous integration](../development/testing.md#continuous-integration).

## Pull request checklist

- [ ] The change is focused and linked to an issue when it is large.
- [ ] Tests cover the new or fixed behavior.
- [ ] `make lint test` passes locally, and the required CI checks are green.
- [ ] Docs are updated if behavior, settings or Terraform inputs changed.
- [ ] Third-party notices are updated if you add a bundled component (`tools/check-notices.sh` passes).
- [ ] No secrets, credentials or real account IDs are included.
- [ ] `internal/web/dist/index.html` is unchanged (restore it with `git checkout -- internal/web/dist/index.html` after a build).

## Licence of contributions

Harbormaster is MIT licensed. By submitting a contribution you agree that it is licensed under the same terms. The repository does not require a DCO sign-off.

## Next steps

- [Coding conventions](../development/coding-conventions.md)
- [Governance](governance.md)
- [Support](support.md)
