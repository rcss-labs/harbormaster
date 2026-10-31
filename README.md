# Harbormaster

Harbormaster is one Go binary that serves an embedded React single-page app for viewing AWS ECS. It polls ECS across AWS accounts and regions into Postgres, and uses tag-based permissions to decide what each user can see.

## Make targets

- `make build`: build the web UI (when `web/package.json` exists) and the `bin/harbormaster` binary
- `make test`: run Go tests, plus web tests when the web app exists
- `make lint`: check gofmt, run `go vet`, and run web lint and typecheck when the web app exists
- `make test-db`: start (or reuse) the local Postgres test container `hm-test-pg`
- `make run`: run the server locally against fake AWS data and the test database
- `make e2e`: end-to-end tests (placeholder until Task 36)
