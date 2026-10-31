VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)

.PHONY: build test test-db e2e lint run

build:
	if [ -f web/package.json ]; then npm --prefix web ci && npm --prefix web run build; fi
	go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/harbormaster ./cmd/harbormaster

test:
	go test ./...
	if [ -f web/package.json ]; then npm --prefix web test -- --run; fi

lint:
	@unformatted="$$(gofmt -l cmd internal)"; if [ -n "$$unformatted" ]; then echo "gofmt needed for:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	if [ -f web/package.json ]; then npm --prefix web run lint && npm --prefix web run typecheck; fi

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
# run starts empty, then runs the Playwright smoke suite against `serve --fake-aws`.
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
