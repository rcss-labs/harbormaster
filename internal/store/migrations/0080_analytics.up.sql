-- Analytics rollups (phase 2): daily tables kept far longer than the raw rows they are built from,
-- plus a failure reason on deployment history.

-- one row per service per UTC day, rolled up from service_counts_history and stopped tasks
create table service_availability_daily (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  day date not null,
  samples int not null,
  samples_ok int not null,
  minutes_below int not null,
  unexpected_stops int not null,
  primary key (account_id, region, cluster_name, service_name, day)
);
create index service_availability_daily_day_idx on service_availability_daily (day);

-- sums and sample counts, not averages, so each 5-minute sample can update the row idempotently (avg = sum / samples)
create table cluster_capacity_daily (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  day date not null,
  samples int not null,
  vcpu_sum double precision not null,
  vcpu_max double precision not null,
  mem_gib_sum double precision not null,
  mem_gib_max double precision not null,
  tasks_sum int not null,
  tasks_max int not null,
  spot_sum int not null,
  primary key (account_id, region, cluster_name, day)
);
create index cluster_capacity_daily_day_idx on cluster_capacity_daily (day);

-- CloudWatch utilisation per service per day; percent columns are null when Container Insights is off
create table service_util_daily (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  day date not null,
  cpu_avg double precision,
  cpu_p95 double precision,
  mem_avg double precision,
  mem_p95 double precision,
  cpu_reserved_units int,                                   -- from the service's task definition at rollup time
  mem_reserved_mib int,
  primary key (account_id, region, cluster_name, service_name, day)
);
create index service_util_daily_day_idx on service_util_daily (day);

-- why a rollout failed: group is circuit_breaker|health_check|failed_to_start|out_of_memory|stopped_by_user|other
alter table deployment_history add column failure_reason text;
alter table deployment_history add column failure_detail text;                -- raw RolloutStateReason or last stopped task's reason
