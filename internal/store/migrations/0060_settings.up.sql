-- Global key/value settings. Absent or unparsable values fall back to code defaults.
create table settings (
  key text primary key,
  value text not null,
  updated_at timestamptz not null
);
