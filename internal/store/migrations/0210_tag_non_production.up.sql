-- A tag is production unless an admin says otherwise: only services under a non-production tag can be
-- suggested for Fargate Spot. Dev is the built-in non-production tag.
alter table tags add column non_production boolean not null default false;
update tags set non_production = true where builtin and lower(name) = 'dev';
