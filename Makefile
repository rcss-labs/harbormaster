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
	HM_FAKE_AWS=1 HM_LISTEN=127.0.0.1:8480 HM_COOKIE_SECURE=false HM_DATABASE_URL=$(HM_TEST_DATABASE_URL) go run ./cmd/harbormaster serve

e2e:
	@echo "e2e: added in Task 36"

.PHONY: tf-check
tf-check:
	tools/tf-check.sh
