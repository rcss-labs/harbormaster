create table users (
  id bigserial primary key,
  username text unique not null,
  password_hash text not null,
  is_admin bool not null default false,
  must_change_password bool not null default false,
  created_at timestamptz not null default now(),
  last_sign_in_at timestamptz,
  locked_until timestamptz
);
create unique index users_username_lower_idx on users (lower(username));

create table sessions (
  id_hash text primary key,
  user_id bigint not null references users on delete cascade,
  csrf_token text not null,
  created_at timestamptz not null default now(),
  last_seen_at timestamptz not null default now(),
  expires_at timestamptz not null,
  ip text,
  user_agent text
);
create index sessions_user_id_idx on sessions (user_id);

create table login_attempts (
  id bigserial primary key,
  username text not null,
  ip text not null,
  ts timestamptz not null default now(),
  ok bool not null
);
create index login_attempts_username_ts_idx on login_attempts (username, ts);
create index login_attempts_ip_ts_idx on login_attempts (ip, ts);

create table tags (
  id bigserial primary key,
  name text not null,
  color text not null check (color in ('neutral','red','amber','blue','violet','green')),
  builtin bool not null default false
);
create unique index tags_name_lower_idx on tags (lower(name));
insert into tags (name, color, builtin) values ('Dev', 'neutral', true);

create table cluster_tags (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  tag_id bigint not null references tags on delete cascade,
  orphaned_at timestamptz,
  primary key (account_id, region, cluster_name)
);

create table service_tags (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  tag_id bigint not null references tags on delete cascade,
  orphaned_at timestamptz,
  primary key (account_id, region, cluster_name, service_name)
);

create table permissions (
  user_id bigint not null references users on delete cascade,
  tag_id bigint not null references tags on delete cascade,
  actions smallint not null,
  primary key (user_id, tag_id)
);

create table aws_accounts (
  id bigserial primary key,
  name text not null,
  account_id text not null unique,
  role_arn text not null,
  external_id text not null,
  regions text[] not null,
  enabled bool not null default true,
  created_at timestamptz default now(),
  last_ok_at timestamptz,
  last_error text
);

create table audit_log (
  id bigserial primary key,
  ts timestamptz not null default now(),
  actor_user_id bigint,
  actor_username text,
  action text not null,
  target text,
  summary text not null,
  details jsonb,
  ip text
);
create index audit_log_ts_idx on audit_log (ts desc);

create table operation_log (
  id bigserial primary key,
  ts timestamptz not null default now(),
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text, -- null = standalone-task operation
  service_arn text,
  actor_user_id bigint,
  actor_username text,
  source text not null check (source in ('harbormaster','ecs')),
  text text not null,
  details jsonb
);
create index operation_log_service_ts_idx on operation_log (account_id, region, cluster_name, service_name, ts desc);

create table revision_records (
  task_definition_arn text primary key,
  registered_by_user_id bigint,
  registered_at timestamptz not null
);

create table deployment_history (
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  deployment_id text not null,
  task_definition_arn text,
  revision int,
  kind text not null check (kind in ('deploy','restart','scale')),
  started_at timestamptz not null,
  finished_at timestamptz,
  outcome text check (outcome in ('completed','failed')),
  started_by_user_id bigint,
  primary key (account_id, region, cluster_name, service_name, deployment_id)
);

-- throttled <=> interval_ms > base_interval_ms; failing <=> failures >= 3
create table poll_loops (
  account_id text not null,
  region text not null,
  tier text not null,
  last_ok_at timestamptz,
  last_error text,
  interval_ms int not null,
  base_interval_ms int not null,
  failures int not null default 0,
  updated_at timestamptz not null,
  primary key (account_id, region, tier)
);
