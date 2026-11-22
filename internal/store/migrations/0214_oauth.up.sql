-- OAuth for MCP connectors. Harbormaster is the authorization server; every secret (code, access and refresh token) is
-- stored only as sha256. Additive: old tasks never read these tables, and widening the api_tokens kind check only admits
-- rows they never write. An old task's token authentication accepts kind 'pat' only, so it ignores OAuth tokens.

-- One row per approval. expires_at is absolute: the user's token maximum at approval time.
create table oauth_grants (
  id bigserial primary key,
  user_id bigint not null references users(id) on delete cascade,
  client_id text not null,
  client_name text not null,
  scope text not null check (scope = 'harbormaster:read'),
  resource text not null,
  auth_source text not null check (auth_source in ('password', 'saml')),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  last_used_at timestamptz
);
create index oauth_grants_user_id_idx on oauth_grants (user_id);
create index oauth_grants_expires_at_idx on oauth_grants (expires_at);

-- Authorization codes: single use (consumed by delete ... returning), 60 seconds, bound to the client, the redirect
-- URI and the PKCE challenge. Only a signed-in user's approval inserts one.
create table oauth_codes (
  code_hash text primary key,
  user_id bigint not null references users(id) on delete cascade,
  client_id text not null,
  client_name text not null,
  redirect_uri text not null,
  code_challenge text not null,
  scope text not null check (scope = 'harbormaster:read'),
  resource text not null,
  auth_source text not null check (auth_source in ('password', 'saml')),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null
);
create index oauth_codes_expires_at_idx on oauth_codes (expires_at);

-- OAuth access and refresh tokens are api_tokens rows tied to their grant; deleting the grant deletes them.
alter table api_tokens drop constraint api_tokens_kind_check;
alter table api_tokens add constraint api_tokens_kind_check check (kind in ('pat', 'oauth_access', 'oauth_refresh'));
alter table api_tokens add column grant_id bigint references oauth_grants(id) on delete cascade;
alter table api_tokens add column rotated_at timestamptz;
alter table api_tokens add constraint api_tokens_grant_matches_kind check ((grant_id is null) = (kind = 'pat'));
create index api_tokens_grant_id_idx on api_tokens (grant_id) where grant_id is not null;
