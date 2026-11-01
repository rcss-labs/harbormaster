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
	go test ./...
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
	HM_FAKE_AWS=1 HM_LISTEN=127.0.0.1:8480 HM_COOKIE_SECURE=false HM_DATABASE_URL="$(HM_TEST_DATABASE_URL)" go run ./cmd/harbormaster serve

# e2e builds the binary, recreates the dedicated schema (tools/e2eschema; HM_E2E_SCHEMA, default hm_e2e) so each
# run starts empty, then runs the Playwright smoke and flow specs against `serve --fake-aws`.
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
	cd web && HM_E2E_DATABASE_URL="$$e2e_url" npx playwright test

.PHONY: tf-check
tf-check:
	tools/tf-check.sh

.PHONY: test-it-exec
# Real-AWS ECS Exec integration test. Export the HM_IT_* variables first (see README "Terminal").
test-it-exec:
	HM_IT_AWS=1 go test ./internal/execbridge/ -run TestIntegration -v

# plan 06
.PHONY: image check-image release release-check
image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t harbormaster:$(VERSION) .

check-image:
	tools/check-image.sh harbormaster:$(VERSION)

# release runs its prerequisites in order (do not use make -j). The build step generates the web app,
# so restore the tracked placeholder afterwards: git checkout -- internal/web/dist/index.html
release: lint test build image check-image
	@echo "image: harbormaster:$(VERSION)"
	bin/harbormaster version

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
