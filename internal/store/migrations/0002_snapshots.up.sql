create table clusters (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  arn text,
  status text,
  running_tasks int,
  pending_tasks int,
  active_services int,
  container_insights bool,
  capacity_providers text[],
  content_hash text not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, cluster_name)
);

create table services (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  arn text,
  status text,
  desired int,
  running int,
  pending int,
  task_definition_arn text,
  family text,
  revision int,
  launch_type text,
  platform_version text,
  scheduling_strategy text,
  deployment_controller text,
  min_healthy_percent int,
  max_percent int,
  circuit_breaker_enable bool,
  circuit_breaker_rollback bool,
  enable_execute_command bool,
  subnets text[],
  security_groups text[],
  assign_public_ip bool,
  load_balancers jsonb,
  deployments jsonb,
  created_at timestamptz,
  content_hash text not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, cluster_name, service_name)
);

create table service_events (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  event_id text not null,
  created_at timestamptz,
  message text,
  content_hash text not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, cluster_name, service_name, event_id)
);
create index service_events_created_at_idx on service_events (created_at desc);

create table tasks (
  account_id text not null,
  region text not null,
  task_arn text not null,
  cluster_name text not null,
  service_name text, -- null = standalone
  "group" text,
  started_by text,
  task_definition_arn text,
  family text,
  revision int,
  last_status text,
  desired_status text,
  health_status text,
  launch_type text,
  capacity_provider text,
  cpu text,
  memory text,
  availability_zone text,
  subnet_id text,
  private_ip text,
  public_ip text,
  eni_id text,
  created_at timestamptz,
  started_at timestamptz,
  stopping_at timestamptz,
  stopped_at timestamptz,
  stop_code text,
  stopped_reason text,
  stop_reason_group text,
  exit_code int,
  containers jsonb,
  content_hash text not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, task_arn)
);
create index tasks_service_idx on tasks (account_id, region, cluster_name, service_name);
create index tasks_last_status_idx on tasks (last_status);
create index tasks_stopped_at_idx on tasks (stopped_at desc);

create table service_counts_history (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  ts timestamptz not null,
  running int,
  desired int,
  pending int,
  primary key (account_id, region, cluster_name, service_name, ts)
);

create table task_definitions (
  account_id text not null,
  region text not null,
  arn text not null,
  family text,
  revision int,
  cpu text,
  memory text,
  network_mode text,
  task_role_arn text,
  execution_role_arn text,
  requires_compatibilities text[],
  containers jsonb,
  efs_volumes jsonb,
  registered_at timestamptz,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, arn)
);
