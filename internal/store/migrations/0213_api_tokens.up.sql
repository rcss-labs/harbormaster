-- Personal access tokens. Only sha256(secret) is stored; public_id is the display form (hmp_<id>), safe to log.
-- The check constraints are deliberate: a new kind or a write scope needs a migration and a review.
-- Additive only: old tasks ignore these tables during a deploy.
create table api_tokens (
  id bigserial primary key,
  public_id text not null unique,
  user_id bigint not null references users(id) on delete cascade,
  kind text not null check (kind = 'pat'),
  name text not null,
  secret_hash text not null,
  scope text not null check (scope = 'read'),
  auth_source text not null check (auth_source in ('password', 'saml')),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  last_used_at timestamptz,
  last_used_ip text
);
create index api_tokens_user_id_idx on api_tokens (user_id);

-- Bytes scanned by token-initiated log searches, per user and UTC day (the daily budget's ledger).
create table token_log_scan_usage (
  user_id bigint not null references users(id) on delete cascade,
  day date not null,
  bytes_scanned bigint not null default 0,
  searches int not null default 0,
  primary key (user_id, day)
);
