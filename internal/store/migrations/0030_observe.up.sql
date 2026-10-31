-- Cached CloudWatch series, keyed by a caller-built series key.
create table metrics_cache (
  key text primary key,
  account_id text not null,
  region text not null,
  fetched_at timestamptz not null,
  payload jsonb not null
);
create index metrics_cache_fetched_at_idx on metrics_cache (fetched_at);

-- One row per attached target group. Deleted when the group is no longer attached.
create table target_health (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  target_group_arn text not null,
  load_balancer_arn text not null,
  healthy int not null,
  unhealthy int not null,
  targets jsonb not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, cluster_name, service_name, target_group_arn)
);

create table scaling (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  has_target bool not null,
  min_capacity int,
  max_capacity int,
  policies jsonb not null,
  scheduled_actions jsonb not null,
  activities jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, cluster_name, service_name)
);

-- Consecutive-poll bookkeeping for a service's derived status.
create table service_status_runs (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  status text not null,
  since timestamptz not null,
  polls int not null,
  primary key (account_id, region, cluster_name, service_name)
);

-- Alerts are replaced wholesale per target on every poll, so there is no natural key.
create table alerts (
  id bigserial primary key,
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null default '',
  kind text not null,
  severity text not null,
  title text not null,
  text text not null,
  at timestamptz not null,
  tab text not null,
  polled_at timestamptz not null
);
create index alerts_account_region_idx on alerts (account_id, region);
