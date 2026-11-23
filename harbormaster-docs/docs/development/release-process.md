# Release process

This page describes how a Harbormaster release is cut. It is for maintainers.

## Before you begin

- Docker, Go, Node and network access (the vulnerability scan downloads `govulncheck`).
- `HM_TEST_DATABASE_URL` and `HM_TEST_KV_URLS` exported, so the full suite runs.
- A clean working tree on `main`.

## Steps

1. Update [the changelog](../community/changelog.md).
2. Tag the release commit, for example `git tag v1.0.0`. The version comes from `git describe --tags --always --dirty`.
3. Run the gate and the release together:

    ```sh
    make release-check release
    ```

    `release-check` fails unless `HEAD` is tagged and the version is not `-dirty`. `release` does not run it, so run both. Do not use `make -j`.

4. Restore the tracked placeholder, because the build overwrites it:

    ```sh
    git checkout -- internal/web/dist/index.html
    ```

5. Push the tag and publish the images to your registry with a pinned version tag. The Makefile builds images locally and pushes nothing.

## What `make release` runs

In order: `lint`, `test`, `vulncheck`, `build`, `image`, `check-image`, `scanner-image`, `check-scanner-image`, `mcp-image`, `check-mcp-image`, then prints the three image names and `bin/harbormaster version`.

| Step | What it checks |
|---|---|
| `vulncheck` | `govulncheck` on the call graph of every package; fails on called advisories not listed in `tools/vulncheck-accepted.txt` |
| `image` | Builds `harbormaster:<version>`. Use `ARCH=arm64` with `cpu_architecture = "ARM64"` |
| `check-image` | Non-root user, health check, distroless, read-only root filesystem start |
| `scanner-image`, `mcp-image` | Build the scanner and MCP gateway images |
| `check-scanner-image`, `check-mcp-image` | Same style of contract checks |

If the host cannot run the image's architecture, the check script runs static checks on the shipped image and runtime checks on a native build of the same Dockerfile.

## Check it worked

- All targets finish without errors.
- `bin/harbormaster version` prints the tag.
- `git status` shows no change to `internal/web/dist/index.html`.

## Next steps

- [Upgrading](../install/upgrading.md)
- [Hardening checklist](../security/hardening.md)
