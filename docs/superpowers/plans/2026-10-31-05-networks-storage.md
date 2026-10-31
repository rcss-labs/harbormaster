# Harbormaster 05: Networks and Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Poll VPCs, subnets, edge components and EFS every 5 minutes, and show them as the Networks drill-down (account → region → VPC tree) and the Storage page, filtered by the signed-in user's permissions.

**Architecture:** Two new poller tiers (`network`, `storage`) copy EC2 and EFS descriptions into cache tables; the request path never calls EC2/EFS. `internal/network` assembles the VPC tree from those tables plus plan 01's task snapshots (task → subnet via the ENI attachment), `internal/storage` maps task definitions' `efsVolumeConfiguration` + `mountPoints` to file systems; EFS charts come from plan 03's `internal/metrics` (CloudWatch `AWS/EFS`, 60 s shared cache). The React side adds three Networks screens with a left-to-right ARIA tree and the Storage page.

**Tech Stack:** Go 1.26, AWS SDK v2 (`ec2`, `efs`), pgx v5, `net/http` ServeMux; React 19 + TypeScript, TanStack Query v5, CSS modules, Vitest + Testing Library, Playwright.

**Spec:** `docs/design/2026-10-31-harbormaster-design.md` (§3 rules 5–7, §5.1 Network/Storage rows, §6.0, §6.6, §6.7, §11.2, §12, §13, §14, §17 phase 5). Conventions: `docs/superpowers/plans/2026-10-31-00-conventions.md`. Mockup: `docs/design/mockup/index.html` (`vNet` l.1474, `netRegion` l.1495, `netVpc` l.1507, `vStorage` l.1546, `efsUsers`/`visibleEfs` l.762–763, `.tree` CSS l.359–387, `.net-card` l.261–266); screenshots `docs/design/screens/network-account.png`, `network-region.png`, `network-tree.png`, `storage.png`.

## Global Constraints

Every line of plan 00 "Global constraints" applies. Phase-5 specifics, verbatim from the spec:

- Network loop: **5 min**, calls `DescribeVpcs`, `DescribeSubnets`, `DescribeNatGateways`, `DescribeInternetGateways`, `DescribeVpcEndpoints`, `DescribeSecurityGroups` (only those used by tasks); stored as `vpcs`, `subnets`, … (§5.1).
- Storage loop: **5 min**, calls `DescribeFileSystems`, `DescribeAccessPoints`, `DescribeMountTargets`; EFS `StorageBytes`, `ClientConnections`, `MeteredIOBytes` via `GetMetricData`; stored as `efs`, `efs_access_points` (§5.1). Metrics are on demand through plan 03's 60 s `metrics_cache` (§5.1 Metrics row, §12), never polled.
- Only the IAM actions in §11.2 may be called: `ec2:DescribeVpcs`, `ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups`, `ec2:DescribeNatGateways`, `ec2:DescribeInternetGateways`, `ec2:DescribeVpcEndpoints`, `elasticfilesystem:DescribeFileSystems`, `elasticfilesystem:DescribeAccessPoints`, `elasticfilesystem:DescribeMountTargets`, `cloudwatch:GetMetricData`. No `DescribeRouteTables` (see "Decisions").
- Visibility (§3): a service is visible only with View on its effective tag (rule 4); standalone tasks use the cluster's tag (rule 7); a VPC/subnet node is visible when it contains a task of a visible service, an EFS file system when a visible service mounts it (rule 6); "Not mounted" file systems are shown to admins only (§6.7). Invisible resources return `not_found` 404 (§15).
- A VPC is **active** when at least one visible task runs in it; regions without active VPCs are dimmed (§6.6). The tree starts collapsed to services when a VPC has more than **150** visible tasks (§6.0).
- Tree order: VPC ▸ availability zone ▸ subnet ▸ cluster ▸ service ▸ task; every node with children can collapse; buttons **"Expand all"** and **"Collapse to services"**; subnets with no visible tasks show **"No running tasks"**; the tree scrolls horizontally; clicking a service or task node opens it (§6.6). Header lists internet gateway, NAT gateways and VPC endpoints `ecr.api`, `ecr.dkr`, `logs`, `ssmmessages` (§6.6).
- Storage (§6.7): stored data by file system, mounted vs not mounted, per file system: name, ID, region, VPC; size over 30 days (`StorageBytes`) and growth; throughput over 24 h and mode; client connections; access points (ID, path, POSIX UID); services that mount it with container path and task count; flag **"Not mounted"**.
- Exact UI copy from the mockup: "Account level: regions with active VPCs. A VPC is active when ECS tasks run in it.", "No active VPCs", "{n} VPC without tasks hidden", "Active VPCs in {account}", "Network tree", "VPC ▸ availability zone ▸ subnet ▸ cluster ▸ service ▸ task", "Standalone tasks", "EFS file systems and the services that mount them. File systems are created by Terraform.", "Stored data by file system", "{size} in total", "Mounted or not", "Mounted by a service", "Not mounted, still billed", "Size, 30 days", "Throughput, 24 h", "Access points", "None", "Mounted by", "No service mounts this file system.", "{n} client(s)", "Network tree →".
- Keyboard: the tree is usable with the keyboard, ARIA roles on tree/items, visible focus, reduced motion respected (§14.1).

## Interfaces taken from plans 01 and 03 (verified against `2026-10-31-01-foundation.md` and the final `2026-10-31-03-observe.md`; use exactly these names)

**Plan 01 (`2026-10-31-01-foundation.md`; task numbers are 01's)**
- `model.Task{ Cluster ClusterKey; ARN string; ServiceName string /* "" = standalone */; Group, StartedBy string; TaskDefinitionARN, Family string; Revision int; LastStatus, DesiredStatus, HealthStatus string; LaunchType, CapacityProvider string; AvailabilityZone, SubnetID, PrivateIP, PublicIP, ENIID string; StartedAt *time.Time; … }` with `ShortID()` (full last ARN segment; UI truncates). Account and region live in `Cluster` (`model.ClusterKey{AccountID, Region, Cluster}`); the service key is `model.ServiceKey{ClusterKey: t.Cluster, Service: t.ServiceName}`. `SubnetID`/`PrivateIP` come from `attachments[type=ElasticNetworkInterface].details` and are empty for bridge/host network mode.
- `model.Service{ Key ServiceKey; ARN, Status string /* AWS status */; Desired, Running, Pending int; TaskDefinitionARN, Family string; Revision int; Subnets, SecurityGroups []string; AssignPublicIP bool; … }`. The derived status is `status.Service(status.Inputs{Service, RunningTasks}) model.Status`.
- `model.TaskDefinitionRef{ AccountID, Region, ARN, Family string; Revision int; …; Containers []ContainerDef; EFSVolumes []EFSVolume; Raw json.RawMessage }`, `ContainerDef{ Name, Image string; Essential bool; …; MountPoints []MountPoint }`, `MountPoint{ SourceVolume, ContainerPath string; ReadOnly bool }`, `EFSVolume{ Name, FileSystemID, AccessPointID string }`.
- Store: `TargetFilter{AccountID, Region string}` (empty = all), `Visibility{Admin bool; TagIDs []int64}` applied **in SQL** by every read; `ListClusters(ctx, f TargetFilter, vis Visibility) ([]ClusterRow{model.Cluster; TagID authz.TagID; TagSet bool}, error)`, `ListServices(ctx, f TargetFilter, cluster string, vis Visibility) ([]ServiceRow{model.Service; EffectiveTagID; TagInherited}, error)`, `RunningTasks(ctx, f TargetFilter, vis Visibility) ([]TaskRow{model.Task; EffectiveTagID; …}, error)` (standalone tasks visible by the cluster's tag), `GetTaskDefinition(ctx, acct, region, arn string) (model.TaskDefinitionRef, error)`, `ListTags(ctx) ([]Tag{ID authz.TagID; Name, Color string; Builtin bool}, error)`, `DevTag(ctx) (Tag, error)`, `ListAccounts(ctx) ([]AWSAccount{ID int64; Name, AccountID string; Regions []string; Enabled bool; …}, error)`, `UpsertServices`/`UpsertTasks(ctx, rows) (WriteResult, error)`, `ContentHash(v any) string`, `ErrNotFound`; test helper `storetest.Open(t) *store.Store`. Migrations: 01 uses `0001`, `0002`; 03 uses `0030`; this plan uses **`0050`**.
- Poller: `Loop{ target, ecs, store, cfg, tiers map[string]*tier }` in `internal/poller/loop.go`; tiers are named entries (`clusters`, `serviceList`, `services`, `runningTasks`, `fullTasks`, `stoppedTasks`) with `tier{name; base, cur; clean}` back-off; intervals come from `poller.Intervals` via `poller.ConfigFrom(config.Config)`; the loop records throttle/failure status per tier run. 01 has no exported tier type; this plan adds tiers `network` and `storage` the same way (Task 3), with `Intervals.Network`/`Intervals.Storage` fed from new `config.Config` fields `Poll.Network`/`Poll.Storage` (env `HM_POLL_NETWORK`, `HM_POLL_STORAGE`, 01's `Poll.*`/`HM_POLL_*` naming, 01 Task 1). The `Loop` gets its EC2/EFS clients from `Clients.EC2/EFS(ctx, target)` (00 `awsx.Clients`).
- API (01 Task 17): `api.Server{Store, Auth, Setup, Clients, Prober, Cfg, Log, Now}` built by `api.New(s Server)` (03 adds `Metrics *metrics.Client`), routes registered in `(s *Server) routes(mux)` in `internal/api/server.go`; `writeJSON(w, status int, v any)`, `writeError(w, status int, code, msg string)`, `s.tags(r)`, `(s *Server) scopeFrom(r) (scope{Subject authz.Subject; Vis store.Visibility; Filter store.TargetFilter; Tags authz.Tags}, error)`; DTO structs in `internal/api/dto.go` with snake_case JSON, mirrored in `web/src/api/types.ts`; `TagDTO{ID int64; Name, Color string; Inherited bool}`; `ServiceDTO.Networking struct{ Subnets []struct{ID string; Tasks int}; SecurityGroups []string; AssignPublicIP bool; LoadBalancers … }`; `ClusterCardDTO{ ID, Name string; Tag TagDTO; AccountID, AccountName, Region string; … }` embedded in `ClusterDTO`; errors map `store.ErrNotFound → 404 not_found`, `awsx.IsThrottle → 503 throttled`. Test harness `internal/api/apitest_test.go`: `newTestServer(t) (*httptest.Server, *fake.Fake, *store.Store)`, `login(t, srv, username, "harbormaster-e2e-1") *http.Client`, `pollOnce(t, srv)`; devseed users `pavel` (admin), `dana` (Dev Full, Staging Deployer, Prod View|Logs), `omer` (Dev Operator, Staging View), `oncall` (Staging and Prod View|Logs|Operate|Terminal); cluster tags `core-services → Prod`, `staging-web → Staging`, the rest Dev.
- awsx (01 Task 11): narrow interfaces live in `internal/awsx/apis.go`, where 01 declares empty placeholders `EC2API interface{}` and `EFSAPI interface{}`; real clients go through `(*Real).configFor(ctx, t)` in `clients.go`.
- Fake (01 Task 12): `fake.Estate` (SDK `types.*` values, mutex-guarded, `MockupEstate(now)`), `fake.New(e) *fake.Fake` (`Fake{Estate *Estate; Throttle int32}` — `Throttle` covers ECS only) implementing `awsx.Clients` (`EC2`/`EFS` return `nil, nil` until this plan), `Calls() map[string]int`. Seeded standalone tasks: `ci-tools` RUNNING `release-build:3` (group `family:release-build`, startedBy `events-rule/nightly-release`); `background-jobs` STOPPED `db-migrate:12`. Task definitions already carry the EFS volume and the main container's mount point where the mockup sets `efs` (`buildTd`). Running tasks have private IPs "inside the mockup's private subnets" but 01 does not pin their `subnetId`; Task 2 does.
- Frontend (01 Tasks 24–32): `web/src/api/client.ts` `api.get<T>(path)` (over `apiFetch`) and `class ApiError { code; status }`; `web/src/api/hooks.ts`, `web/src/api/types.ts`; `web/src/app/router.tsx`; `web/src/app/shell/useFilters.ts` (`useFilters()` → `{account, region}`), `useCrumbs([...])`; `web/src/app/shell/Sidebar.tsx` renders "Networks" and "Storage" links (Storage disabled with title "Phase 5" until this plan, Task 12 enables it); 01 has no `PageHead` or `StatusPill` component: page heads are the `h1` + `.meta` markup 01's screens use, status pills are `<Pill kind={statusKind(s)}>{statusLabel(s)}</Pill>`; components `Card { title; right?; children }`, `Pill { kind: 'ok'|'info'|'warn'|'bad'|'mute'; children }`, `TagChip { tag: TagDTO; onClick? }`, `EmptyState { children }`, `KV`; `web/src/components/status.ts` `statusKind(s)`, `statusLabel(s)`, `SERIES`; charts `Sparkline { values; color?; height?; fluid? }`, `Donut { segments: {name, v, color}[]; label; sub }`, `HBars { items: {name, href?, sub?, v?, parts?, ghost?, label?, color?}[]; max?; unit? }`, `web/src/charts/format.ts` `fmtNum`; screens `web/src/app/services/OverviewTab.tsx` (Networking `KV` whose VPC row says "—" until this plan; test `ServicePage.test.tsx`), `web/src/app/clusters/{Cluster,ClusterCard}.tsx` (test `Cluster.test.tsx`); tests `renderWithProviders(ui, { route, me })` with MSW `server.use(http.get(...))`; e2e (01 Task 36) `web/e2e/helpers.ts` `login(page, username, password='harbormaster-e2e-1')`, `make e2e` (builds, then Playwright against `serve --fake-aws` on `127.0.0.1:8481`).

**Plan 03 (`2026-10-31-03-observe.md`, final; Task numbers are 03's)**
- `internal/metrics/query.go` + `client.go` (03 Task 3): `Dim{Name, Value string}`; `Query{ID /* ^[a-z][a-zA-Z0-9_]*$, unique per Fetch */, Namespace, MetricName string; Dimensions []Dim; Stat string; Period int /* seconds, multiple of 60 */; Label string}`; `type Span time.Duration` (constants `Span1h…Span72h`, `ParseRange(s) (Span, bool)` for UI strings); `Result{Start, End time.Time; Period int; Timestamps []time.Time; Series []Series{ID, Label string; Values []*float64}; FetchedAt time.Time; Stale bool; StaleReason string}`; `(*Result).Points(id) []Point{T time.Time; V *float64}` (nil = no datapoint); `(*metrics.Client).Fetch(ctx, t awsx.Target, span Span, queries []Query) (*Result, error)` — 60 s `metrics_cache`, ≤ 500 queries per call; all queries of one Fetch must share one `Period` (`metrics.ErrMixedPeriods`), so mixed periods need separate Fetch calls; End/Start are computed by Fetch (End = now floored to the period). `api.Server.Metrics *metrics.Client`.
- `internal/awsx/fake/cloudwatch.go` (03 Task 1): fake `GetMetricData` synthesizing deterministic series per namespace/metric from `seed_observe.go` profiles; Task 6 adds the `AWS/EFS` branch next to the `AWS/ApplicationELB` one.

## Decisions (stated so the implementer does not re-decide)

1. **Public/private subnet** = `MapPublicIpOnLaunch` (§11.2 grants no `DescribeRouteTables`). Label text: "Public subnet" / "Private subnet".
2. **IP usage:** `total = 2^(32−prefix) − 5`, `used = total − AvailableIpAddressCount`, both clamped to `[0, total]`. The mockup adds the tasks here on top of `free`; real `AvailableIpAddressCount` already counts task ENIs, so it is not added.
3. **Tasks with no subnet** (EC2 bridge/host mode, or an awsvpc task whose attachment is gone): shown under a synthetic node `kind: "nosubnet"`, label "No subnet (bridge or host network mode)", a sibling of the AZ nodes under the VPC, containing cluster ▸ service ▸ task like a subnet. A cluster belongs to a VPC when at least one of its visible running tasks has a subnet in that VPC; a cluster with no awsvpc task anywhere cannot be placed and is counted as `unplaced_tasks` (account page note "{n} tasks without a subnet not shown"). Tasks whose subnet ID is not in the `subnets` table yet (created < 5 min ago) are also `unplaced_tasks`.
4. **Standalone tasks:** one node `kind: "standalone"`, label "Standalone tasks", last child of the cluster node; its children are task nodes carrying `group` (`model.Task.Group`, else `StartedBy`), rendered as a muted suffix. In the fake estate this is `release-build` under `ci-tools` (shared-services VPC).
5. **Region card "clusters"** = distinct clusters that have a visible running task in an active VPC of that region (consistent with the tasks count).
6. **`/api/vpcs/{vpcId}/tree` needs `?account=&region=`** (VPC IDs are not guaranteed unique across accounts/regions); missing → 422 `invalid`.
7. **Security groups** polled = union of `model.Service.SecurityGroups` over the account×region (standalone tasks' groups are not available without `DescribeNetworkInterfaces`), fetched with filter `group-id` so a deleted group does not fail the call.
8. **EFS → VPC** = `VpcId` of the file system's first mount target; `mount_target_azs` lists all. Client connections = CloudWatch `ClientConnections` (latest point) when present, else the running task count of mounting services (`mounting_tasks`), which the UI shows as the pill.
9. **Storage visibility** counts services only (rule 6 wording); standalone tasks mounting EFS do not make a file system visible.
10. **Cache-table writes** are full replace per account×region (upsert all + delete vanished) rather than hash-diffed: ≤ hundreds of rows every 5 min, unlike the 5 s task tier.
11. **Refetch:** networks pages 5 000 ms (tasks move); storage 60 000 ms (metrics page, spec §14 "60 s on metrics").
12. Throughput series period **20 min** (72 points over 24 h, as in the mockup); size series period **1 day** (30 points); connections period **1 min** over the last 10 min. CloudWatch query IDs are index-based (`sz_0`, `io_0`, `cc_0`) because file system IDs contain `-`, which GetMetricData IDs reject.

## Review Focus

1. A subnet whose `AvailableIpAddressCount` exceeds the usable size (stale count, IPv6-only subnet without `CidrBlock`, a `/28`) must never show a negative or > 100 % IP bar → Task 5 `TestSubnetIPs` clamp rows.
2. CloudWatch throttled or returning nothing while the Storage page is open → file systems still listed, each card says "Metrics unavailable" instead of empty charts → Task 8 `TestStorage_MetricsFailureStillListsFileSystems`, Task 14 "shows Metrics unavailable".
3. A running awsvpc task whose ENI attachment is missing (just transitioned, or DELETED) → placed under "No subnet …", never silently dropped; counts stay consistent → Task 5 `TestAssemble_NoSubnetTasksUnderNosubnetNode`.
4. VPCs, subnets and file systems without a `Name` tag (default VPC, un-tagged Terraform) → labels fall back to the AWS ID → Task 3 `TestNameTag_FallsBackToID`, Task 4 `TestNormalizeFileSystem_NameFallsBackToID`.
5. The last visible task in a VPC stops while its tree page is open → next refetch gets 404; the page shows an empty state "No running tasks you can see in this VPC." with a link back to the region, not a blank page → Task 7 `TestVPCTree_InvisibleVPCIs404`, Task 13 "shows empty state on 404".

---

## File Structure

```
internal/model/network.go                 VPC, Subnet, VPCEdge, SecurityGroup           (Task 1)
internal/model/storage.go                 FileSystem, AccessPoint                        (Task 1)
internal/store/migrations/0050_networks_storage.up.sql                                   (Task 1)
internal/store/network.go, network_test.go   ReplaceNetwork, ListVPCs, GetVPC, ListSubnets, ListVPCEdges, ListSecurityGroups
internal/store/storage.go, storage_test.go   ReplaceStorage, ListFileSystems, ListAccessPoints
internal/awsx/apis.go, clients.go         (01) fill EC2API, EFSAPI placeholders; Real.EC2/EFS (Task 2)
internal/awsx/fake/ec2.go, efs.go, estate_network.go, *_test.go   fake estate            (Task 2)
internal/config/config.go                 + Poll.Network, Poll.Storage (HM_POLL_NETWORK, HM_POLL_STORAGE) (Task 3)
internal/poller/network.go, network_test.go   network tier, EC2 normalization            (Task 3)
internal/poller/storage.go, storage_test.go   storage tier, EFS normalization            (Task 4)
internal/network/tree.go, ips.go, summary.go, *_test.go                                  (Task 5)
internal/storage/mounts.go, visible.go, format.go, *_test.go                             (Task 6)
internal/metrics/efs.go, efs_test.go      EFS query builders, spans and periods          (Task 6)
internal/awsx/fake/cloudwatch_efs.go      AWS/EFS branch of 03's fake GetMetricData       (Task 6)
internal/api/networks.go, networks_test.go   3 handlers; dto.go additions; routes in server.go (Task 7)
internal/api/storage.go, storage_test.go     storage handler; services.go/clusters.go vpc enrichment (Task 8)
web/src/api/networks.ts, storage.ts       types + query hooks                            (Task 9)
web/src/app/networks/NetCard.tsx, NetworksPage.tsx, NetworkRegionPage.tsx (+ tests)     (Task 10)
web/src/app/networks/tree/Tree.tsx, TreeNode.tsx, NodeBody.tsx, useTreeState.ts, useTreeKeyboard.ts, tree.module.css, Tree.test.tsx (Task 11)
web/src/app/networks/NetworkVpcPage.tsx (+ test), web/src/app/router.tsx, web/src/app/shell/Sidebar.tsx (Task 12)
web/src/app/storage/StoragePage.tsx, EfsCard.tsx, format.ts (+ tests)                   (Task 13)
web/src/app/services/OverviewTab.tsx, web/src/app/clusters/Cluster.tsx, web/src/api/types.ts (modify) (Task 14)
web/e2e/networks.spec.ts, web/e2e/storage.spec.ts                                        (Task 15)
```

---

### Task 1: Model types, migration and store repositories — *Implementer: Haiku*

**Files:**
- Create: `internal/model/network.go`, `internal/model/storage.go`
- Create: `internal/store/migrations/0050_networks_storage.up.sql`
- Create: `internal/store/network.go`, `internal/store/storage.go`
- Test: `internal/store/network_test.go`, `internal/store/storage_test.go` (DB tests via `storetest.Open(t)`, skip without `HM_TEST_DATABASE_URL`)

**Interfaces:**
- Consumes: `store.Store`, `store.ErrNotFound`, `storetest.Open`.
- Produces (`internal/model`):

```go
type VPC struct { AccountID, Region, VPCID, Name, CIDR string; IsDefault bool; Raw json.RawMessage; PolledAt time.Time }
type Subnet struct { AccountID, Region, SubnetID, VPCID, Name, CIDR, AZ string; AvailableIPs int; Public bool; Raw json.RawMessage; PolledAt time.Time }
type VPCEdgeKind string // "igw" | "nat" | "endpoint"
type VPCEdge struct { AccountID, Region, VPCID string; Kind VPCEdgeKind; ResourceID, SubnetID, Detail string; Raw json.RawMessage; PolledAt time.Time }
type SecurityGroup struct { AccountID, Region, GroupID, VPCID, Name string; Raw json.RawMessage; PolledAt time.Time }
type FileSystem struct { AccountID, Region, FileSystemID, ARN, Name string; SizeBytes int64; ThroughputMode string; ProvisionedMiBps *float64; PerformanceMode, LifecycleState string; Encrypted bool; VPCID string; MountTargetAZs []string; Raw json.RawMessage; PolledAt time.Time }
type AccessPoint struct { AccountID, Region, AccessPointID, FileSystemID, Name, RootPath string; PosixUID, PosixGID *int64; Raw json.RawMessage; PolledAt time.Time }
```

- Produces (`internal/store`), every method `ctx` first; `""` for `accountID`/`region` means "all":

```go
func (s *Store) ReplaceNetwork(ctx, accountID, region string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error
func (s *Store) ListVPCs(ctx, accountID, region string) ([]model.VPC, error)
func (s *Store) GetVPC(ctx, accountID, region, vpcID string) (model.VPC, error)      // ErrNotFound
func (s *Store) ListSubnets(ctx, accountID, region string) ([]model.Subnet, error)
func (s *Store) ListVPCEdges(ctx, accountID, region, vpcID string) ([]model.VPCEdge, error)
func (s *Store) ListSecurityGroups(ctx, accountID, region string, ids []string) ([]model.SecurityGroup, error)
func (s *Store) ReplaceStorage(ctx, accountID, region string, fs []model.FileSystem, aps []model.AccessPoint) error
func (s *Store) ListFileSystems(ctx, accountID, region string) ([]model.FileSystem, error)
func (s *Store) ListAccessPoints(ctx, accountID, region string) ([]model.AccessPoint, error)
```

Tables (migration), all `account_id text, region text, … , raw jsonb not null, polled_at timestamptz not null`:

| table | columns besides the shared ones | PK / index |
|---|---|---|
| `vpcs` | `vpc_id, name, cidr, is_default bool` | PK `(account_id, region, vpc_id)` |
| `subnets` | `subnet_id, vpc_id, name, cidr, az, available_ips int, public bool` | PK `(account_id, region, subnet_id)`; index `(account_id, region, vpc_id)` |
| `vpc_edges` | `vpc_id, kind text check (kind in ('igw','nat','endpoint')), resource_id, subnet_id text null, detail text` | PK `(account_id, region, resource_id)`; index `(account_id, region, vpc_id)` |
| `security_groups` | `group_id, vpc_id, name` | PK `(account_id, region, group_id)` |
| `efs` | `file_system_id, arn, name, size_bytes bigint, throughput_mode, provisioned_mibps numeric null, performance_mode, lifecycle_state, encrypted bool, vpc_id text, mount_target_azs text[]` | PK `(account_id, region, file_system_id)` |
| `efs_access_points` | `access_point_id, file_system_id, name, root_path, posix_uid bigint null, posix_gid bigint null` | PK `(account_id, region, access_point_id)`; index `(account_id, region, file_system_id)` |

`Replace*` runs in one transaction: upsert every row (`ON CONFLICT … DO UPDATE` all columns), then `DELETE … WHERE account_id=$1 AND region=$2 AND NOT (<id> = ANY($3))` per table.

- [ ] **Step 1: Write the failing store tests**

`internal/store/network_test.go`:
- `TestReplaceNetwork_UpsertsAndDeletesVanished`: replace with subnets A, B; replace again with A (name changed) and C → `ListSubnets` returns exactly A (new name) and C.
- `TestReplaceNetwork_IsScopedToAccountRegion`: rows of `("1", "eu-west-1")` survive a replace for `("1", "us-east-1")`.
- `TestGetVPC_NotFound`: `errors.Is(err, store.ErrNotFound)`.
- `TestListSubnets_WildcardAccountRegion`: `ListSubnets(ctx, "", "")` returns rows from two account×regions; `ListVPCEdges` filtered by vpc.
- `TestListSecurityGroups_ByIDs`: only requested ids come back.

`internal/store/storage_test.go`:
- `TestReplaceStorage_UpsertsFileSystemsAndAccessPoints`: 2 file systems + 1 access point; `MountTargetAZs` round-trips as `[]string`; `ProvisionedMiBps` nil round-trips as nil; second replace without the access point deletes it.

- [ ] **Step 2: Run to verify they fail**

Run: `HM_TEST_DATABASE_URL=… go test ./internal/store/ -run 'ReplaceNetwork|GetVPC_NotFound|ListSubnets_Wildcard|ListSecurityGroups|ReplaceStorage' -v`
Expected: FAIL, undefined `ReplaceNetwork` / `model.Subnet`.

- [ ] **Step 3: Add the model types and the migration file (column list above)**

- [ ] **Step 4: Implement `internal/store/network.go` and `internal/store/storage.go`** with the signatures above (pgx batch for upserts; `pgx.Tx`).

- [ ] **Step 5: Run the tests; expect PASS. Run `go vet ./...` and `gofmt -l cmd internal` (empty).**

- [ ] **Step 6: Commit**

```bash
git add internal/model/network.go internal/model/storage.go internal/store/migrations/0050_networks_storage.up.sql internal/store/network.go internal/store/storage.go internal/store/network_test.go internal/store/storage_test.go
git commit -m "feat(store): cache tables for VPCs, subnets, edges, security groups and EFS"
```

---

### Task 2: `awsx` EC2/EFS interfaces and the fake network/storage estate — *Implementer: Haiku*

**Files:**
- Modify: `internal/awsx/apis.go` (fill 01's `EC2API` / `EFSAPI` placeholders), `internal/awsx/clients.go` (`(*Real).EC2` / `(*Real).EFS` through `configFor`, same pattern as `ECS`), `internal/awsx/fake/fake.go` (`(*Fake).EC2` / `(*Fake).EFS` return target-scoped clients; `ThrottleEC2`, `ThrottleEFS int32` knobs)
- Create: `internal/awsx/fake/ec2.go`, `internal/awsx/fake/efs.go`, `internal/awsx/fake/estate_network.go`
- Modify: `internal/awsx/fake/estate.go` (task subnet placement, standalone task, task-definition EFS volumes)
- Test: `internal/awsx/fake/ec2_test.go`, `internal/awsx/fake/efs_test.go`, `internal/awsx/fake/estate_network_test.go`

**Interfaces:**
- Consumes: `awsx.Clients`, `awsx.Target`, `fake.Estate` (01), `awsx.IsThrottle`.
- Produces:

```go
// internal/awsx/apis.go (01's placeholder, filled)
type EC2API interface {
    DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
    DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
    DescribeNatGateways(ctx context.Context, in *ec2.DescribeNatGatewaysInput, opts ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
    DescribeInternetGateways(ctx context.Context, in *ec2.DescribeInternetGatewaysInput, opts ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error)
    DescribeVpcEndpoints(ctx context.Context, in *ec2.DescribeVpcEndpointsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error)
    DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}
// internal/awsx/apis.go (01's placeholder, filled)
type EFSAPI interface {
    DescribeFileSystems(ctx context.Context, in *efs.DescribeFileSystemsInput, opts ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error)
    DescribeAccessPoints(ctx context.Context, in *efs.DescribeAccessPointsInput, opts ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error)
    DescribeMountTargets(ctx context.Context, in *efs.DescribeMountTargetsInput, opts ...func(*efs.Options)) (*efs.DescribeMountTargetsOutput, error)
}
```

Fake: `*fake.EC2` and `*fake.EFS` implement these from `fake.Estate`; honour filters `vpc-id`, `attachment.vpc-id`, `group-id`, pagination (`NextToken` for EC2 with page size 2 when `MaxResults` set; `Marker`/`NextMarker` for EFS with page size 2), and `Fake.ThrottleEC2` / `Fake.ThrottleEFS` (next N calls of that service return a throttling error, like 01's ECS-only `Fake.Throttle`; `awsx.IsThrottle` true); calls count in `Calls()`.

Seed data (`estate_network.go`), copied from the mockup `VPCS`/`EFS` (l.529–563):

| account | region | VPC name · id · CIDR | default | NAT | subnets (name · id · CIDR · AZ · type · mockup `free`) |
|---|---|---|---|---|---|
| homelab-admin | us-east-1 | homelab-main · vpc-0a41c7e29b3d · 10.20.0.0/16 | no | yes | private-1a · subnet-03f1a9c2 · 10.20.1.0/24 · us-east-1a · Private · 231; private-1b · subnet-0b77e410 · 10.20.2.0/24 · us-east-1b · Private · 236; public-1a · subnet-09c2d5ee · 10.20.101.0/24 · us-east-1a · Public · 248 |
| homelab-admin | us-east-1 | shared-services · vpc-07de19a4c011 · 10.21.0.0/16 | no | yes | tools-1a · subnet-0e1c88a1 · 10.21.1.0/24 · us-east-1a · Private · 247; tools-1c · subnet-02aa71f3 · 10.21.3.0/24 · us-east-1c · Private · 249 |
| homelab-admin | us-east-1 | legacy-default · vpc-5c1e0f2a · 172.31.0.0/16 | yes | no | default-1a · subnet-6f0a1c3e · 172.31.0.0/20 · us-east-1a · Public · 4091 |
| homelab-admin | eu-west-1 | eu-apps · vpc-0f3392bb71e2 · 10.30.0.0/16 | no | yes | private-1a · subnet-0a90f7c1 · 10.30.1.0/24 · eu-west-1a · Private · 240; private-1b · subnet-0c6b1e92 · 10.30.2.0/24 · eu-west-1b · Private · 242 |
| homelab-admin | eu-central-1 | edge · vpc-03b1d7f0c5a8 · 10.40.0.0/16 | no | yes | private-1a · subnet-0d2e5a77 · 10.40.1.0/24 · eu-central-1a · Private · 250; public-1a · subnet-0f61c3b8 · 10.40.101.0/24 · eu-central-1a · Public · 248 |
| staging-example | us-east-1 | staging · vpc-0b18e6cd2a91 · 10.50.0.0/16 | no | yes | private-a · subnet-01f8a2d4 · 10.50.1.0/24 · us-east-1a · Private · 249; private-b · subnet-0e7c3b19 · 10.50.2.0/24 · us-east-1b · Private · 250 |

Every non-default VPC has one internet gateway (`igw-` + 8 hex) and, when NAT = yes, one NAT gateway (`nat-` + 17 hex) in its first public (else first) subnet; `homelab-main` additionally has VPC endpoints `com.amazonaws.us-east-1.ecr.api`, `.ecr.dkr`, `.logs`, `.ssmmessages` plus one `.s3` (gateway) that must be filtered out. `AvailableIpAddressCount` = mockup `free` − number of seeded running tasks in that subnet (so the tree shows the screenshot's "27 of 251 IPs used" for private-1a). Security groups: one per service `sg-<8 hex>` named after the service, plus `alb-ingress` for load-balanced ones (mockup l.1220).

EFS (`DescribeFileSystems` fields): jenkins-home · fs-0c91a4e7 · shared-services · 38.4 GiB · elastic · generalPurpose · AP `fsap-0a1b2c3d4e` `/jenkins` uid 1000; reports-data · fs-02e7d1c9 · homelab-main · 12.1 GiB · bursting · AP `fsap-07f3e9a1c2` `/reports` uid 1001; shared-certs · fs-0a6b33f0 · edge · 0.02 GiB · bursting · AP `fsap-0e44d18b7a` `/certs` uid 101; search-index · fs-0d18c2ab · eu-apps · 74.9 GiB · provisioned 64 MiB/s · AP `fsap-03c7a9e5d1` `/index` uid 1000; uploads-archive · fs-07a2b9e1 · homelab-main · 211.6 GiB · elastic · no access points. One mount target per private subnet of the VPC.

Estate modifications (01's fake): (a) awsvpc tasks are placed round-robin over the cluster VPC's subnets of the right type (public for `edge-nginx`, private otherwise), with `PrivateIP` inside the subnet CIDR, as mockup `subnetsOf`/`ipIn` (l.636–644); (b) 01's standalone RUNNING task `release-build:3` in `ci-tools` is placed in a `shared-services` private subnet (no new standalone task is added); (c) 01 already seeds the EFS volumes and mount points of `report-worker` (`/data/reports`), `jenkins-controller` (`/var/jenkins_home`), `search-indexer` (`/index`), `edge-nginx` (`/etc/nginx/certs`), `cert-renewer` (`/certs`); this task only makes each `efsVolumeConfiguration` carry `fileSystemId` and `authorizationConfig{accessPointId, iam: ENABLED}` matching the file systems and access points below, `transitEncryption: ENABLED` (mockup l.613, 629–632).

- [ ] **Step 1: Write the failing fake tests**

- `TestFakeEC2_DescribeSubnetsFiltersByVpc`: filter `vpc-id=vpc-0a41c7e29b3d` → 3 subnets.
- `TestFakeEC2_DescribeVpcEndpointsIncludesS3Decoy`: homelab-main has 5 endpoints.
- `TestFakeEC2_ThrottleKnob`: `ThrottleEC2=1` → first call `awsx.IsThrottle(err)`, second ok.
- `TestFakeEFS_DescribeFileSystemsPaginates`: homelab-admin/us-east-1 returns 3 file systems across two pages via `NextMarker`.
- `TestFakeEFS_DescribeMountTargetsByFileSystem`: jenkins-home → 2 mount targets with `VpcId=vpc-07de19a4c011`.
- `TestFakeEstate_TasksUseSeededSubnets`: every RUNNING awsvpc task's `SubnetID` exists in the fake subnets of its account×region and its `PrivateIP` is inside that subnet's CIDR.
- `TestFakeEstate_HasStandaloneTask`: at least one running task with empty `ServiceName` and non-empty `Group`.
- `TestFakeEstate_TaskDefinitionsMountEFS`: the five services above have exactly one `efsVolumeConfiguration` with the listed file system id and container path.
- Compile-time: `var _ awsx.EC2API = (*ec2.Client)(nil)`, `var _ awsx.EFSAPI = (*efs.Client)(nil)`, `var _ awsx.EC2API = (*fake.EC2)(nil)`, `var _ awsx.EFSAPI = (*fake.EFS)(nil)`.

- [ ] **Step 2: Run `go test ./internal/awsx/... -run 'Fake(EC2|EFS|Estate)' -v`; expect FAIL (undefined).**

- [ ] **Step 3: Implement the interfaces, real constructors (`ec2.NewFromConfig` / `efs.NewFromConfig` on the cached per-target `aws.Config`), fake clients and seed.**

- [ ] **Step 4: Run the tests; expect PASS. Run 01's whole fake/estate suite too (`go test ./internal/awsx/...`), since task placement changed.**

- [ ] **Step 5: Commit**

```bash
git add internal/awsx
git commit -m "feat(awsx): EC2 and EFS interfaces with a fake estate matching the mockup"
```

---

### Task 3: Network poller tier — *Implementer: Sonnet*

**Files:**
- Modify: `internal/config/config.go` (+ `Poll.Network`, `Poll.Storage time.Duration`, env `HM_POLL_NETWORK`, `HM_POLL_STORAGE`, default `5m`, parse errors reported like 01's other durations), `internal/poller/poller.go` (`Intervals.Network`, `Intervals.Storage` in `ConfigFrom`)
- Create: `internal/poller/network.go`
- Modify: `internal/poller/tiers.go` (add the `network` entry to the Loop's tier table)
- Test: `internal/poller/network_test.go`, `internal/config/config_test.go` (add cases)

**Interfaces:**
- Consumes: `awsx.EC2API`, `fake.EC2`, `store.ReplaceNetwork`, `store.ListServices` (for security group IDs, called with `store.Visibility{Admin: true}`), 01's unexported `tier` back-off.
- Produces:

```go
type networkStore interface {
    ListServices(ctx context.Context, f store.TargetFilter, cluster string, vis store.Visibility) ([]store.ServiceRow, error)   // 01's signature
    ReplaceNetwork(ctx context.Context, accountID, region string, vpcs []model.VPC, subnets []model.Subnet, edges []model.VPCEdge, sgs []model.SecurityGroup) error
}
// tier table entry: name "network", base = cfg.Intervals.Network, work = pollNetwork
func pollNetwork(ctx context.Context, t awsx.Target, ec2c awsx.EC2API, st networkStore, now time.Time) error
func normalizeVPCs(t awsx.Target, in []ec2types.Vpc, now time.Time) []model.VPC
func normalizeSubnets(t awsx.Target, in []ec2types.Subnet, now time.Time) []model.Subnet
func normalizeEdges(t awsx.Target, igws []ec2types.InternetGateway, nats []ec2types.NatGateway, eps []ec2types.VpcEndpoint, now time.Time) []model.VPCEdge
func normalizeSecurityGroups(t awsx.Target, in []ec2types.SecurityGroup, now time.Time) []model.SecurityGroup
func nameTag(tags []ec2types.Tag, fallback string) string
func endpointShortName(serviceName string) (string, bool)   // "com.amazonaws.us-east-1.ecr.api" → ("ecr.api", true); keeps only ecr.api, ecr.dkr, logs, ssmmessages
```

`pollNetwork` order: VPCs (paginator) → subnets → IGWs (filter `attachment.vpc-id` = all VPC ids) → NATs (filter `vpc-id`, state `available`|`pending`) → endpoints (filter `vpc-id`) → security groups (filter `group-id` = union of services' groups; skip the call when the set is empty) → one `ReplaceNetwork`. Edge `Detail`: endpoint short name; NAT state; empty for IGW. Edge `SubnetID`: NAT's subnet. Any error returns immediately (the loop's back-off handles throttles); nothing is written on error.

- [ ] **Step 1: Write the failing tests**

`internal/poller/network_test.go` (table-driven where natural; fake EC2 from Task 2; `recordingNetworkStore` captures the last `ReplaceNetwork` args):
- `TestNormalizeEdges_KeepsOnlyEcsEndpoints`: 5 endpoints in (4 ECS + s3) → 4 edges, `Detail` values exactly `{"ecr.api","ecr.dkr","logs","ssmmessages"}`, `Kind=="endpoint"`.
- `TestEndpointShortName`: rows for the four names, `com.amazonaws.us-east-1.s3` → `("", false)`, `cn.com.amazonaws.cn-north-1.logs` → `("logs", true)`.
- `TestNameTag_FallsBackToID`: no tags → fallback; tag `Name=""` → fallback; `Name="homelab-main"` → name.
- `TestNormalizeSubnets_PublicFromMapPublicIpOnLaunch`: `MapPublicIpOnLaunch=true` → `Public=true`; nil → false; `AvailableIpAddressCount` nil → 0.
- `TestPollNetwork_WritesEstate`: homelab-admin/us-east-1 → 3 VPCs, 6 subnets, edges contain 3 igw + 2 nat + 4 endpoints, `legacy-default.IsDefault==true`.
- `TestPollNetwork_SecurityGroupsOnlyFromServices`: services list 2 groups → exactly those 2 in the replace call; empty list → no `DescribeSecurityGroups` call (fake call counter).
- `TestPollNetwork_ThrottleReturnsThrottleAndWritesNothing`: `ThrottleEC2=1` → `awsx.IsThrottle(err)`, store never called.
- `internal/config/config_test.go`: `TestLoad_PollIntervalsDefault5m` (`Poll.Network == 5*time.Minute`, same for storage) and `TestLoad_PollNetworkIntervalParsed` (`HM_POLL_NETWORK=90s`).

- [ ] **Step 2: Run `go test ./internal/poller/ ./internal/config/ -run 'Network|Endpoint|NameTag|NormalizeSubnets|PollInterval' -v`; expect FAIL (undefined).**

- [ ] **Step 3: Implement config fields, `internal/poller/network.go`, and add the tier entry in `tiers.go`.** Use the SDK paginators (`ec2.NewDescribeVpcsPaginator` etc. accept the narrow interface).

- [ ] **Step 4: Run the tests; expect PASS. `go vet ./...`.**

- [ ] **Step 5: Commit**

```bash
git add internal/config internal/poller/network.go internal/poller/network_test.go internal/poller/tiers.go internal/poller/poller.go
git commit -m "feat(poller): network tier polls VPCs, subnets, edges and used security groups every 5 min"
```

---

### Task 4: Storage poller tier — *Implementer: Haiku (follow Task 3's pattern)*

**Files:**
- Create: `internal/poller/storage.go`
- Modify: `internal/poller/tiers.go` (add the `storage` entry to the Loop's tier table)
- Test: `internal/poller/storage_test.go`

**Interfaces:**
- Consumes: `awsx.EFSAPI`, `fake.EFS`, `store.ReplaceStorage`.
- Produces:

```go
type storageStore interface {
    ReplaceStorage(ctx context.Context, accountID, region string, fs []model.FileSystem, aps []model.AccessPoint) error
}
// tier table entry: name "storage", base = cfg.Intervals.Storage, work = pollStorage
func pollStorage(ctx context.Context, t awsx.Target, efsc awsx.EFSAPI, st storageStore, now time.Time) error
func normalizeFileSystem(t awsx.Target, fs efstypes.FileSystemDescription, mts []efstypes.MountTargetDescription, now time.Time) model.FileSystem
func normalizeAccessPoint(t awsx.Target, ap efstypes.AccessPointDescription, now time.Time) model.AccessPoint
func throughputModeLabel(mode efstypes.ThroughputMode, provisionedMiBps *float64) string   // "Bursting" | "Elastic" | "Provisioned 64 MiB/s"
func performanceModeLabel(mode efstypes.PerformanceMode) string                            // "General Purpose" | "Max I/O"
```

Name = `FileSystemDescription.Name`, else tag `Name`, else the file system ID. `SizeBytes = SizeInBytes.Value`. Mount targets: one `DescribeMountTargets{FileSystemId}` per file system (paginated by `Marker`); `VPCID` = first mount target's `VpcId`; `MountTargetAZs` sorted unique `AvailabilityZoneName`. Access points: one paginated `DescribeAccessPoints` without filter; `RootPath = RootDirectory.Path` (default `/`), `PosixUID/GID` from `PosixUser` when set.

- [ ] **Step 1: Write the failing tests**

- `TestThroughputModeLabel`: `(bursting, nil)` → "Bursting"; `(elastic, nil)` → "Elastic"; `(provisioned, 64)` → "Provisioned 64 MiB/s"; `(provisioned, 12.5)` → "Provisioned 12.5 MiB/s".
- `TestPerformanceModeLabel`: `generalPurpose` → "General Purpose"; `maxIO` → "Max I/O".
- `TestNormalizeFileSystem_VPCFromMountTargets`: two mount targets in `us-east-1a`,`us-east-1c` → `VPCID` set, `MountTargetAZs == {"us-east-1a","us-east-1c"}`; none → `VPCID==""`.
- `TestNormalizeFileSystem_NameFallsBackToID`.
- `TestNormalizeAccessPoint_PosixUser`: uid 1000/gid 1000 → pointers set; nil `PosixUser` → nil; empty `RootDirectory` → `"/"`.
- `TestPollStorage_WritesFileSystemsAndAccessPoints`: homelab-admin/us-east-1 → 3 file systems (`jenkins-home` VPC `vpc-07de19a4c011`), 2 access points; `search-index` absent (eu-west-1).
- `TestPollStorage_ThrottleReturnsThrottle`.

- [ ] **Step 2: Run `go test ./internal/poller/ -run 'Storage|ThroughputMode|PerformanceMode|NormalizeFileSystem|NormalizeAccessPoint' -v`; expect FAIL.**

- [ ] **Step 3: Implement `internal/poller/storage.go`; add the tier entry.**

- [ ] **Step 4: Run the tests; expect PASS.**

- [ ] **Step 5: Commit**

```bash
git add internal/poller/storage.go internal/poller/storage_test.go internal/poller/tiers.go
git commit -m "feat(poller): storage tier polls EFS file systems, mount targets and access points"
```

---

### Task 5: `internal/network` — visibility, IP math, tree assembly, summaries — *Implementer: Sonnet*

**Files:**
- Create: `internal/network/visibility.go`, `internal/network/ips.go`, `internal/network/tree.go`, `internal/network/summary.go`
- Test: `internal/network/visibility_test.go`, `ips_test.go`, `tree_test.go`, `summary_test.go` (pure; fixtures built in `fixtures_test.go` mirroring homelab-main: 10 running tasks exactly as in `network-tree.png`)

**Interfaces:**
- Consumes: `authz.Subject`, `authz.Tags`, `authz.Can`, `authz.EffectiveServiceTag`, `authz.EffectiveClusterTag`, `model.Task`, `model.VPC`, `model.Subnet`, `model.VPCEdge`, `model.ClusterKey.ID()`, `model.ServiceKey.ID()`, `model.TaskID`.
- Produces:

```go
type TaskVisibility func(model.Task) bool
func Visibility(subj authz.Subject, tags authz.Tags) TaskVisibility       // service task: Can(View, EffectiveServiceTag); standalone: Can(View, EffectiveClusterTag)
func FilterTasks(v TaskVisibility, tasks []model.Task) []model.Task        // keeps LastStatus == "RUNNING" and visible

func SubnetIPs(cidr string, availableIPs int) (used, total int)           // total = 2^(32−prefix) − 5; used = total − available; clamp both to [0,total]; bad CIDR → (0,0)
func ShortTaskID(taskARN string) string                                     // last "/" segment, first 8 chars

type ServiceInfo struct { ID string; Status model.Status; Revision int }
type TagInfo struct { Name, Color string }
type Input struct {
    VPC               model.VPC
    Subnets           []model.Subnet                      // all subnets of the account×region (Assemble picks this VPC's)
    Edges             []model.VPCEdge                     // this VPC's
    Tasks             []model.Task                        // visible RUNNING tasks of the account×region
    Services          map[model.ServiceKey]ServiceInfo
    ClusterTags       map[model.ClusterKey]TagInfo
    CollapseThreshold int                                 // 150
}
type Node struct {
    Kind     string   `json:"kind"`             // vpc | az | nosubnet | subnet | cluster | service | standalone | task
    Key      string   `json:"key"`
    Label    string   `json:"label"`
    Children []Node   `json:"children,omitempty"`
    VPC      *VPCData `json:"vpc,omitempty"`;  AZ *AZData `json:"az,omitempty"`;  Subnet *SubnetData `json:"subnet,omitempty"`
    Cluster  *ClusterData `json:"cluster,omitempty"`; Service *ServiceData `json:"service,omitempty"`; Task *TaskData `json:"task,omitempty"`
    Tasks    int      `json:"tasks"`            // running visible tasks under this node (nosubnet/standalone use it too)
}
type VPCData struct { VPCID, CIDR string; Zones int }
type AZData struct { Subnets int }
type SubnetData struct { SubnetID, CIDR string; Public bool; IPsUsed, IPsTotal int }
type ClusterData struct { ClusterID string; Tag TagInfo; Services int }
type ServiceData struct { ServiceID string; Status model.Status; Revision int }
type TaskData struct { TaskID, ServiceID, PrivateIP, Group string; Unhealthy bool }
type Edge struct { Kind model.VPCEdgeKind `json:"kind"`; ID string `json:"id"`; SubnetID string `json:"subnet_id,omitempty"`; Detail string `json:"detail,omitempty"` }
type Summary struct { Subnets, Zones, RunningTasks, UnplacedTasks int; CollapsedByDefault bool }
type Tree struct { Root Node `json:"root"`; Edges []Edge `json:"edges"`; Summary Summary `json:"summary"` }
func Assemble(in Input) Tree

type RegionSummary struct { Region string; ActiveVPCs, HiddenVPCs, Clusters, Tasks, UnplacedTasks int }
func SummarizeRegion(region string, vpcs []model.VPC, subnets []model.Subnet, tasks []model.Task) RegionSummary
type VPCSummary struct { VPC model.VPC; Subnets int; Clusters []string; Tasks int }
func SummarizeVPCs(vpcs []model.VPC, subnets []model.Subnet, tasks []model.Task) []VPCSummary   // active only, by name
func ActiveVPCs(subnets []model.Subnet, tasks []model.Task) map[string]int                     // vpcID → visible running tasks
```

JSON field names are snake_case via struct tags (`vpc_id`, `ips_used`, `ips_total`, `cluster_id`, `service_id`, `task_id`, `private_ip`, …). Keys: `vpc:<vpcId>`, `az:<vpcId>:<az>`, `nosub:<vpcId>`, `sn:<subnetId>`, `sn:<subnetId>:<cluster>`, `sn:<subnetId>:<cluster>:<service>`, `sn:<subnetId>:<cluster>:_standalone`, `task:<taskId>`; under nosubnet the `sn:<subnetId>` prefix becomes `nosub:<vpcId>`. Ordering: AZs alphabetical; subnets by name then ID; `nosubnet` after the AZs; clusters by name; services by name, `standalone` last; tasks by `StartedAt` then ARN. Labels: VPC/subnet/AZ names, cluster/service names, task `ShortTaskID`. Edges sorted igw, nat, endpoint, then by Detail/ID.

Example tree excerpt (what the API returns, for the frontend contract):

```json
{"kind":"subnet","key":"sn:subnet-03f1a9c2","label":"private-1a","tasks":7,
 "subnet":{"subnet_id":"subnet-03f1a9c2","cidr":"10.20.1.0/24","public":false,"ips_used":27,"ips_total":251},
 "children":[{"kind":"cluster","key":"sn:subnet-03f1a9c2:core-services","label":"core-services","tasks":5,
   "cluster":{"cluster_id":"<ClusterKey.ID()>","tag":{"name":"Prod","color":"red"},"services":4},
   "children":[{"kind":"service","key":"sn:subnet-03f1a9c2:core-services:payments-api","label":"payments-api","tasks":2,
     "service":{"service_id":"<ServiceKey.ID()>","status":"steady","revision":42},
     "children":[{"kind":"task","key":"task:<TaskID>","label":"6faba45f","tasks":1,
       "task":{"task_id":"<TaskID>","service_id":"<ServiceKey.ID()>","private_ip":"10.20.1.23","unhealthy":false}}]}]}]}
```

- [ ] **Step 1: Write the failing tests**

- `TestVisibility_ServiceTaskUsesEffectiveServiceTag` (Prod-only subject sees payments-api task, not report-worker), `TestVisibility_StandaloneTaskUsesClusterTag`, `TestVisibility_AdminSeesAll`, `TestFilterTasks_DropsStopped`.
- `TestSubnetIPs` rows: `("10.20.1.0/24",224)→(27,251)`; `("172.31.0.0/20",4091)→(0,4091)`; `("10.0.0.0/28",11)→(0,11)`; `("10.0.0.0/24",300)→(0,251)`; `("",0)→(0,0)`; `("garbage",5)→(0,0)`.
- `TestShortTaskID`: `arn:aws:ecs:us-east-1:4821:task/core-services/6faba45f1c…` → `"6faba45f"`.
- `TestAssemble_MatchesScreenshotCounts` (homelab-main fixture): root `Tasks==10`, `VPC.Zones==2`, `Summary=={Subnets:3, Zones:2, RunningTasks:10}`; AZ us-east-1a `AZ.Subnets==2`, `Tasks==7`; subnet private-1a `IPsUsed==27`, `IPsTotal==251`; cluster core-services under private-1a `Services==4`, `Tasks==5`.
- `TestAssemble_OrdersAZsSubnetsClustersServicesTasks`.
- `TestAssemble_EmptySubnetHasNoChildren`: public-1a → `Children==nil`, `Tasks==0`.
- `TestAssemble_StandaloneTasksNodeLast`: standalone task in background-jobs → last child of the cluster has `Kind=="standalone"`, `Label=="Standalone tasks"`, key suffix `:_standalone`, child task `Group=="family:report-backfill"`.
- `TestAssemble_NoSubnetTasksUnderNosubnetNode`: a bridge task (empty SubnetID) of core-services → node `nosub:vpc-0a41c7e29b3d` after the AZ nodes containing cluster ▸ service ▸ task; a bridge task of a cluster with no awsvpc task anywhere → absent, `Summary.UnplacedTasks==1`.
- `TestAssemble_UnknownSubnetIsUnplaced`: task with `SubnetID="subnet-new"` → not in tree, `UnplacedTasks==1`, `RunningTasks` excludes it.
- `TestAssemble_CollapsedByDefaultAbove150`: 151 tasks → true; 150 → false.
- `TestAssemble_UnhealthyTaskFlag`: `HealthStatus=="UNHEALTHY"` → `Task.Unhealthy`.
- `TestAssemble_EdgesSortedAndDetailed`: igw first, nat with `SubnetID`, endpoints alphabetical by `Detail`.
- `TestAssemble_IgnoresTasksOfOtherVPCs`.
- `TestActiveVPCs_CountsOnlyGivenTasks`; `TestSummarizeRegion_HiddenVPCs` (us-east-1 homelab-admin: `ActiveVPCs==2, HiddenVPCs==1, Clusters==3, Tasks==12`); `TestSummarizeRegion_UnplacedTasks`; `TestSummarizeVPCs_ActiveOnlySortedByName` (homelab-main: 3 subnets, clusters `[background-jobs core-services]`, 10 tasks; shared-services 2/1/2; legacy-default absent).

- [ ] **Step 2: Run `go test ./internal/network/ -v`; expect FAIL (package missing).**

- [ ] **Step 3: Implement the four files with the signatures above.** Group tasks with nested maps keyed by subnet → cluster → service, then emit sorted slices; compute `Tasks` bottom-up.

- [ ] **Step 4: Run `go test ./internal/network/ -v`; expect PASS.**

- [ ] **Step 5: Commit**

```bash
git add internal/network
git commit -m "feat(network): assemble the VPC tree from subnets and visible running tasks"
```

---

### Task 6: `internal/storage` mounts and visibility; EFS metric queries — *Implementer: Sonnet*

**Files:**
- Create: `internal/storage/mounts.go`, `internal/storage/visible.go`, `internal/storage/format.go`
- Create: `internal/metrics/efs.go`
- Create: `internal/awsx/fake/cloudwatch_efs.go`
- Test: `internal/storage/mounts_test.go`, `visible_test.go`, `format_test.go`, `internal/metrics/efs_test.go`, `internal/awsx/fake/cloudwatch_efs_test.go`; fixture `internal/storage/testdata/taskdef_efs.json` (a real-shaped `DescribeTaskDefinition.taskDefinition` for report-worker)

**Interfaces:**
- Consumes: `metrics.Query`, `metrics.Dim`, `metrics.Span`, `metrics.Point` (03 Task 3), `model.Service`, `model.FileSystem`, `authz.Subject`.
- Produces:

```go
// internal/storage
type Mount struct { FileSystemID, AccessPointID, Container, ContainerPath string; ReadOnly bool }
func MountsFromTaskDefinition(raw json.RawMessage) ([]Mount, error)      // volumes[].efsVolumeConfiguration joined with containerDefinitions[].mountPoints[].sourceVolume
type MountedBy struct { Service model.ServiceKey; ServiceID, ServiceName, Cluster, ContainerPath string; RunningTasks int }
func MountIndex(services []model.Service, taskDefs map[string]json.RawMessage, running map[model.ServiceKey]int) map[string][]MountedBy   // fsID → sorted by Cluster, ServiceName
func Visible(subj authz.Subject, fs []model.FileSystem, index map[string][]MountedBy) []model.FileSystem   // admin: all; else len(index[fs]) > 0; sorted by Name
func FormatSize(bytes int64) string          // < 1 GiB → "20 MiB" (0 decimals); else "38.4 GiB" (1 decimal)
func Growth(points []metrics.Point) (pct float64, ok bool)   // from (*metrics.Result).Points(id); nil V skipped; (last−first)/first·100; ok=false when < 2 non-nil points or first == 0

// internal/metrics/efs.go
func EFSStorageQueries(fsIDs []string) []Query     // ID "sz_<i>", Namespace AWS/EFS, MetricName StorageBytes, Dimensions FileSystemId + StorageClass=Total, Stat Average, Period 86400
func EFSThroughputQueries(fsIDs []string) []Query  // "io_<i>", MeteredIOBytes, Sum, Period 1200
func EFSConnectionQueries(fsIDs []string) []Query  // "cc_<i>", ClientConnections, Sum, Period 60
const (
    EFSSizeSpan = Span(30 * 24 * time.Hour)       // fetched alone (period 86400)
    EFSThroughputSpan = Span24h                   // fetched alone (period 1200)
    EFSConnectionSpan = Span(10 * time.Minute)    // fetched alone (period 60)
)   // three Fetch calls per account×region because 03's Fetch rejects mixed periods (ErrMixedPeriods); End/Start flooring is done by Fetch
func BytesPerPeriodToMiBps(sum float64, periodSeconds int) float64
```

Fake CloudWatch synth for `AWS/EFS` (mockup l.798–801): `StorageBytes` rises linearly from 78 % to 100 % of the seeded size with ±1 % noise, last point = seeded size; `MeteredIOBytes` ≈ 25 MiB/s (size > 10 GiB) or 4 MiB/s over the period, ±50 %; `ClientConnections` = running tasks of mounting services (shared-certs → 3).

- [ ] **Step 1: Write the failing tests**

- `TestMountsFromTaskDefinition_EFSVolumeWithMountPoint` (fixture): one mount `{fs-02e7d1c9, fsap-07f3e9a1c2, "report-worker", "/data/reports", false}`.
- `TestMountsFromTaskDefinition_IgnoresNonEFSVolumes` (host and dockerVolumeConfiguration volumes → none), `TestMountsFromTaskDefinition_VolumeWithoutMountPointIsSkipped`, `TestMountsFromTaskDefinition_TwoContainersSameVolume` (two mounts), `TestMountsFromTaskDefinition_InvalidJSON` (error).
- `TestMountIndex_GroupsAndSorts`: edge-nginx + cert-renewer → `index["fs-0a6b33f0"]` ordered by cluster then service, `RunningTasks` 2 and 1; a service whose task definition is missing from `taskDefs` is skipped.
- `TestVisible_NonAdminSeesOnlyMounted` (uploads-archive hidden), `TestVisible_AdminSeesNotMounted`, `TestVisible_SortedByName`.
- `TestFormatSize`: `21474836→"20 MiB"`, `41231686041→"38.4 GiB"`, `227204155801→"211.6 GiB"`, `0→"0 MiB"`.
- `TestGrowth`: 78→100 → `28.2…` (±0.1), first 0 → false, one point → false.
- `TestEFSStorageQueries_IDsAndDimensions` (IDs match 03's `^[a-z][a-zA-Z0-9_]*$`, `StorageClass=="Total"`, `Period == 86400`), `TestEFSQueries_OnePeriodPerBuilder` (every query of a builder has the same `Period`, so each set is a valid single Fetch), `TestBytesPerPeriodToMiBps` (`1258291200` over 1200 s → `1.0`).
- `TestFakeCloudWatch_EFSStorageBytesEndsAtSeededSize`, `TestFakeCloudWatch_EFSClientConnections` (shared-certs → 3).

- [ ] **Step 2: Run `go test ./internal/storage/ ./internal/metrics/ ./internal/awsx/fake/ -run 'Mount|Visible|FormatSize|Growth|EFS' -v`; expect FAIL.**

- [ ] **Step 3: Implement.** Parse only the fields named (small private structs, not the SDK type), so a task definition with unknown fields still parses.

- [ ] **Step 4: Run the tests; expect PASS.**

- [ ] **Step 5: Commit**

```bash
git add internal/storage internal/metrics/efs.go internal/metrics/efs_test.go internal/awsx/fake/cloudwatch_efs.go internal/awsx/fake/cloudwatch_efs_test.go
git commit -m "feat(storage): map task definitions to EFS mounts and build EFS metric queries"
```

---

### Task 7: Networks API handlers — *Implementer: Sonnet*

**Files:**
- Create: `internal/api/networks.go`
- Modify: `internal/api/server.go` (3 routes in `routes(mux)`)
- Test: `internal/api/networks_test.go` (DB test; seeds accounts, tags, services, tasks via 01's store methods and network rows via `ReplaceNetwork`; subjects: admin, `devOnly` = `{Dev: View}`, `prodOnly` = `{Prod: View}`, `none`)

**Interfaces:**
- Consumes: Task 1 store methods, Task 5 `network.*`, 01's `ListAccounts` (keep `Enabled`), `RunningTasks(ctx, f, vis)` (already visibility-filtered in SQL), `ListServices`, `ListTags`, `s.scopeFrom(r)` (subject, `Vis`, `Filter`, `Tags`), `writeJSON`, `writeError`.
- Produces routes and bodies:

```
GET /api/networks?account=&region=
  {"items":[{"account_id":"482109375512","account_name":"homelab-admin",
             "regions":[{"region":"us-east-1","active_vpcs":2,"hidden_vpcs":1,"clusters":3,"tasks":12,"unplaced_tasks":0}, …every configured region…]}],
   "polled_at":"…"}
GET /api/networks/{account}/{region}
  {"items":[{"vpc_id":"vpc-0a41c7e29b3d","name":"homelab-main","cidr":"10.20.0.0/16","subnets":3,
             "clusters":["background-jobs","core-services"],"tasks":10}], "polled_at":"…"}   // active VPCs only
GET /api/vpcs/{vpcId}/tree?account=&region=
  {"vpc":{"vpc_id":"…","name":"…","cidr":"…","account_id":"…","account_name":"…","region":"…"},
   "edges":[{"kind":"igw","id":"igw-…"},{"kind":"nat","id":"nat-…","subnet_id":"subnet-…","detail":"available"},{"kind":"endpoint","id":"vpce-…","detail":"ecr.api"}],
   "summary":{"subnets":3,"zones":2,"running_tasks":10,"unplaced_tasks":0,"collapsed_by_default":false},
   "root":{…network.Node…}, "polled_at":"…"}
```

`polled_at` = newest `PolledAt` among the tasks considered, else the VPC's. Status codes: unknown account or region not in its list → 404 `not_found`; tree missing `account`/`region` → 422 `invalid`; VPC unknown **or not active for this subject** → 404 `not_found`. Visibility = `RunningTasks(ctx, scope.Filter, scope.Vis)` plus `network.Visibility(scope.Subject, scope.Tags)` (same rule, kept for the pure package) before any counting; `Services` map built from `ListServices` + `model.ServiceKey.ID()`; `ClusterTags` from `EffectiveClusterTag` + `ListTags`. Collapse threshold 150.

- [ ] **Step 1: Write the failing handler tests**

- Counts below are from the mockup screenshots; derive the exact numbers from 01's `MockupEstate` when writing the tests (01 seeds `notifications` with two running tasks, :10 and :9, and the standalone `release-build` runs in `ci-tools` → `shared-services`), and keep the assertions' shape.
- `TestNetworks_AdminSeesAllAccountsAndDimmedRegions`: homelab-admin has 3 region entries; us-east-1 `{2,1,3,12}`; staging-example eu-west-1 `active_vpcs==0`.
- `TestNetworks_DevUserSeesOnlyDevTasks`: devOnly → homelab-admin us-east-1 `tasks` excludes core-services (Prod) tasks, `hidden_vpcs` unchanged.
- `TestNetworks_NoPermissionsIsEmptyRegions` (none → every region `active_vpcs==0`).
- `TestNetworks_FilterByAccountAndRegion` (`?account=731022849906&region=us-east-1` → one account, one region).
- `TestNetworkRegion_ListsActiveVPCsOnly` (homelab-main 3/2/10, shared-services 2/1/2, no legacy-default), `TestNetworkRegion_UnknownAccountIs404`, `TestNetworkRegion_RegionNotScannedIs404`.
- `TestVPCTree_RequiresAccountAndRegion` (422 `invalid`), `TestVPCTree_InvisibleVPCIs404` (prodOnly → shared-services 404; none → homelab-main 404), `TestVPCTree_ReturnsTreeEdgesSummary` (admin homelab-main: 4 endpoints, `running_tasks==10`, root kind `vpc`), `TestVPCTree_ProdOnlySeesOnlyCoreServices` (no `background-jobs` cluster node).

- [ ] **Step 2: Run `go test ./internal/api/ -run 'Networks|NetworkRegion|VPCTree' -v`; expect FAIL.**

- [ ] **Step 3: Implement `internal/api/networks.go` and register routes.**

- [ ] **Step 4: Run the tests; expect PASS. `go vet ./...`.**

- [ ] **Step 5: Commit**

```bash
git add internal/api/networks.go internal/api/networks_test.go internal/api/server.go
git commit -m "feat(api): networks endpoints with permission-filtered VPC tree"
```

---

### Task 8: Storage API handler and VPC enrichment of service/cluster detail — *Implementer: Sonnet*

**Files:**
- Create: `internal/api/storage.go`
- Modify: `internal/api/server.go` (route), `internal/api/services.go` (networking block), `internal/api/clusters.go` (detail), `internal/api/dto.go`
- Test: `internal/api/storage_test.go`, additions to `internal/api/services_test.go` and `clusters_test.go`

**Interfaces:**
- Consumes: Task 1 store, Task 6 `storage.*` + `metrics.EFS*`, 03's `(*metrics.Client).Fetch(ctx, t, span, queries) (*metrics.Result, error)` via `s.Metrics` (behind a small interface `efsMetrics interface{ Fetch(ctx context.Context, t awsx.Target, span metrics.Span, qs []metrics.Query) (*metrics.Result, error) }` so tests inject a failing one), `GetTaskDefinition`.
- Produces:

```
GET /api/storage?account=&region=
  {"items":[{"file_system_id":"fs-0c91a4e7","name":"jenkins-home","account_id":"…","region":"us-east-1",
             "vpc":{"vpc_id":"vpc-07de19a4c011","name":"shared-services"} | null,
             "size_bytes":41231686041,"throughput_mode":"Elastic","performance_mode":"General Purpose",
             "mounted":true,"client_connections":1 | null,"mounting_tasks":1,
             "access_points":[{"access_point_id":"fsap-0a1b2c3d4e","path":"/jenkins","uid":1000 | null}],
             "mounted_by":[{"service_id":"…","service":"jenkins-controller","cluster":"ci-tools","container_path":"/var/jenkins_home","tasks":1}],
             "size_30d":[{"t":"…","v":…}] | null, "growth_30d_pct": 29.1 | null,
             "throughput_24h":[{"t":"…","v":<MiB/s>}] | null}],
   "totals":{"size_bytes":…,"mounted":4,"not_mounted":1},
   "metrics_error": "…" | omitted,
   "polled_at":"…"}
```
Service detail `networking` gains `"vpc":{"vpc_id","name"}|null` (VPC of the service's first subnet) and `"security_group_names":{"sg-…":"payments-api"}`; cluster detail gains `"vpc":{"vpc_id","name"}|null` (most common VPC among the cluster's visible running tasks' subnets).

Handler flow: visible services (View on effective tag) → `MountIndex` (task definitions via `GetTaskDefinition(ctx, acct, region, arn)`, running counts from `RunningTasks`) → `storage.Visible` → drop index entries whose file system is not in `efs` → per account×region three `Fetch` calls (`EFSSizeSpan` + `EFSStorageQueries`, `EFSThroughputSpan` + `EFSThroughputQueries`, `EFSConnectionSpan` + `EFSConnectionQueries`; series via `Result.Points(id)`); a `Stale` result is used as is; any fetch error sets `metrics_error` and leaves the series null for that account×region, the response is still 200.

- [ ] **Step 1: Write the failing tests**

- `TestStorage_AdminSeesNotMounted` (5 items, totals `{mounted:4, not_mounted:1}`, uploads-archive `mounted==false`, `mounted_by==[]`).
- `TestStorage_DevUserSeesOnlyMounted` (4 items, no uploads-archive); `TestStorage_ProdOnlySeesNothing` (`items==[]`).
- `TestStorage_MountedByHasPathAndTaskCount` (shared-certs: edge-nginx `/etc/nginx/certs` tasks 2 before cert-renewer `/certs` tasks 1; `mounting_tasks==3`).
- `TestStorage_MetricsFailureStillListsFileSystems` (fetcher returns error → 200, 5 items, `size_30d==null`, `metrics_error` non-empty).
- `TestStorage_MountOfUnknownFileSystemIgnored` (task definition references `fs-deadbeef` → no phantom item, no panic).
- `TestStorage_FilterByRegion` (`?region=eu-west-1` → search-index only).
- `TestServiceDetail_IncludesVPCAndSecurityGroupNames`; `TestClusterDetail_IncludesVPC`.

- [ ] **Step 2: Run `go test ./internal/api/ -run 'Storage|ServiceDetail_IncludesVPC|ClusterDetail_IncludesVPC' -v`; expect FAIL.**

- [ ] **Step 3: Implement the handler and the two enrichments.**

- [ ] **Step 4: Run the whole `./internal/api/` suite; expect PASS.**

- [ ] **Step 5: Commit**

```bash
git add internal/api/storage.go internal/api/storage_test.go internal/api/server.go internal/api/dto.go internal/api/services.go internal/api/clusters.go internal/api/services_test.go internal/api/clusters_test.go
git commit -m "feat(api): storage endpoint with EFS metrics and VPC links on service and cluster detail"
```

---

### Task 9: Frontend API types and hooks — *Implementer: Haiku*

**Files:**
- Create: `web/src/api/networks.ts`, `web/src/api/storage.ts`
- Test: `web/src/api/networks.test.ts` (hook builds the right URL and query key)

**Interfaces:**
- Consumes: `api.get` (01's `web/src/api/client.ts`), `useQuery`, `useFilters`.
- Produces (TypeScript):

```ts
export type TreeNode =
  | { kind: 'vpc'; key: string; label: string; tasks: number; vpc: { vpc_id: string; cidr: string; zones: number }; children?: TreeNode[] }
  | { kind: 'az'; key: string; label: string; tasks: number; az: { subnets: number }; children?: TreeNode[] }
  | { kind: 'nosubnet'; key: string; label: string; tasks: number; children?: TreeNode[] }
  | { kind: 'subnet'; key: string; label: string; tasks: number; subnet: { subnet_id: string; cidr: string; public: boolean; ips_used: number; ips_total: number }; children?: TreeNode[] }
  | { kind: 'cluster'; key: string; label: string; tasks: number; cluster: { cluster_id: string; tag: { name: string; color: string }; services: number }; children?: TreeNode[] }
  | { kind: 'service'; key: string; label: string; tasks: number; service: { service_id: string; status: Status; revision: number }; children?: TreeNode[] }
  | { kind: 'standalone'; key: string; label: string; tasks: number; children?: TreeNode[] }
  | { kind: 'task'; key: string; label: string; tasks: number; task: { task_id: string; service_id?: string; private_ip: string; group?: string; unhealthy: boolean } };
export interface NetworksResponse { items: AccountNetworks[]; polled_at: string }       // shapes mirror Task 7 bodies
export interface RegionVpcsResponse { items: VpcSummary[]; polled_at: string }
export interface VpcTreeResponse { vpc: …; edges: Edge[]; summary: {…}; root: TreeNode; polled_at: string }
export function useNetworks(): UseQueryResult<NetworksResponse>                           // key ['networks', filters], refetchInterval 5000
export function useNetworkRegion(account: string, region: string)                          // key ['networks', account, region]
export function useVpcTree(account: string, region: string, vpcId: string)                 // key ['vpcTree', account, region, vpcId]; GET /api/vpcs/{vpcId}/tree?account=&region=
export function useStorage(): UseQueryResult<StorageResponse>                              // key ['storage', filters], refetchInterval 60000
export function nodeHref(n: TreeNode): string | undefined   // cluster → /clusters/:id; service → /services/:id/overview; task → /services/:id/tasks (undefined for standalone tasks → /tasks)
```

- [ ] **Step 1: Write the failing test** `useVpcTree builds the tree URL with account and region` and `useStorage refetches every 60 s` (inspect `queryOptions` or the mocked `api.get` call URL).
- [ ] **Step 2: Run `npm --prefix web run test -- networks`; expect FAIL.**
- [ ] **Step 3: Implement both files.**
- [ ] **Step 4: Run the test and `npm --prefix web run typecheck`; expect PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): typed networks and storage API hooks"`

---

### Task 10: Networks account and region pages — *Implementer: Haiku*

**Files:**
- Create: `web/src/app/networks/NetCard.tsx`, `NetworksPage.tsx`, `NetworkRegionPage.tsx`, `networks.module.css`
- Test: `web/src/app/networks/NetworksPage.test.tsx`, `NetworkRegionPage.test.tsx`

**Interfaces:**
- Consumes: Task 9 hooks, 01's page-head markup (`h1` + `.meta`) and `useCrumbs`, `Card`, `EmptyState`, `Link`.
- Produces: `NetCard({ title, subtitle?, stats: {value: number, label: string}[], note?, to?: string, off?: boolean })` — `.net-cards` grid `repeat(auto-fill, minmax(280px, 1fr))`, `.off` → `opacity: 0.6`, no link (mockup l.261–266, `network-account.png`).

Copy: page meta "Account level: regions with active VPCs. A VPC is active when ECS tasks run in it."; per account a heading `name` + mono account number; region card stats `active VPC(s)`, `clusters`, `tasks`; note "{n} VPC without tasks hidden" when `hidden_vpcs > 0` (plural "VPCs" when n > 1), note "{n} tasks without a subnet not shown" when `unplaced_tasks > 0`; dimmed card text "No active VPCs". Region page: title = region, meta "Active VPCs in {account_name}", breadcrumbs Networks / account / region; VPC cards: name, mono `vpc_id · cidr`, stats subnets/clusters/tasks, cluster names joined by ", ", link to `/networks/:account/:region/:vpcId`; when no active VPC: `EmptyState` "No active VPCs in this region."

- [ ] **Step 1: Write the failing component tests**

`NetworksPage.test.tsx`: renders "homelab-admin" and "482109375512"; the us-east-1 card has "2 active VPCs", "3 clusters", "12 tasks", "1 VPC without tasks hidden" and links to `/networks/482109375512/us-east-1`; a region with `active_vpcs: 0` renders "No active VPCs" without a link.
`NetworkRegionPage.test.tsx` (route `/networks/482109375512/us-east-1`): heading "us-east-1", "Active VPCs in homelab-admin"; card "homelab-main" with "vpc-0a41c7e29b3d · 10.20.0.0/16", "3 subnets", "core-services, background-jobs" (order as returned) and link `/networks/482109375512/us-east-1/vpc-0a41c7e29b3d`; empty items → "No active VPCs in this region."

- [ ] **Step 2: Run `npm --prefix web run test -- networks/Network`; expect FAIL.**
- [ ] **Step 3: Implement the three components.**
- [ ] **Step 4: Run tests, `lint`, `typecheck`; expect PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): networks account and region pages"`

---

### Task 11: Tree component with connector lines and keyboard navigation — *Implementer: Sonnet*

**Files:**
- Create: `web/src/app/networks/tree/Tree.tsx`, `TreeNode.tsx`, `NodeBody.tsx`, `useTreeState.ts`, `useTreeKeyboard.ts`, `tree.module.css`
- Test: `web/src/app/networks/tree/Tree.test.tsx`, `useTreeState.test.ts`

**Interfaces:**
- Consumes: `TreeNode`, `nodeHref` (Task 9), `Pill` + `statusKind`/`statusLabel`, `TagChip`, `useNavigate`.
- Produces:

```ts
export function useTreeState(root: TreeNode | undefined, collapsedByDefault: boolean, resetKey: string): {
  closed: ReadonlySet<string>; toggle(key: string): void; expandAll(): void; collapseToServices(): void }
  // collapseToServices = closed := every key of kind 'service' | 'standalone'; initial state = collapseToServices() when collapsedByDefault; state survives data refetches (keys are stable) and resets when resetKey (vpcId) changes
export function Tree({ root, closed, onToggle, ariaLabel }: { root: TreeNode; closed: ReadonlySet<string>; onToggle(key: string): void; ariaLabel: string }): JSX.Element
export function serviceKeys(root: TreeNode): string[]
```

DOM / ARIA: container `div.tree` (`overflow-x: auto`) → `ul role="tree" aria-label` → `li role="treeitem" aria-level aria-expanded (only when it has children) aria-selected tabIndex (roving: 0 on the focused item, −1 elsewhere) data-key` → `div.node.<kind>` + nested `ul role="group"`. Subnet with `tasks === 0` renders `ul > li role="none" > div.node.none` "No running tasks". The toggle button (`−`/`+`, `aria-label` "Collapse"/"Expand", `tabIndex=-1`) and the name link (`tabIndex=-1`) are mouse targets; keyboard works on the treeitem: ArrowDown/ArrowUp move across visible items in document order, ArrowRight expands a closed node or moves to its first child, ArrowLeft collapses an open node or moves to the parent, Home/End first/last visible, Enter/Space → `navigate(nodeHref(node))` when it has one, else toggle. Focus ring visible (`:focus-visible` outline accent).

Node contents (mockup l.1525–1542, `network-tree.png`): `span.kind` uppercase label ("VPC", "Availability zone", "Private subnet"/"Public subnet", "Cluster", "Service", "Standalone tasks", "No subnet"); `div.nh` name + pill/tag + toggle; `span.ns` lines: VPC `vpc_id · cidr` and `{zones} zones · {tasks} tasks`; AZ `{n} subnet(s) · {tasks} tasks`; subnet `subnet_id · cidr`, `{ips_used} of {ips_total} IPs used` and an `.ipbar` whose fill width = `max(2, used/total·100)%`; cluster `{services} service(s) · {tasks} tasks` + `TagChip`; service `{tasks} task(s) here · :{revision}` + status `Pill`; task node is a compact mono chip `{label} {private_ip}` (+ muted `group` for standalone), class `unhealthy` when flagged.

CSS approach (port the mockup rules l.359–387 into the module, tokens from `tokens.css`): `ul` is `display:flex; flex-direction:column; gap:8px; padding-left:30px; position:relative`; `li` is `display:flex; align-items:center; position:relative` so the nested `ul` sits to the right of its node; connector lines are pseudo-elements only: `li > ul::before` horizontal 15 px stub leaving the parent node at 50 % height; `ul ul > li::before` horizontal 15 px stub entering each child at 50 %; `ul ul > li::after` vertical line from `top:-4px` to `bottom:-4px`, trimmed to `top:50%` on `:first-child` and `bottom:50%` on `:last-child`, hidden on `:only-child`; line colour `var(--border-strong)`, 1 px. Kind colours: `.vpc` 2 px accent border + `--accent-soft`; `.az` `--raised`; `.subnet.public` 4 px left border `--success`, `.subnet.private` `--accent`; `.cluster` `--series-3`; `.service` `--series-2`; `.task.unhealthy` border `--warning` + `--warning-soft`; `.none` dashed, italic, muted. `@media (prefers-reduced-motion: no-preference)` only for the toggle transition.

- [ ] **Step 1: Write the failing tests** (fixture: the Task 5 example tree with 2 AZs, 3 subnets, one empty subnet, one standalone node, one unhealthy task)

`Tree.test.tsx`:
- `renders ARIA tree roles and levels` (`role=tree`, VPC item `aria-level=1`, subnet `aria-level=3`, `aria-expanded="true"` on nodes with children, none on task items).
- `shows "No running tasks" under an empty subnet` (`role=none` item).
- `renders IP usage text and bar width` ("27 of 251 IPs used", fill `style.width === "10.76%"` rounded to 2 decimals, and `2%` minimum for 1/4091).
- `toggle hides children` (click `−` on payments-api → its task items gone, button now labelled "Expand").
- `collapsed set hides groups but keeps nodes` (closed = service keys → no task items, services present).
- `marks unhealthy task` (class contains `unhealthy`).
- `standalone node lists tasks with group` ("Standalone tasks", "family:report-backfill").
- `keyboard: ArrowDown/ArrowUp move focus, ArrowLeft collapses then moves to parent, ArrowRight expands then enters, Home/End`.
- `Enter on a service item navigates to /services/:id/overview`.

`useTreeState.test.ts`: `starts collapsed to services when collapsedByDefault`; `expandAll clears`; `collapseToServices closes service and standalone keys only`; `keeps closed keys when the root object is replaced`; `resets when resetKey changes`.

- [ ] **Step 2: Run `npm --prefix web run test -- tree`; expect FAIL.**
- [ ] **Step 3: Implement the five files and the CSS module.**
- [ ] **Step 4: Run tests, `lint`, `typecheck`; expect PASS. Open `make run` later (Task 12) to eyeball against `network-tree.png`.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): accessible left-to-right network tree with CSS connector lines"`

---

### Task 12: VPC page, routes — *Implementer: Haiku*

**Files:**
- Create: `web/src/app/networks/NetworkVpcPage.tsx`
- Modify: `web/src/app/router.tsx` (add `/networks`, `/networks/:accountId/:region`, `/networks/:accountId/:region/:vpcId`, `/storage` → Task 13's page), `web/src/app/shell/Sidebar.tsx` (enable the Storage item 01 renders disabled with title "Phase 5"; extend 01's `Shell.test.tsx` with `Storage link is enabled`)
- Test: `web/src/app/networks/NetworkVpcPage.test.tsx`

**Interfaces:**
- Consumes: `useVpcTree`, `useTreeState`, `Tree`, `serviceKeys`, 01's page-head markup and `useCrumbs`, `Card`, `EmptyState`, `ApiError`.
- Produces: the page. Header: title = VPC name; meta spans `vpc_id` (mono), `cidr`, "{n} subnets", "{n} running tasks", edges rendered as "Internet gateway" (if any igw), "NAT gateway" / "{n} NAT gateways", "Endpoints: ecr.api, ecr.dkr, logs, ssmmessages" (sorted, only when any); note "{n} tasks without a subnet not shown" when `unplaced_tasks > 0`. Actions: buttons **Expand all** and **Collapse to services**. Card "Network tree" with right-side muted "VPC ▸ availability zone ▸ subnet ▸ cluster ▸ service ▸ task"; body = `Tree`. Breadcrumbs Networks / account / region / VPC. 404 → `EmptyState` "No running tasks you can see in this VPC." with link "Back to {region}"; 422/other → the shared error state.

- [ ] **Step 1: Write the failing tests**: `renders header meta and edge summary` ("Internet gateway", "NAT gateway", "Endpoints: ecr.api, ecr.dkr, logs, ssmmessages", "10 running tasks"); `Collapse to services hides task nodes, Expand all restores`; `starts collapsed when collapsed_by_default`; `shows empty state on 404` (mock `ApiError{status: 404}` → text and link `/networks/482109375512/us-east-1`); `routes resolve` (render router at `/networks/482109375512/us-east-1/vpc-0a41c7e29b3d` → page present).
- [ ] **Step 2: Run `npm --prefix web run test -- NetworkVpcPage`; expect FAIL.**
- [ ] **Step 3: Implement the page and routes.**
- [ ] **Step 4: Run tests, `lint`, `typecheck`; expect PASS. `make run`, open `/networks` → homelab-admin → us-east-1 → homelab-main and compare with `network-tree.png` (node colours, connector lines, horizontal scroll at 1280 px).**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): VPC tree page with expand/collapse and edge summary"`

---

### Task 13: Storage page — *Implementer: Haiku (escalate to Sonnet if the chart props differ from the assumptions)*

**Files:**
- Create: `web/src/app/storage/StoragePage.tsx`, `EfsCard.tsx`, `format.ts`, `storage.module.css`
- Test: `web/src/app/storage/StoragePage.test.tsx`, `format.test.ts`

**Interfaces:**
- Consumes: `useStorage`, `HBars`, `Donut`, `Sparkline`, `Card`, `Pill`, 01's page-head markup, `EmptyState`.
- Produces: `formatSize(bytes: number): string` (same rule as Go `FormatSize`), `formatMiBps(v: number): string` (`fmtNum` rule: ≥ 10 → integer, else 1 decimal, suffix " MiB/s"), `EfsCard({ fs: StorageItem })`.

Layout (`storage.png`, mockup l.1552–1568): meta "EFS file systems and the services that mount them. File systems are created by Terraform."; grid `g-wide` with card "Stored data by file system" (`HBars`: name, sub = region, value = `size_bytes`, label = `formatSize`, colour = series palette for mounted, `--border-strong` for not mounted; card right text "{formatSize(totals.size_bytes)} in total") and card "Mounted or not" (`Donut` segments "Mounted by a service" accent / "Not mounted, still billed" warning; label = count, sub "file systems"). Then `.cards` of `EfsCard`: top = name, mono `file_system_id · region · vpc.name` (vpc null → omit), pill `ok` "{n} client(s)" (`client_connections ?? mounting_tasks`) or `warn` "Not mounted"; `mini2` grid: "Size, 30 days" bold `formatSize(size_bytes)` + `Sparkline(size_30d)` + "{±n}% in 30 days" (hidden when `growth_30d_pct` null); "Throughput, 24 h" bold `formatMiBps(last)` + `Sparkline(throughput_24h, --series-4)` + `throughput_mode`; when both series are null: the text "Metrics unavailable" in place of the sparklines; "Access points": mono lines `id · path · uid {n}` ("uid —" when null) or muted "None"; "Mounted by": lines `<a service>` `(cluster) → <mono path>` or warning-coloured "No service mounts this file system.". No items → `EmptyState` "No file systems you can see."

- [ ] **Step 1: Write the failing tests**

`format.test.ts`: `formatSize` rows from Task 6; `formatMiBps(29.4) === "29 MiB/s"`, `formatMiBps(5.37) === "5.4 MiB/s"`.
`StoragePage.test.tsx` (fixture = 5 mockup file systems): renders the five bar labels and "337.0 GiB in total"; donut legend "Mounted by a service" with 4 and "Not mounted, still billed" with 1; jenkins-home card shows "fs-0c91a4e7 · us-east-1 · shared-services", pill "1 client", "+29% in 30 days", "Elastic", "fsap-0a1b2c3d4e · /jenkins · uid 1000", link "jenkins-controller" to `/services/<id>/overview` followed by "(ci-tools) →" and "/var/jenkins_home"; uploads-archive shows pill "Not mounted", "None", "No service mounts this file system."; `shows Metrics unavailable when series are null`; `shows empty state with no items`.

- [ ] **Step 2: Run `npm --prefix web run test -- storage`; expect FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run tests, `lint`, `typecheck`; expect PASS. `make run` → `/storage`, compare with `storage.png`.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): storage page with EFS cards, size and mount charts"`

---

### Task 14: "Network tree →" links from service and cluster pages — *Implementer: Haiku*

**Files:**
- Modify: `web/src/app/services/OverviewTab.tsx` (01's Networking card), `web/src/app/clusters/Cluster.tsx`, `web/src/api/types.ts` (`ServiceDTO.networking` and `ClusterDTO` gain optional `vpc`, `security_group_names`)
- Test: extend 01's `web/src/app/services/ServicePage.test.tsx` and `web/src/app/clusters/Cluster.test.tsx`

**Interfaces:**
- Consumes: Task 8 response additions.
- Produces: in the Networking card head, right-aligned link "Network tree →" to `/networks/:accountId/:region/:vpcId` when `networking.vpc` is set; the VPC row shows `vpc.name` + mono `vpc_id`; security group rows render `sg-… (name)` using `security_group_names` (mockup l.1217–1220). Cluster header VPC link (spec §6.3) → same route; absent when `vpc` is null.

- [ ] **Step 1: Write the failing tests**: `shows Network tree link when vpc present` (href exact), `omits the link when vpc is null`, `renders security group names`, `cluster header links to the VPC tree`.
- [ ] **Step 2: Run `npm --prefix web run test -- ServicePage Cluster`; expect FAIL.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run tests, `lint`, `typecheck`; expect PASS.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): link service and cluster pages to the VPC network tree"`

---

### Task 15: End-to-end drill-down and storage against the fake estate — *Implementer: Haiku*

**Files:**
- Create: `web/e2e/networks.spec.ts`, `web/e2e/storage.spec.ts`

**Interfaces:**
- Consumes: 01's Playwright setup (`make e2e`, `login(page, user)` with password `harbormaster-e2e-1`, devseed `pavel` and `omer`).

- [ ] **Step 1: Write the specs**

`networks.spec.ts`:
- `admin drills from accounts to the homelab-main tree`: `/networks` → click us-east-1 under homelab-admin → click homelab-main → heading "homelab-main", "10 running tasks", `role=tree` present, task chips visible; click "Collapse to services" → no task chips; "Expand all" → back; press ArrowDown twice on the focused VPC item → focus moves (assert `document.activeElement` text changes); click "payments-api" → URL `/services/.../overview`.
- `dev user does not see Prod clusters in the tree`: as `omer`, homelab-main tree has "background-jobs" and no "core-services"; `/networks/482109375512/us-east-1/vpc-5c1e0f2a` shows "No running tasks you can see in this VPC.".
- `service networking card links to the tree`: open payments-api → click "Network tree →" → VPC page.

`storage.spec.ts`:
- `admin sees five file systems including Not mounted`: `/storage` → 5 cards, "uploads-archive" with "Not mounted", "337.0 GiB in total".
- `dev user sees four`: as `omer`, no "uploads-archive".

- [ ] **Step 2: Run `make build && cd web && npx playwright test -g "networks|storage"`; expect PASS (the fake estate is already seeded by Tasks 2 and 6). Fix seed mismatches in the fake, not in the tests.**
- [ ] **Step 3: Run the full suites once: `make test`, `make e2e`, `make lint`. All green.**
- [ ] **Step 4: Commit** — `git commit -m "test(e2e): networks drill-down and storage page against the fake estate"`

---

## Self-review

- **Spec coverage:** §5.1 Network/Storage rows → Tasks 3, 4; §6.0 collapsed > 150 → Tasks 5, 11; §6.6 three levels, node contents, buttons, "No running tasks", horizontal scroll, click-through, edge header → Tasks 5, 7, 10–12; §6.7 everything incl. "Not mounted" admins-only → Tasks 6, 8, 13; §3 rules 4–7 → Tasks 5, 6, 7, 8 tests with `devOnly`/`prodOnly`/`none` subjects; §12 cache tables → Task 1; §13 four routes → Tasks 7, 8; §14.1 keyboard tree → Task 11; §6.3/§6.4 VPC links → Task 14; fake estate for `make run` → Tasks 2, 6. Not covered on purpose: security groups of standalone tasks (needs `DescribeNetworkInterfaces`, decision 7).
- **Type consistency:** `network.Node` JSON ↔ `TreeNode` TS union (same kinds, snake_case fields, `tasks` on every node); `storage.FormatSize` ↔ `formatSize`; store signatures reused verbatim by Tasks 3, 4, 7, 8; `useTreeState`/`Tree`/`serviceKeys` names match between Tasks 11 and 12.
- **Review Focus:** each line has its test in Tasks 3, 4, 5, 7, 8, 13 as listed.
- **Proportion:** no function bodies; tests are names + assertions; the only data blocks are the fake-estate tables and one JSON contract excerpt.
