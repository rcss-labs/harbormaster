-- Log search: per-user saved searches (name, query, scope and time range).
-- Rows go with their user, so deleting a user removes their saved searches.

create table log_search_saved (
  id bigserial primary key,
  user_id bigint not null references users(id) on delete cascade,
  name text not null check (char_length(name) between 1 and 80),
  query text not null check (char_length(query) <= 4000),
  -- Array of service id strings.
  service_ids jsonb not null,
  -- '15m' | '1h' | '24h' | 'custom'.
  range text not null,
  -- Only set when range is 'custom'.
  range_from timestamptz,
  range_to timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (user_id, name)
);
