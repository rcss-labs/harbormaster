-- Ownership of polling work. One row per account x region ("slice") plus one coordinator row. The owner's epoch is the
-- fencing token: it rises on every change of owner, and every poller write checks it inside its own transaction, so a
-- paused owner can never commit after a takeover. Time is always the database's now(); no task clock takes part.
-- Additive only: old tasks (which use the advisory leader lock) and new tasks overlap during a deploy.
create table poll_leases (
  kind text not null check (kind in ('slice', 'coordinator')),
  account_id text not null default '',  -- '' for the coordinator row
  region text not null default '',      -- '' for the coordinator row
  owner_id uuid,                        -- the holding process, null when free
  epoch bigint not null default 0,      -- +1 on every change of owner
  expires_at timestamptz not null default now(),
  acquired_at timestamptz,
  renewed_at timestamptz,
  primary key (kind, account_id, region)
);

-- One row per running process, beaten every 10 s; the gauges feed the status page and the rebalance planner.
create table poll_members (
  instance_id uuid primary key,         -- new on every process start
  roles text[] not null,
  version text not null,
  started_at timestamptz not null default now(),
  heartbeat_at timestamptz not null default now(),
  slices int not null default 0,
  weight int not null default 0,
  focus_objects int not null default 0
);
