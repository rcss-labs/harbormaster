# Coding conventions

This page lists the checks and habits expected of every change. It is for contributors.

## Go

- Format with `gofmt`. `make lint` fails on unformatted files in `cmd` and `internal`.
- `go vet ./...` must pass.
- `staticcheck` runs with `staticcheck.conf`, which is the default set minus `ST1005`. User-facing error strings are capitalised and punctuated on purpose, such as "Wrong username or password.". The pinned analyser version is in the `staticcheck` target of the `Makefile`.
- Keep findings at zero. When a finding is intended, add a `//lint:ignore` with the reason.
- Use descriptive names. Write comments only where the logic is not obvious. Every package has a doc comment that says what it is for.
- Packages depend on narrow interfaces (for example `awsx` and `kv`) so tests do not touch SDKs directly.

## Web

- ESLint and `tsc --noEmit` must pass (`make lint`).
- See [Frontend](frontend.md).

## Terraform

- Lowercase names with hyphens, consistent across a module.
- Run `terraform fmt` and `make tf-check` before committing.

## Shell scripts

- Start with `set -euo pipefail`, handle errors with meaningful exit codes, and include usage text for scripts that take arguments.

## Third-party notices

`tools/check-notices.sh` (part of `make lint`) checks that `LICENSE` is the MIT licence, that code ported from AWS carries its Apache-2.0 header, and that `THIRD_PARTY_NOTICES.md` names every bundled component. When you add a bundled dependency, font or ported code, update the notices and add the licence copy under `third_party/licenses/`. See [Licence](../community/license.md).

## Commits

Use conventional commits: `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `perf:`. Explain why the change is needed, not only what it does. Never commit secrets or credentials.

## Before you push

```sh
make lint test
```

## Next steps

- [Testing](testing.md)
- [Contributing](../community/contributing.md)
