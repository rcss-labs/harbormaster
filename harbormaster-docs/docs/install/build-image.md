# Build the image

This page shows how to build the Harbormaster images from source and push them to ECR. It is for the engineer preparing a release for deployment.

## The images

| Image | Dockerfile | Make target | What it holds |
| --- | --- | --- | --- |
| `harbormaster` | `Dockerfile` | `make image` | The server and the embedded web app |
| `harbormaster-scanner` | `Dockerfile.scanner` | `make scanner-image` | The scanner binary plus Syft and Grype. Needed only for image scanning |
| `harbormaster-mcp` | `Dockerfile.mcp` | `make mcp-image` | The MCP gateway binary alone. Needed only for the [MCP gateway](mcp-gateway.md) |

All three use multi-stage builds and end on a distroless, non-root runtime image.

### Main image

`Dockerfile` has three stages: a Node stage builds the web app, a Go stage builds the binary (static, `CGO_ENABLED=0`) with the web build embedded, and a `distroless/static` nonroot stage runs it. The build and Go stages run on the build host's native platform and cross-compile, so building for another architecture needs no emulation.

The final image:

- runs as `nonroot`,
- listens on `HM_LISTEN=:8480` and exposes 8480,
- has a `HEALTHCHECK` that runs `harbormaster healthcheck`,
- has no shell, with entrypoint `/harbormaster` and default command `serve`.

### Scanner image

The build downloads Syft and Grype, verifies their cosign signatures and checksums (`tools/scanner-verify.sh`, with versions and identities pinned as `ARG`s), and fails if anything does not verify.

### MCP gateway image

The gateway binary alone on distroless nonroot, with no credentials. It listens on `:8481` and has its own `HEALTHCHECK`.

## Before you begin

- Docker running locally.
- A checkout of the release you want to deploy.
- AWS CLI v2 with a profile that can push to ECR in the Harbormaster account.

## Build

1. Build the image. `VERSION` becomes the image tag. If you leave it out, it comes from `git describe`, or `dev` outside a git checkout.

    ```sh
    make image VERSION=v1.0.0
    ```

2. Check it. This verifies the runtime contract: non-root, healthcheck, read-only root filesystem.

    ```sh
    make check-image VERSION=v1.0.0
    ```

    If your machine cannot run the image's architecture, `check-image` checks the shipped image statically, then builds the same Dockerfile for your host architecture and runs the runtime checks on that build. Nothing is pushed.

3. Optional: build and check the scanner and gateway images.

    ```sh
    make scanner-image VERSION=v1.0.0
    make check-scanner-image VERSION=v1.0.0
    make mcp-image VERSION=v1.0.0
    make check-mcp-image VERSION=v1.0.0
    ```

    `check-mcp-image` also confirms the gateway has no shell, is a static binary, and links no database, cache, AWS or SAML module.

## Architecture (`ARCH`)

`make image` builds `--platform linux/amd64` by default, matching the module's default `cpu_architecture = "X86_64"`. This holds on any build host, Apple silicon included.

To run on Graviton, set `cpu_architecture = "ARM64"` in the module and build with `ARCH=arm64`. Use the same `ARCH` for the scanner and gateway images.

```sh
make image VERSION=v1.0.0 ARCH=arm64
```

The image architecture must match the module setting. If it does not, ECS tasks fail with `exec format error`.

## Push to ECR

1. Create the repository if it does not exist. Immutable tags are recommended: they stop a tag from moving after it was scanned or deployed.

    ```sh
    aws ecr create-repository --repository-name harbormaster \
      --image-tag-mutability IMMUTABLE \
      --profile <profile> --region <region>
    ```

2. Log in, tag and push.

    ```sh
    aws ecr get-login-password --profile <profile> --region <region> \
      | docker login --username AWS --password-stdin <account-id>.dkr.ecr.<region>.amazonaws.com
    docker tag harbormaster:v1.0.0 <account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:v1.0.0
    docker push <account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:v1.0.0
    ```

3. Push the scanner and gateway images the same way, to their own repositories (`harbormaster-scanner`, `harbormaster-mcp`).

The scanner image is referenced by digest in the module. Read the digest after the push:

```sh
aws ecr describe-images --repository-name harbormaster-scanner \
  --image-ids imageTag=v1.0.0 --query 'imageDetails[0].imageDigest' --output text \
  --profile <profile> --region <region>
```

## Check it worked

- `docker run --rm harbormaster:v1.0.0 version` prints a line like `harbormaster v1.0.0 (<commit>)`.
- The image appears in your ECR repository with the tag you pushed.

## Next steps

- [Deploy with Terraform](deploy-with-terraform.md)
- [Release process](../development/release-process.md)
