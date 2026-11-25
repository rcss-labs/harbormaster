-- Automatic alert investigations (phase 2). Additive: older tasks name their columns and never see these tables.

-- The per-route switch. Older tasks select and update named columns only, so they neither read nor reset it.
alter table notification_routes add column investigate boolean not null default false;

-- One investigation per subject and window. A request is a row in 'requested'; a claim leases it to one worker.
create table agent_investigations (
  id bigserial primary key,
  subject_kind text not null check (subject_kind in ('service', 'cluster', 'account')),
  -- service/<account>/<region>/<cluster>/<service>, cluster/<account>/<region>/<cluster>, account/<account>/<region or empty>
  subject_key text not null,
  account_id text not null,
  region text not null default '',
  cluster_name text not null default '',
  service_name text not null default '',
  members jsonb not null default '[]' check (jsonb_typeof(members) = 'array' and jsonb_array_length(members) <= 50),
  alert_kind text not null,
  alert_title text not null,
  alert_text text not null,
  alert_opened_at timestamptz not null,           -- snapshot of the first incident
  state text not null default 'requested' check (state in ('requested', 'running', 'done', 'failed', 'skipped')),
  reason text not null default '',
  attempts int not null default 0 check (attempts between 0 and 2),
  claimed_until timestamptz,                      -- lease while running; retry time while requested
  claim_seq bigint not null default 0,            -- +1 on every claim: the fence every write by a holder carries
  run_id uuid,                                    -- latest attempt; no foreign key, runs are deleted separately
  provider text not null default '',
  model text not null default '',
  summary text not null default '' check (char_length(summary) <= 1200),
  summary_replaced boolean not null default false,
  evidence_dropped int not null default 0,
  requested_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz
);
create index agent_investigations_subject_idx on agent_investigations (subject_key, requested_at desc);
create index agent_investigations_service_idx on agent_investigations (account_id, region, cluster_name, service_name, requested_at desc);
create index agent_investigations_open_idx on agent_investigations (requested_at) where state in ('requested', 'running');
create index agent_investigations_finished_idx on agent_investigations (finished_at);
create index agent_investigations_list_idx on agent_investigations (requested_at desc, id desc);
create index agent_investigations_started_idx on agent_investigations (started_at) where started_at is not null;

-- An incident joins at most one investigation. Deleting either side removes only the link.
create table agent_investigation_incidents (
  investigation_id bigint not null references agent_investigations(id) on delete cascade,
  incident_id bigint primary key references notification_incidents(id) on delete cascade
);
create index agent_investigation_incidents_investigation_idx on agent_investigation_incidents (investigation_id);

-- The only tool-derived text kept: at most 12 items, 1500 bytes each, labelled by code and never by the model.
create table agent_investigation_evidence (
  id bigserial primary key,
  investigation_id bigint not null references agent_investigations(id) on delete cascade,
  ordinal int not null check (ordinal between 1 and 12),
  tool text not null,
  action text not null check (action in ('view', 'logs')),
  account_id text not null check (account_id <> ''),
  region text not null check (region <> ''),
  cluster_name text not null check (cluster_name <> ''),
  service_name text not null check (service_name <> ''),
  note text not null default '' check (char_length(note) <= 300),
  excerpt text not null default '' check (octet_length(excerpt) <= 1500),
  unique (investigation_id, ordinal)
);
