-- SAML single sign-on (plan 07): users carry two independent credentials,
-- sessions record how they were opened, and the SAML configuration, SP keys,
-- request state and replay cache get their own tables.

-- users: two independent credentials (D6, D11)
alter table users alter column password_hash drop not null;              -- NULL = no password
alter table users add column break_glass bool not null default false;
alter table users add column idp_entity_id text;                          -- the three identity columns are NULL or set together
alter table users add column external_id text;                            -- NameID value
alter table users add column external_id_format text;
alter table users add column email text;
alter table users add column display_name text;
alter table users add column pending_link_name_id text;                   -- admin pre-link (D11)
alter table users add column pending_link_set_at timestamptz;
alter table users add column pending_link_expires_at timestamptz;
alter table users add column pending_link_set_by_user_id bigint;
create unique index users_external_id_idx on users (idp_entity_id, external_id) where external_id is not null;
create unique index users_pending_link_idx on users (pending_link_name_id) where pending_link_name_id is not null;
alter table users add constraint users_identity_shape check (
  (idp_entity_id is null and external_id is null and external_id_format is null) or
  (idp_entity_id is not null and external_id is not null and external_id_format is not null));
alter table users add constraint users_has_credential check (password_hash is not null or external_id is not null);
alter table users add constraint users_break_glass_needs_password check (not break_glass or password_hash is not null);
alter table users add constraint users_pending_needs_unlinked check (pending_link_name_id is null or external_id is null);
alter table users add constraint users_pending_shape check (
  (pending_link_name_id is null) = (pending_link_expires_at is null));

-- sessions
alter table sessions add column auth_source text not null default 'password'
  check (auth_source in ('password','saml'));
alter table sessions add column not_after timestamptz;

-- single-row configuration
create table saml_config (
  id smallint primary key check (id = 1),
  enabled bool not null default false,
  idp_entity_id text not null,
  idp_sso_redirect_url text not null,
  idp_sso_post_url text,
  idp_certs text[] not null,                     -- PEM, 1..5 signing certificates
  metadata_url text,
  metadata_xml text,
  metadata_fetched_at timestamptz,
  sp_entity_id text not null,                    -- default {base_url}/api/auth/saml/metadata
  name_id_format text not null default 'unspecified'
    check (name_id_format in ('unspecified','email','persistent')),
  attr_email text,                               -- NULL = try the well-known names
  attr_display_name text,
  attr_username text,                            -- NULL = derive (D6 rule 3)
  clock_skew_seconds int not null default 120 check (clock_skew_seconds between 0 and 300),
  button_label text not null default 'Sign in with SSO',
  updated_at timestamptz not null,
  updated_by_user_id bigint
);

create table saml_sp_keys (
  id bigserial primary key,
  cert_pem text not null,
  key_pem_enc bytea not null,                    -- AES-256-GCM(nonce || ciphertext)
  created_at timestamptz not null default now(),
  retired_at timestamptz
);

create table saml_requests (
  id text primary key,                           -- AuthnRequest ID ("_" + 32 random bytes hex)
  purpose text not null default 'login' check (purpose in ('login','test','link')),
  user_id bigint references users on delete cascade,   -- set for purpose = 'link' only
  return_to text not null,
  binding_hash text not null,                    -- sha256 of the hm_saml_req cookie value
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  result jsonb,                                  -- test outcome, read once
  constraint saml_requests_link_user check ((purpose = 'link') = (user_id is not null))
);
create index saml_requests_expires_idx on saml_requests (expires_at);

create table saml_assertion_ids (
  id text primary key,
  expires_at timestamptz not null
);
create index saml_assertion_ids_expires_idx on saml_assertion_ids (expires_at);
