-- Which CPU architectures an image supports, read by a leader job from the registry, never on a request. A manifest is
-- immutable per digest, so it is stored once per (registry, repository, digest); image_refs maps the image string of a
-- task definition to the digest it currently resolves to and how the last check went.
create table image_manifests (
  registry text not null,
  repository text not null,
  digest text not null,
  media_type text not null,
  is_index boolean not null,
  platforms text[] not null,            -- empty for a single manifest
  fetched_at timestamptz not null,
  primary key (registry, repository, digest)
);

create table image_refs (
  image text primary key,               -- exactly as written in the task definition
  registry text not null,
  repository text not null,
  digest text,                          -- null until resolved
  status text not null check (status in ('pending', 'ok', 'not_found', 'denied', 'no_permission', 'not_connected',
                                         'registry_not_checked', 'invalid', 'error')),
  detail text,                          -- account id for no_permission/not_connected; never a raw error body
  checked_at timestamptz,
  next_check_at timestamptz not null,
  last_seen_at timestamptz not null
);
create index image_refs_next_check_idx on image_refs (next_check_at);
