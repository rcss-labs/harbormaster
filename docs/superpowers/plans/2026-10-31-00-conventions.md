# Harbormaster Plans 00: Shared Conventions

> **For agentic workers:** This is not an executable plan. Every phase plan
> (01–06) implicitly includes this file. Read it before any task.

**Spec:** [`docs/design/2026-10-31-harbormaster-design.md`](../../design/2026-10-31-harbormaster-design.md)
**Mockup:** [`docs/design/mockup/index.html`](../../design/mockup/index.html) (open in a browser; screenshots in [`docs/design/screens/`](../../design/screens/))

## Plans

| # | Plan | Spec phase | Depends on |
|---|---|---|---|
| 01 | [Foundation, read-only](2026-10-31-01-foundation.md) | §17 phase 1 | 00 |
| 02 | [Change: task definitions and operations](2026-10-31-02-change.md) | phase 2 | 01 |
| 03 | [Observe: metrics, logs, scaling, alerts](2026-10-31-03-observe.md) | phase 3 | 01 |
| 04 | [Terminal: ECS Exec bridge](2026-10-31-04-terminal.md) | phase 4 | 01 |
| 05 | [Networks and storage](2026-10-31-05-networks-storage.md) | phase 5 | 01, 03 (metrics client and cache for EFS charts) |
| 06 | [Hardening: Terraform, image, limits, docs](2026-10-31-06-hardening.md) | phase 6 | 01–05 |

02, 03 and 04 depend only on 01 and may run in any order; 05 needs 03's `internal/metrics`.

## Global constraints (every plan)

- Go **1.26** (`go 1.26` in `go.mod`), module path `github.com/rcss-labs/harbormaster`.
- AWS SDK for Go **v2**. Postgres driver **pgx v5** (`pgxpool`). Logging **`log/slog`**. Router: `net/http` `ServeMux` with Go 1.22+ method/path patterns (no third-party router).
- Frontend: React **19**, TypeScript, Vite, react-router, TanStack Query v5, Vitest + Testing Library, Playwright for e2e. Node **≥ 22**. App lives in `web/`; Vite `build.outDir` is `../internal/web/dist` (Go `//go:embed` cannot reach outside its package), embedded by `internal/web`; `internal/web/dist/` is git-ignored except a placeholder `index.html` so `go build` works without a web build.
- Snapshot freshness: cache rows carry `polled_at` = when their content last changed. "Data as of" for pages comes from a per-loop record (`poll_loops(account_id, region, tier, last_ok_at, last_error, interval_ms)`), so unchanged rows are never rewritten.
- Database: PostgreSQL **16**. Migrations: plain SQL files in `internal/store/migrations/NNNN_name.up.sql`, applied on start by a small in-repo runner (table `schema_migrations(version int primary key, applied_at timestamptz)`); no down migrations.
- Licence **MIT**. **No code copied from Stackstead.** Apache-2.0 code ported from `aws/session-manager-plugin` keeps its headers + `THIRD_PARTY_NOTICES.md`.
- Config only via env vars with prefix `HM_` (see `internal/config`).
- All AWS calls go through `internal/awsx` interfaces so tests use fakes; no test may call real AWS unless guarded by `HM_IT_AWS=1`.
- DB tests use a real Postgres from `HM_TEST_DATABASE_URL` (each test package creates and drops its own schema `t_<random>`); `make test-db` starts `postgres:16.4-alpine` in Docker for local runs. Tests skip with a clear message when the var is unset.
- UI copy follows spec wording verbatim where the spec gives it (quoted strings). Layout, density and colours follow the mockup; full-width content, never a max-width container.
- Commits: conventional commits (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`), message says why. TDD: failing test first, then code.
- Format/lint before every commit: `gofmt -l cmd internal` empty, `go vet ./...`, `npm --prefix web run lint` and `npm --prefix web run typecheck`.

## Repository layout (target after all plans)

```
cmd/harbormaster/            main.go: subcommands serve | migrate | healthcheck | version | create-admin
internal/config/             Config struct, Load() from env
internal/model/              domain types shared everywhere (keys, Cluster, Service, Task, ...)
internal/store/              pgx pool, migration runner, repositories (one file per table group)
internal/store/migrations/   NNNN_*.up.sql
internal/auth/               argon2id, sessions, CSRF, login limiter, setup token, middleware
internal/authz/              Action/ActionSet, presets, tag resolution, Can/Visible, filters
internal/awsx/               per account×region clients, STS cache, throttling helpers, narrow API interfaces
internal/awsx/fake/          in-memory fake estate (seeded like the mockup) implementing the interfaces
internal/poller/             leader lock, account×region loops, tiers, diff writes, retention jobs
internal/status/             service/cluster status derivation, alerts            (01 status, 03 alerts)
internal/deploy/             taskdef conversion/validation, register, deploy, restart, scale, stop (02)
internal/metrics/            GetMetricData builders + Postgres-backed 60 s cache                 (03)
internal/logs/               FilterLogEvents history, StartLiveTail relay                         (03)
internal/execbridge/         SSM Session Manager data channel + WebSocket bridge                  (04)
internal/network/            VPC tree assembly                                                    (05)
internal/api/                server, middleware, handlers (one file per area), ws helpers, JSON errors
internal/web/                embed web/dist, SPA fallback
web/                         React app (see Frontend conventions)
terraform/modules/harbormaster/         (06)
terraform/modules/harbormaster-access/  (06)
Dockerfile, Makefile, README.md, THIRD_PARTY_NOTICES.md, LICENSE
```

## Shared Go types (defined in plan 01; later plans consume them unchanged)

Exact names and shapes. Plans 02–06 must not redefine these.

### `internal/model`

```go
type ClusterKey struct{ AccountID, Region, Cluster string }
type ServiceKey struct{ ClusterKey; Service string }

func (k ClusterKey) ID() string            // base64url(no padding) of "acct|region|cluster"
func (k ServiceKey) ID() string            // base64url of "acct|region|cluster|service"
func ParseClusterID(id string) (ClusterKey, error)
func ParseServiceID(id string) (ServiceKey, error)
func TaskID(accountID, region, taskARN string) string     // base64url of "acct|region|taskARN"
func ParseTaskID(id string) (accountID, region, taskARN string, err error)

type Status string // "steady" | "deploying" | "degraded" | "failing" | "unhealthy_task" | "scaled_to_zero"
type ClusterStatus string // "healthy" | "degraded" | "deploying" | "needs_attention"
```

Snapshot structs (normalized fields the UI needs; each also carries `Raw json.RawMessage` and `PolledAt time.Time`): `Cluster`, `Service`, `Deployment`, `ServiceEvent`, `Task`, `Container` (per task), `TaskDefinitionRef`. Field lists are fixed in plan 01 Task "model".

### `internal/authz`

```go
type Action uint8
const ( View Action = 1 << iota; Logs; Operate; Deploy; Edit; Terminal )   // V=1 L=2 O=4 D=8 E=16 T=32
type ActionSet uint8
var Presets = map[string]ActionSet{ "None":0, "Viewer":View, "Operator":View|Logs|Operate,
    "Deployer":View|Logs|Operate|Deploy|Edit, "Full":View|Logs|Operate|Deploy|Edit|Terminal }
func PresetName(s ActionSet) string        // "Custom" when none match
func Normalize(s ActionSet) ActionSet       // any action ⇒ View; no View ⇒ 0

type TagID int64
type Subject struct{ UserID int64; Username string; Admin bool; Perms map[TagID]ActionSet }

type Tags interface {                       // implemented by store; cached per request
    DevTagID() TagID
    ClusterTag(model.ClusterKey) (TagID, bool)   // false = untagged
    ServiceTag(model.ServiceKey) (TagID, bool)   // false = inherit
}
func EffectiveClusterTag(t Tags, k model.ClusterKey) TagID
func EffectiveServiceTag(t Tags, k model.ServiceKey) TagID
func Can(s Subject, a Action, tag TagID) bool    // Admin ⇒ true; else Perms[tag] has View and a
```

### `internal/awsx`

```go
type Target struct{ AccountID, Region string }
type Clients interface {
    ECS(ctx context.Context, t Target) (ECSAPI, error)
    EC2(ctx context.Context, t Target) (EC2API, error)                  // 05
    EFS(ctx context.Context, t Target) (EFSAPI, error)                  // 05
    ELB(ctx context.Context, t Target) (ELBAPI, error)                  // 03
    AutoScaling(ctx context.Context, t Target) (AutoScalingAPI, error)  // 03
    CloudWatch(ctx context.Context, t Target) (CloudWatchAPI, error)    // 03
    Logs(ctx context.Context, t Target) (LogsAPI, error)                // 03
    ECR(ctx context.Context, t Target) (ECRAPI, error)                  // 02
    SSM(ctx context.Context, t Target) (SSMAPI, error)                  // 04
    Credentials(ctx context.Context, t Target) (aws.CredentialsProvider, error) // 04 SigV4 on ssmmessages; 03 if needed
}
```

Each `*API` is a narrow interface listing only the SDK methods Harbormaster calls, with the SDK's own signatures (`func(ctx, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)`). Plan 01 defines `Clients`, `ECSAPI` and the real implementation; later plans add methods to the interfaces they own and extend `awsx/fake`. `func IsThrottle(err error) bool` lives in `awsx`.

### `internal/store`

`type Store struct{ Pool *pgxpool.Pool }` with methods grouped per file (`users.go`, `sessions.go`, `tags.go`, `permissions.go`, `accounts.go`, `audit.go`, `oplog.go`, `snapshots.go`, …). Every method takes `ctx context.Context` first. Not-found returns `store.ErrNotFound`. Later plans add files/methods; they do not change 01's signatures.

Pinned store API used by several plans (01 implements):

```go
func (s *Store) GetService(ctx context.Context, k model.ServiceKey) (model.Service, error)
func (s *Store) GetTask(ctx context.Context, accountID, region, taskARN string) (model.Task, error)
func (s *Store) GetTaskDefinition(ctx context.Context, accountID, region, arn string) (model.TaskDefinitionRef, error)
func (s *Store) UpsertTaskDefinition(ctx context.Context, td model.TaskDefinitionRef) error

type Operation struct {
    AccountID, Region, Cluster string
    Service     *string          // nil for standalone tasks
    ActorUserID *int64           // nil for ECS events
    Source      string           // "harbormaster" | "ecs"
    Text        string
    Details     map[string]any   // jsonb (task ARN, revision, duration, ...)
}
func (s *Store) AppendOperation(ctx context.Context, op Operation) error
func (s *Store) ListOperations(ctx context.Context, k model.ServiceKey, includeECS bool, before *time.Time, limit int) ([]OperationRow, error)
```

Tasks link to their service by `service_name` (nullable for standalone tasks): ECS `DescribeTasks` has no service ARN, only `group = "service:<name>"`.

Cross-plan hooks: `status.Service(Inputs)` has `TargetUnhealthy bool`, set from plan 03's target-health tier; the Tasks tables show reserved CPU/memory in 01 and plan 03 swaps in per-task utilisation when Container Insights enhanced data exists.

01 owns the `operation_log` table (service columns nullable) and its retention; 02, 03 and 04 only call `AppendOperation`. 01's poller only *inserts* `deployment_history` rows (outcome NULL); 02 owns outcome attribution.

### `internal/api`

- JSON everywhere. Error body: `{"error":{"code":"<snake_case>","message":"<user-facing sentence>"}}`. Codes used across plans: `unauthenticated` 401, `forbidden` 403 (admin areas, and a visible resource whose action the user lacks; message names it: "You need Deploy on Prod"), `not_found` 404 (also for resources the user cannot see at all, i.e. no View), `conflict` 409, `invalid` 422, `throttled` 503, `aws_error` 502.
- Session cookie `hm_session`; CSRF header `X-CSRF-Token` required on every non-GET; value from `GET /api/me`.
- Handlers obtain the subject with `auth.SubjectFrom(ctx) authz.Subject`.
- List responses: `{"items":[...], "next":"<cursor>|null", "polled_at":"RFC3339"}`. Detail responses include `"polled_at"`.
- `ETag` on GET responses (hash of body); `If-None-Match` → 304.
- WebSockets use `github.com/coder/websocket`; auth by session cookie + `Origin` check; first client message carries the CSRF token.

## Frontend conventions

- `web/src/app/` routes and screens (one folder per screen, mirroring the mockup's nav), `web/src/components/` shared UI (Card, Tile, Pill, TagChip, Tabs, Dialog, Toast, ConfirmDialog, Seg, EmptyState), `web/src/charts/` (LineChart, BarChart, Sparkline, Donut, HBars, Timeline: fixed-height panels with HTML labels as in the mockup), `web/src/api/` (typed client + TanStack Query hooks), `web/src/styles/tokens.css` (copy the token *values* from the mockup's `:root` blocks: colours, fonts, radii; this is design data, not Stackstead code).
- Routes: `/login`, `/`, `/clusters`, `/clusters/:clusterId`, `/services/:serviceId/:tab?` (tabs: `overview|metrics|task-definition|tasks|scaling|logs|terminal|operations`), `/tasks`, `/networks`, `/networks/:accountId/:region`, `/networks/:accountId/:region/:vpcId`, `/storage`, `/admin/users`, `/admin/tags`, `/admin/accounts`, `/admin/audit`, `/me/access`.
- Query keys: `['overview', filters]`, `['clusters', filters]`, `['cluster', id]`, `['service', id]`, `['service', id, 'tasks']`, …; live pages `refetchInterval: 5000`, `refetchIntervalInBackground: false`.
- Account/region filters live in the URL search params (`?account=&region=`) and are remembered in `localStorage` per user.
- Every screen has a component test; plan tasks name the test file and its assertions.

## Make targets (defined in plan 01, extended later)

`make build` (web + Go binary to `bin/harbormaster`), `make test` (Go + web unit), `make test-db` (start test Postgres), `make e2e` (Playwright against `harbormaster serve --fake-aws`; needs Postgres from `make test-db` / `HM_TEST_DATABASE_URL`), `make lint`, `make run` (serve with fake AWS and seeded data on `127.0.0.1:8480`).

`--fake-aws` (plan 01) makes `serve` use `awsx/fake` with an estate seeded to match the mockup (accounts `homelab-admin` and `staging-example`, clusters `core-services`, `background-jobs`, `ci-tools`, `eu-apps`, `edge-proxy`, `staging-web`, the 15 services and their states). Every phase extends the fake so `make run` always demonstrates the whole product. Plan 01's `fake.MockupEstate` and its `TestMockupEstate_Counts` are authoritative for numbers (running tasks per service, per VPC, per region); later plans' tests derive expected counts from the estate, never from the mockup screenshots (e.g. `notifications` is seeded mid-rollout with one task on :9 and one on :10).
