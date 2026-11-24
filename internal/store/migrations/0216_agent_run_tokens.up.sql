-- Run tokens: short-lived read-only tokens the agent service uses to call the read API as one user for one agent run.
-- Stored like every other api_tokens row (only sha256 of the secret). Additive: old tasks never write kind 'run', and
-- their token authentication accepts kinds 'pat' and 'oauth_access' only, so they ignore these rows.
-- run_id names the agent run the token belongs to; it has no foreign key because the runs table arrives in a later
-- migration and the token is deleted by run id when the run ends.
alter table api_tokens drop constraint api_tokens_kind_check;
alter table api_tokens add constraint api_tokens_kind_check check (kind in ('pat', 'oauth_access', 'oauth_refresh', 'run'));
alter table api_tokens add column run_id uuid;
alter table api_tokens add column redact_env boolean not null default true;
alter table api_tokens drop constraint api_tokens_grant_matches_kind;
alter table api_tokens add constraint api_tokens_grant_matches_kind check ((grant_id is null) = (kind in ('pat', 'run')));
alter table api_tokens add constraint api_tokens_run_matches_kind check ((run_id is not null) = (kind = 'run'));
create index api_tokens_run_id_idx on api_tokens (run_id) where run_id is not null;
