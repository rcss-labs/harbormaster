-- Off-hours schedules: scale tagged ECS services down outside working hours.
-- A schedule selects services by one exact AWS service tag (tag_key = tag_value). The enrolment row keeps the
-- capacity saved before scaling down, so a disable or delete can restore it.
-- Foreign keys to users use "on delete set null" so deleting a user never blocks.

create table offhours_schedules (
  id bigserial primary key,
  name text not null unique,
  enabled bool not null default true,
  tag_key text not null,
  tag_value text not null,
  timezone text not null,
  -- 168 flags, index day*24+hour with Monday = day 0; '1' means awake.
  awake text not null check (awake ~ '^[01]{168}$'),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  created_by bigint references users on delete set null,
  updated_by bigint references users on delete set null,
  unique (tag_key, tag_value)
);

create table offhours_enrolments (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  -- NULL means the service is leaving: the reconciler restores it and then deletes the row.
  schedule_id bigint references offhours_schedules on delete set null,
  saved_min int not null,
  saved_max int not null,
  saved_desired int,
  registered_target bool not null,
  awake_tasks int not null,
  state text not null check (state in ('active', 'blocked')),
  blocked_reason text,
  last_error text,
  last_applied_at timestamptz,
  last_checked_at timestamptz,
  woken_until timestamptz,
  woken_by bigint references users on delete set null,
  enrolled_at timestamptz not null default now(),
  primary key (account_id, region, cluster_name, service_name)
);

create index offhours_enrolments_schedule_idx on offhours_enrolments (schedule_id);

-- Serves the exact-tag match: services.raw -> 'Tags' @> [{"Key": k, "Value": v}].
create index services_aws_tags_idx on services using gin ((raw -> 'Tags') jsonb_path_ops);
