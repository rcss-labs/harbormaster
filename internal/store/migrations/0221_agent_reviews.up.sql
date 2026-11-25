-- Scheduled AI reviews (phase 3). Additive: older tasks never read these tables. The type list below is replaced by
-- phase 4, which makes review types data.

create table agent_reviews (
  id bigserial primary key,
  name text not null check (char_length(name) between 1 and 80),
  type text not null check (type in ('cost', 'security', 'reliability', 'capacity')),
  enabled boolean not null default true,
  cadence text not null check (cadence in ('daily', 'weekly', 'monthly')),
  at_minute int not null check (at_minute between 0 and 1439),     -- local minutes after midnight
  weekday int check (weekday between 1 and 7),                     -- Monday = 1, weekly only
  month_day int check (month_day between 1 and 28),                -- monthly only
  timezone text not null check (timezone <> ''),                   -- an IANA name; the API validates it
  -- Occurrences at or before this time are never recorded: set when the schedule changes or the review is re-enabled.
  schedule_changed_at timestamptz not null default now(),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  updated_by bigint,
  check ((cadence = 'weekly') = (weekday is not null)),
  check ((cadence = 'monthly') = (month_day is not null))
);
create unique index agent_reviews_name_idx on agent_reviews (lower(name));

-- The focus of a review: the shape of notification_route_scopes, for three kinds and without a threshold. Natural
-- keys and no foreign key, so a deleted tag or cluster keeps its entry and is reported as missing.
create table agent_review_scopes (
  review_id bigint not null references agent_reviews on delete cascade,
  kind text not null check (kind in ('account', 'cluster', 'harbormaster')),
  account_id text, region text, cluster_name text,
  tag_id bigint,
  check (
    (kind = 'account' and account_id is not null and cluster_name is null and tag_id is null)   -- region null = all regions
    or (kind = 'cluster' and account_id is not null and region is not null and cluster_name is not null and tag_id is null)
    or (kind = 'harbormaster' and tag_id is not null and account_id is null and region is null and cluster_name is null)
  )
);
create unique index agent_review_scopes_unique_idx on agent_review_scopes
  (review_id, kind, coalesce(account_id, ''), coalesce(region, ''), coalesce(cluster_name, ''), coalesce(tag_id, 0));
create index agent_review_scopes_tag_idx on agent_review_scopes (tag_id) where tag_id is not null;

-- Where the result goes. Deleting a route detaches it; a review left with no route stays and sends nothing.
create table agent_review_routes (
  review_id bigint not null references agent_reviews on delete cascade,
  route_id bigint not null references notification_routes on delete cascade,
  primary key (review_id, route_id)
);
create index agent_review_routes_route_idx on agent_review_routes (route_id);

-- One row per review and period; a claim leases it to one worker, behind claim_seq. Name and type are snapshots, so
-- the row outlives its review.
create table agent_review_runs (
  id bigserial primary key,
  review_id bigint references agent_reviews on delete set null,
  review_name text not null,
  review_type text not null,
  period_key text not null,
  period_label text not null,
  scheduled_for timestamptz not null,
  manual boolean not null default false,
  state text not null default 'requested' check (state in ('requested', 'running', 'done', 'failed', 'skipped')),
  reason text not null default '',
  attempts int not null default 0 check (attempts between 0 and 2),
  claim_seq bigint not null default 0,            -- +1 on every claim: the fence every write by a holder carries
  claimed_until timestamptz,                      -- lease while running; retry time while requested
  run_id uuid,                                    -- latest attempt; no foreign keys, runs are deleted separately
  prev_run_id uuid,                               -- the attempt before it, kept for its cost
  provider text not null default '',
  model text not null default '',
  scope_label text not null default '' check (char_length(scope_label) <= 300),
  covered_tags bigint[] not null default '{}',
  services_total int not null default 0,
  range text not null default '',
  summary text not null default '' check (char_length(summary) <= 1200),
  summary_replaced boolean not null default false,
  evidence_dropped int not null default 0,
  delivery_planned_at timestamptz,
  requested_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz
);
create unique index agent_review_runs_period_idx on agent_review_runs (review_id, period_key) where review_id is not null;
create index agent_review_runs_open_idx on agent_review_runs (requested_at) where state in ('requested', 'running');
create index agent_review_runs_finished_idx on agent_review_runs (finished_at);
create index agent_review_runs_list_idx on agent_review_runs (requested_at desc, id desc);
create index agent_review_runs_review_idx on agent_review_runs (review_id, requested_at desc);
create index agent_review_runs_started_idx on agent_review_runs (started_at) where started_at is not null;

-- The columns and checks of agent_investigation_evidence; a review cites View evidence only.
create table agent_review_evidence (
  id bigserial primary key,
  review_run_id bigint not null references agent_review_runs(id) on delete cascade,
  ordinal int not null check (ordinal between 1 and 12),
  tool text not null,
  action text not null check (action = 'view'),
  account_id text not null check (account_id <> ''),
  region text not null check (region <> ''),
  cluster_name text not null check (cluster_name <> ''),
  service_name text not null check (service_name <> ''),
  note text not null default '' check (char_length(note) <= 300),
  excerpt text not null default '' check (octet_length(excerpt) <= 1500),
  unique (review_run_id, ordinal)
);
