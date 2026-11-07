-- Image security: scanned images, scanner runs, findings and the vulnerability-DB state.
-- Foreign keys to users use "on delete set null" so deleting a user never blocks.

create table scan_images (
  digest text primary key,
  platform_digest text,
  platform text,
  image_ref text not null,
  tag text,
  registry_kind text not null check (registry_kind in ('ecr', 'ecr_public', 'other')),
  registry_account_id text,
  registry_region text,
  repository text,
  role text not null check (role in ('running', 'candidate')),
  status text not null check (status in ('queued', 'scanning', 'scanned', 'failed', 'unsupported', 'aged_out')),
  failure_code text,
  failure_detail text,
  attempts int not null default 0,
  next_attempt_at timestamptz,
  large bool not null default false,
  sbom_key text,
  sbom_generated_at timestamptz,
  syft_version text,
  scanner_image_digest text,
  last_matched_run_id bigint,
  matched_at timestamptz,
  matched_db_built_at timestamptz,
  packages int,
  truncated bool not null default false,
  pushed_at timestamptz,
  candidates_checked_at timestamptz,
  first_seen_at timestamptz not null,
  last_seen_running_at timestamptz,
  requested_by bigint references users (id) on delete set null,
  updated_at timestamptz not null
);
create index scan_images_status_next_attempt_idx on scan_images (status, next_attempt_at);
create index scan_images_repository_idx on scan_images (repository, registry_account_id, registry_region);

create table scan_runs (
  id bigserial primary key,
  kind text not null check (kind in ('scan', 'rematch')),
  task_arn text,
  cluster_arn text,
  batch_size int not null,
  large bool not null default false,
  status text not null check (status in ('launching', 'running', 'succeeded', 'failed', 'timed_out', 'stopped')),
  started_at timestamptz not null,
  finished_at timestamptz,
  exit_code int,
  stop_reason text,
  -- Recorded by the leader at launch from the job, never from a scanner file.
  db_built_at timestamptz,
  db_checksum text,
  grype_version text,
  syft_version text,
  scanner_image_digest text,
  images_ok int,
  images_failed int,
  error text,
  objects_deleted_at timestamptz
);
create index scan_runs_status_idx on scan_runs (status);

-- The batch of a run, so reconcile can requeue it.
create table scan_run_images (
  run_id bigint not null references scan_runs (id) on delete cascade,
  digest text not null,
  primary key (run_id, digest)
);

-- Which candidate images belong to which running image.
create table scan_candidate_links (
  running_digest text not null,
  candidate_digest text not null,
  primary key (running_digest, candidate_digest)
);

create table scan_findings (
  digest text not null,
  vuln_id text not null,
  package_name text not null,
  package_version text not null,
  package_type text,
  location text,
  severity text not null,
  cvss double precision,
  fixed_version text,
  fix_state text,
  data_source text,
  url text,
  first_seen_at timestamptz not null,
  last_seen_at timestamptz not null,
  run_id bigint not null,
  primary key (digest, vuln_id, package_name, package_version)
);

-- Only the best candidate per running image is kept.
create table scan_fix_candidates (
  running_digest text primary key,
  candidate_digest text not null,
  repository text,
  tag text not null,
  pushed_at timestamptz,
  fixes int,
  introduces int,
  open_total int,
  computed_at timestamptz not null,
  computed_against_run_id bigint
);

create table scan_risk_acceptances (
  id bigserial primary key,
  digest text not null,
  vuln_id text not null,
  package_name text not null,
  package_version text not null,
  reason text not null,
  accepted_by bigint references users (id) on delete set null,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  revoked_at timestamptz,
  revoked_by bigint references users (id) on delete set null
);
create index scan_risk_acceptances_digest_vuln_idx on scan_risk_acceptances (digest, vuln_id);

create table scan_db_state (
  id int primary key check (id = 1),
  built_at timestamptz,
  checksum text,
  archive_key text,
  imported_at timestamptz,
  last_refresh_at timestamptz,
  last_refresh_error text,
  scanner_image_digest text,
  engine_requeue_at timestamptz
);
