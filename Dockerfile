# Harbormaster image: web build -> Go build -> distroless nonroot runtime.
# Build: make image VERSION=<version> [ARCH=amd64|arm64]  (default amd64, matching the module's cpu_architecture=X86_64)
# The web and Go stages run on the build host's native platform and cross-compile, so building for
# another architecture needs no emulation; only the final distroless stage is for the target platform.

# node:22.23.4-alpine3.24
FROM --platform=$BUILDPLATFORM node:22.23.4-alpine3.24@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914 AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
# vite.config.ts writes to ../internal/web/dist, i.e. /src/internal/web/dist
RUN npm run build

# golang:1.26.10-alpine3.24
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine3.24@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS go-build
ARG VERSION=dev
ARG COMMIT=none
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Replace the tracked placeholder with the real web build before embedding.
COPY --from=web-build /src/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/harbormaster ./cmd/harbormaster

# gcr.io/distroless/static-debian12:nonroot (no versioned tags; pinned by digest)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=go-build /out/harbormaster /harbormaster
USER nonroot:nonroot
ENV HM_LISTEN=:8480
EXPOSE 8480
# Exec form: the image has no shell.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/harbormaster", "healthcheck"]
ENTRYPOINT ["/harbormaster"]
CMD ["serve"]
