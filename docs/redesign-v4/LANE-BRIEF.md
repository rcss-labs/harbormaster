# Lane brief: read this first

You implement one lane of `docs/superpowers/plans/2026-11-06-ui-redesign-v4.md`. Read in the plan: the header, Global Constraints, Review Focus, Decisions, API contracts, and your lane. Also read `docs/redesign-v4/README.md` (spec), the matching parts of `docs/redesign-v4/INVENTORY.md`, the mockup screens for your pages in `docs/redesign-v4/mockup/src/screens/`, and `docs/superpowers/plans/2026-10-31-00-conventions.md`. The mockup is a reference: rebuild with real API data; its "Show state", "Preview as", "View as" and "Rollout state" controls are not built.

## Rules
- You are in your own git worktree. Confirm with `git branch --show-current`, and that it contains the plan file; if it doesn't, run `git merge --ff-only feat/ui-redesign-v4` and report if that fails. Edit only files your lane owns. If you need a change elsewhere, stop and report it. Don't touch the main checkout.
- Test-first for behaviour: write the test, run it, confirm it fails for the expected reason (not an import error or typo), then implement. Pure restyles update existing tests. Test observable behaviour, not implementation. Stop adding tests once the behaviour is covered.
- Every permission check, lock, disabled reason, dialog, toast and validation message stays with its exact wording, unless the plan's Decisions rename it.
- No new npm dependencies. No `max-width` on page containers. 12 px minimum, 4.5:1 contrast, no raw hex in components, no emoji.
- Decisions: use the TypeSafe AI skill (`typesafe:typesafe-ai`, key in `TYPESAFE_API_KEY`) for real choices between approaches, red-phase checks where the failure is ambiguous, and ranking root causes. Include its verdict in the commit message for non-trivial decisions. If it is unavailable or fails, say so in your report and proceed. Haiku lanes don't decide: if a decision is needed, stop and escalate in your report.
- Conventional commits; the body says why. Every commit message ends with exactly:
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01G6atqei8utLsUdYaVb2i6f`
- Never commit `internal/web/dist`; after any web build run `git checkout -- internal/web/dist/index.html`. Don't push. Don't merge. Never print secrets.

## Commands
Go and web tests that need a database, and e2e, use the shared test Postgres (container `hm-v4-pg`, already running; test-only password):
`export HM_TEST_DATABASE_URL='postgres://postgres:hm@127.0.0.1:55498/postgres?sslmode=disable'`

- Setup once: `npm --prefix web ci`.
- Web unit: `npm --prefix web test -- --run <path>`; before hand-off also `npm --prefix web run lint && npm --prefix web run typecheck`.
- Go: `go test ./internal/<pkg>/...`; before hand-off `gofmt -l cmd internal` and `go vet ./...`.
- Your e2e specs only, on your own port and schema (`<nn>` = your port number below):
  `make build && E2E_URL="$(HM_E2E_SCHEMA=v4_<lane> go run ./tools/e2eschema)" && (cd web && HM_E2E_PORT=<nn> HM_E2E_DATABASE_URL="$E2E_URL" npx playwright test <your specs>); git checkout -- internal/web/dist/index.html`
- Don't run the whole `make e2e`, and don't use floci unless your lane says so: the lead runs both at the wave gate.

Ports: 0A 8410, 0B 8411, 0D 8412, 1A 8420, 1B 8421, 1C 8422, 1D 8423, 1E 8424, 1F 8425, 1G 8426, 1H 8427, 1I 8428, 1J 8429, 1K 8430, 1L 8431, 2A 8440, 2B 8441, 2C 8442.

## Final report (under 150 words)
Branch name and head commit; tests run and their results, with any failures quoted; anything you could not finish; files outside your ownership you need changed; contract problems; TypeSafe verdicts used or why not. Never claim a check you didn't run.
