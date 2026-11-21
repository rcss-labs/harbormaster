-- Alert notifications: routes (one channel each) with a scope, the incidents derived from the alerts table, and the
-- delivery queue and log. Alert derivation is untouched; notifications only read the alerts table.

create table notification_routes (
  id bigserial primary key,
  name text not null check (char_length(name) between 1 and 80),
  enabled bool not null default false,
  destination text not null check (destination in ('slack', 'telegram', 'pagerduty', 'webhook', 'email')),
  config jsonb not null,            -- non-secret: label, recipients, pagerduty_region, url_host, chat_id, thread_id
  secret_enc bytea,                 -- secretbox(JSON {url, routing_key, signing_secret, bot_token}); null for email
  fallback bool not null default false,   -- only when no other route matches (D28)
  min_severity text not null check (min_severity in ('warning', 'critical')),
  kinds text[] not null check (cardinality(kinds) > 0),
  repeat_seconds int not null default 0 check (repeat_seconds in (0, 1800, 7200)),
  quiet_enabled bool not null default false,
  quiet_timezone text not null,
  notify_resolved bool not null default true,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  updated_by bigint
);
create unique index notification_routes_name_idx on notification_routes (lower(name));

-- One row per scope selector; a route with none matches everything. Natural keys, never foreign keys, so a deleted
-- or renamed cluster or service keeps its selector and the API reports it as missing (D27).
create table notification_route_scopes (
  route_id bigint not null references notification_routes on delete cascade,
  kind text not null check (kind in ('account', 'cluster', 'service', 'aws', 'harbormaster')),
  account_id text, region text, cluster_name text, service_name text,
  aws_key text, aws_value text,
  -- Cluster and account entries are threshold entries (D35): one aggregated notification when at least this many
  -- of their services are failing. Service and tag entries notify per alert and have none.
  threshold int check (threshold between 1 and 500),
  -- No foreign key, like the other natural keys: deleting a Harbormaster tag keeps the selector, which then matches
  -- nothing and is reported as missing (D27). Cascading it away would turn a tag-only route into "everything".
  tag_id bigint,
  check ((kind in ('account', 'cluster')) = (threshold is not null)),
  check (
    (kind = 'account' and account_id is not null and cluster_name is null and service_name is null
       and aws_key is null and aws_value is null and tag_id is null)            -- region null = all regions
    or (kind = 'cluster' and account_id is not null and region is not null and cluster_name is not null
       and service_name is null and aws_key is null and aws_value is null and tag_id is null)
    or (kind = 'service' and account_id is not null and region is not null and cluster_name is not null
       and service_name is not null and aws_key is null and aws_value is null and tag_id is null)
    or (kind = 'aws' and aws_key is not null and aws_value is not null and tag_id is null
       and account_id is null and region is null and cluster_name is null and service_name is null)
    or (kind = 'harbormaster' and tag_id is not null and aws_key is null and aws_value is null
       and account_id is null and region is null and cluster_name is null and service_name is null)
  )
);
create unique index notification_route_scopes_unique_idx on notification_route_scopes
  (route_id, kind, coalesce(account_id, ''), coalesce(region, ''), coalesce(cluster_name, ''), coalesce(service_name, ''),
   coalesce(aws_key, ''), coalesce(aws_value, ''), coalesce(tag_id, 0));
create index notification_route_scopes_route_idx on notification_route_scopes (route_id);
create index notification_route_scopes_tag_idx on notification_route_scopes (tag_id) where tag_id is not null;

create table notification_incidents (
  id bigserial primary key,
  account_id text not null, region text not null, cluster_name text not null, service_name text not null default '',
  kind text not null, severity text not null, title text not null, text text not null,
  opened_at timestamptz not null,
  present_since timestamptz not null default now(),  -- last time the alert came back after being absent; settle counts from it
  missing_since timestamptz,        -- set while the alert is absent, cleared when it is back
  resolved_at timestamptz,
  -- Group incidents (D43): one per route and cluster or account entry, open while the entry's threshold is met.
  -- They share this table so the delivery key, plan states, lease, quiet hours and resolve logic apply unchanged.
  -- No foreign key to the route: when the route or the entry goes away the dispatcher stops asking for the group
  -- and it resolves after the grace.
  group_route_id bigint not null default 0,              -- 0 = alert incident; else the route that owns this group
  scope_kind text not null default 'alert' check (scope_kind in ('alert', 'cluster', 'account')),
  threshold int, failing int, total int,                 -- group incidents only
  members jsonb not null default '[]',                   -- [{cluster, service, kind, title, critical}], sorted
  generation int not null default 0,                     -- +1 each time a group gets worse (D36)
  check ((scope_kind = 'alert') = (group_route_id = 0))
);
create unique index notification_incidents_open_idx on notification_incidents
  (group_route_id, account_id, region, cluster_name, service_name, kind) where resolved_at is null;
create index notification_incidents_resolved_idx on notification_incidents (resolved_at) where resolved_at is not null;

create table notification_deliveries (
  id bigserial primary key,
  route_id bigint references notification_routes on delete set null,
  route_name text not null, destination text not null, target text not null,   -- snapshots for the log
  incident_id bigint references notification_incidents on delete cascade,      -- null for a test
  event text not null check (event in ('opened', 'repeat', 'update', 'resolved', 'test')),
  seq int not null default 0,
  severity text not null, alert_title text not null, alert_text text not null,
  account_id text not null default '', region text not null default '',
  cluster_name text not null default '', service_name text not null default '', kind text not null default '',
  status text not null check (status in ('pending', 'held', 'delivered', 'failed', 'cancelled')),
  attempts int not null default 0,
  retries int not null default 0,            -- manual retries
  next_attempt_at timestamptz,
  held_until timestamptz,
  claimed_until timestamptz,
  status_code int, duration_ms int, note text not null default '',
  details jsonb not null default '{}',                   -- {scope, threshold, failing, total, members} snapshot for group messages
  created_at timestamptz not null default now(),
  finished_at timestamptz
);
create unique index notification_deliveries_key_idx on notification_deliveries (route_id, incident_id, event, seq)
  where incident_id is not null and route_id is not null;
create index notification_deliveries_due_idx on notification_deliveries (next_attempt_at) where status = 'pending';
create index notification_deliveries_created_idx on notification_deliveries (created_at desc, id desc);
create index notification_deliveries_failed_idx on notification_deliveries (created_at desc) where status = 'failed';
