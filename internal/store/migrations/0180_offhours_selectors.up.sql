-- Off-hours schedules select services by a list of tags (OR) instead of one exact AWS tag, and can exclude
-- individual services. A tag is either an AWS service tag (key = value) or a Harbormaster tag (the internal tags
-- table, matched against a service's effective tag).

create table offhours_schedule_tags (
  schedule_id bigint not null references offhours_schedules on delete cascade,
  kind text not null check (kind in ('aws', 'harbormaster')),
  aws_key text,
  aws_value text,
  -- Deleting a Harbormaster tag removes the selector; the schedule then matches one tag fewer.
  tag_id bigint references tags on delete cascade,
  check (
    (kind = 'aws' and aws_key is not null and aws_value is not null and tag_id is null)
    or (kind = 'harbormaster' and aws_key is null and aws_value is null and tag_id is not null)
  )
);

create unique index offhours_schedule_tags_unique_idx on offhours_schedule_tags
  (schedule_id, kind, coalesce(aws_key, ''), coalesce(aws_value, ''), coalesce(tag_id, 0));

create index offhours_schedule_tags_tag_idx on offhours_schedule_tags (tag_id) where tag_id is not null;

create table offhours_schedule_exclusions (
  schedule_id bigint not null references offhours_schedules on delete cascade,
  account_id text not null,
  region text not null,
  cluster_name text not null,
  service_name text not null,
  primary key (schedule_id, account_id, region, cluster_name, service_name)
);

insert into offhours_schedule_tags (schedule_id, kind, aws_key, aws_value)
  select id, 'aws', tag_key, tag_value from offhours_schedules;

-- Dropping the columns also drops unique (tag_key, tag_value); the name stays unique.
alter table offhours_schedules drop column tag_key, drop column tag_value;
