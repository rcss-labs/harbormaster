# Evaluated once: a recursive `?=` would re-run git describe after `build` dirties the tree,
# so the binary and the image tag could disagree.
ifeq ($(origin VERSION),undefined)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
endif
ifeq ($(origin COMMIT),undefined)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
endif
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build test test-db e2e lint run

build:
	if [ -f web/package.json ]; then npm --prefix web ci && npm --prefix web run build; fi
	go build -ldflags "$(LDFLAGS)" -o bin/harbormaster ./cmd/harbormaster

test:
	go test -timeout 30m ./...
	if [ -f web/package.json ]; then npm --prefix web test -- --run; fi

lint:
	@unformatted="$$(gofmt -l cmd internal)"; if [ -n "$$unformatted" ]; then echo "gofmt needed for:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	if [ -f web/package.json ]; then npm --prefix web run lint && npm --prefix web run typecheck; fi
	tools/check-notices.sh

test-db:
	if docker inspect hm-test-pg >/dev/null 2>&1; then \
		docker start hm-test-pg && \
		echo "hm-test-pg already exists; reusing it (check its port/credentials with: docker port hm-test-pg)"; \
	else \
		docker run -d --name hm-test-pg -e POSTGRES_PASSWORD=hm -p 54329:5432 postgres:16.4-alpine && \
		echo "export HM_TEST_DATABASE_URL=postgres://postgres:hm@127.0.0.1:54329/postgres?sslmode=disable"; \
	fi

run:
	HM_FAKE_AWS=1 HM_LISTEN=127.0.0.1:8480 HM_COOKIE_SECURE=false HM_SESSION_KEY=dev-session-key HM_DATABASE_URL="$(HM_TEST_DATABASE_URL)" go run ./cmd/harbormaster serve

# e2e builds the binary, recreates the dedicated schema (tools/e2eschema; HM_E2E_SCHEMA, default hm_e2e) so each
# run starts empty, then runs the Playwright smoke and flow specs against `serve --fake-aws`. Playwright wipes
# web/test-results on every run, so a failed run's traces and screenshots are copied to web/test-results-failed/<time>.
e2e:
	@if [ -z "$$HM_TEST_DATABASE_URL" ]; then \
		echo "make e2e: HM_TEST_DATABASE_URL is not set; run 'make test-db' and export the URL it prints" >&2; \
		exit 2; \
	fi
	@e2e_port="$${HM_E2E_PORT:-8481}"; if lsof -iTCP:"$$e2e_port" -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "make e2e: port $$e2e_port is already in use; stop the process listening on it" >&2; \
		exit 2; \
	fi
	$(MAKE) build
	@e2e_url="$$(go run ./tools/e2eschema)" || { echo "make e2e: could not prepare the e2e schema" >&2; exit 1; }; \
	cd web && rm -rf test-results && { HM_E2E_DATABASE_URL="$$e2e_url" npx playwright test; status=$$?; \
		if [ $$status -ne 0 ] && [ -d test-results ]; then \
			keep="test-results-failed/$$(date +%Y%m%dT%H%M%S)"; mkdir -p "$$keep" && cp -R test-results/. "$$keep"/ \
				&& echo "make e2e: failure artifacts kept in web/$$keep" >&2; \
		fi; exit $$status; }

.PHONY: tf-check
tf-check:
	tools/tf-check.sh

.PHONY: test-it-exec
# Real-AWS ECS Exec integration test. Export the HM_IT_* variables first (see README "Terminal").
test-it-exec:
	HM_IT_AWS=1 go test ./internal/execbridge/ -run TestIntegration -v

# plan 06
# ARCH is the image architecture. The default matches the module's cpu_architecture = "X86_64"; pass ARCH=arm64
# together with cpu_architecture = "ARM64". check-image runs the image as built (emulated on a different host arch).
ARCH ?= amd64
.PHONY: image check-image release release-check scanner-image check-scanner-image
image:
	docker build --platform linux/$(ARCH) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t harbormaster:$(VERSION) .

# check-image runs the shipped image when this host can execute it (same architecture, or emulation such as
# binfmt/Rosetta). Otherwise it checks the shipped image's static contract, then builds the same Dockerfile for
# the host architecture and runs the runtime checks on that build. Only the target platform differs, and the
# arch-independent contract (user, healthcheck, distroless, read-only start) is what the checks cover.
check-image:
	@if tools/check-image.sh --can-run harbormaster:$(VERSION); then \
		tools/check-image.sh harbormaster:$(VERSION); \
	else \
		native="$$(docker info --format '{{.Architecture}}' | sed -e 's/aarch64/arm64/' -e 's/x86_64/amd64/')"; \
		echo "check-image: host cannot run harbormaster:$(VERSION); static checks on it, runtime checks on a native $$native build"; \
		CHECK_IMAGE_STATIC_ONLY=1 tools/check-image.sh harbormaster:$(VERSION) && \
		docker build -q --platform linux/$$native --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t harbormaster:$(VERSION)-check . >/dev/null && \
		tools/check-image.sh harbormaster:$(VERSION)-check; \
	fi

# scanner-image builds the scanner task image (Dockerfile.scanner): the harbormaster-scanner binary plus Syft and
# Grype, downloaded and verified (cosign, signed checksums) inside the build. Nothing is pushed.
scanner-image:
	docker build -f Dockerfile.scanner --platform linux/$(ARCH) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t harbormaster-scanner:$(VERSION) .

# check-scanner-image follows check-image: runtime checks on the shipped image when this host can execute it,
# otherwise static checks on it and runtime checks on a native build of the same Dockerfile.
check-scanner-image:
	@if tools/check-scanner-image.sh --can-run harbormaster-scanner:$(VERSION); then \
		tools/check-scanner-image.sh harbormaster-scanner:$(VERSION); \
	else \
		native="$$(docker info --format '{{.Architecture}}' | sed -e 's/aarch64/arm64/' -e 's/x86_64/amd64/')"; \
		echo "check-scanner-image: host cannot run harbormaster-scanner:$(VERSION); static checks on it, runtime checks on a native $$native build"; \
		CHECK_IMAGE_STATIC_ONLY=1 tools/check-scanner-image.sh harbormaster-scanner:$(VERSION) && \
		docker build -q -f Dockerfile.scanner --platform linux/$$native --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t harbormaster-scanner:$(VERSION)-check . >/dev/null && \
		tools/check-scanner-image.sh harbormaster-scanner:$(VERSION)-check; \
	fi

# release runs its prerequisites in order (do not use make -j). The build step generates the web app,
# so restore the tracked placeholder afterwards: git checkout -- internal/web/dist/index.html
release: lint test vulncheck build image check-image scanner-image check-scanner-image
	@echo "image: harbormaster:$(VERSION)"
	@echo "scanner image: harbormaster-scanner:$(VERSION)"
	bin/harbormaster version

# vulncheck scans the call graph of every package against the Go vulnerability database (needs network access).
.PHONY: vulncheck
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# release-check refuses an untagged HEAD and a -dirty VERSION.
release-check:
	@if [ -z "$$(git tag --points-at HEAD)" ]; then echo "release-check: HEAD is not tagged" >&2; exit 1; fi
	@case "$(VERSION)" in *-dirty) echo "release-check: VERSION $(VERSION) is dirty; commit or stash changes" >&2; exit 1;; esac

# plan 06
.PHONY: icons
icons:
	node web/scripts/render-icons.mjs

.PHONY: check-notices
check-notices:
	tools/check-notices.sh

# plan 06
# soak runs the rate-limit / back-off soak tests at target estate scale (about 10 minutes, needs HM_TEST_DATABASE_URL; see 'make test-db').
.PHONY: soak
soak:
	@if [ -z "$$HM_TEST_DATABASE_URL" ]; then \
		echo "make soak: HM_TEST_DATABASE_URL is not set; run 'make test-db' and export the URL it prints" >&2; \
		exit 2; \
	fi
	HM_SOAK=1 go test -count=1 -tags soak -run TestSoak -timeout 20m -v ./internal/poller

# Floci harness (tools/floci): a local AWS emulator plus Postgres, so Harbormaster's real AWS code path is checked
# against an ECS estate. Every script sources tools/floci/env.sh, which refuses (exit 2) any non-local endpoint.
# Typical run: make floci-up floci-seed floci-check floci-down
.PHONY: floci-selfcheck floci-up floci-seed floci-serve floci-check floci-down
FLOCI_COMPOSE := docker compose -f tools/floci/docker-compose.yml

floci-selfcheck:
	tools/floci/env_test.sh

floci-up:
	tools/floci/env.sh >/dev/null
	$(FLOCI_COMPOSE) up -d --wait

floci-seed:
	tools/floci/seed.sh

# floci-serve prepares the database, then runs the server in the foreground; run tools/floci/connect.sh from another shell.
floci-serve:
	tools/floci/prepare.sh
	tools/floci/serve.sh

# floci-check builds the binary, prepares the database and runs the Playwright floci suite (its webServer starts the
# server and its setup project runs connect.sh). Set HM_E2E_SCREENSHOT_DIR to keep a screenshot per route, width and theme.
floci-check:
	tools/floci/env.sh >/dev/null
	$(MAKE) build
	git checkout -- internal/web/dist/index.html
	tools/floci/prepare.sh
	cd web && npx playwright test -c playwright.floci.config.ts

floci-down:
	$(FLOCI_COMPOSE) down -v --remove-orphans
	rm -f tools/floci/.admin-password
	rm -rf web/.auth-floci
