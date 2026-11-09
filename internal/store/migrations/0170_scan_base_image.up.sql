-- Base image origin: which findings come from the Dockerfile's base image and which from the app layers.
-- Layers are diff ids ("sha256:..."), listed base first.

alter table scan_findings add column layer_id text;

-- The image's own layers and the base as it was scanned (the base ref the last scan used, with its layers or error).
alter table scan_images
  add column layer_ids text[],
  add column base_ref text,
  add column base_digest text,
  add column base_layer_ids text[],
  add column base_error text;

-- The base image a user configured for a repository. Keyed by the repository, so every tag and digest of it shares the
-- base. Not pruned: like service_scan_settings it is a setting, not scan data.
create table scan_base_images (
  registry_account_id text not null,
  registry_region text not null,
  repository text not null,
  base_ref text not null,
  updated_by bigint references users (id) on delete set null,
  updated_at timestamptz not null,
  primary key (registry_account_id, registry_region, repository)
);
