-- Cached VPC network topology, polled per account and region. Replaced wholesale per scope.
create table vpcs (
  account_id text not null,
  region text not null,
  vpc_id text not null,
  name text not null,
  cidr text not null,
  is_default bool not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, vpc_id)
);

create table subnets (
  account_id text not null,
  region text not null,
  subnet_id text not null,
  vpc_id text not null,
  name text not null,
  cidr text not null,
  az text not null,
  available_ips int not null,
  public bool not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, subnet_id)
);
create index subnets_vpc_idx on subnets (account_id, region, vpc_id);

-- Internet gateways, NAT gateways and endpoints attached to a VPC.
create table vpc_edges (
  account_id text not null,
  region text not null,
  vpc_id text not null,
  kind text not null check (kind in ('igw', 'nat', 'endpoint')),
  resource_id text not null,
  subnet_id text null,
  detail text not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, resource_id)
);
create index vpc_edges_vpc_idx on vpc_edges (account_id, region, vpc_id);

create table security_groups (
  account_id text not null,
  region text not null,
  group_id text not null,
  vpc_id text not null,
  name text not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, group_id)
);

-- EFS file systems and their access points, polled per account and region.
create table efs (
  account_id text not null,
  region text not null,
  file_system_id text not null,
  arn text not null,
  name text not null,
  size_bytes bigint not null,
  throughput_mode text not null,
  provisioned_mibps numeric null,
  performance_mode text not null,
  lifecycle_state text not null,
  encrypted bool not null,
  vpc_id text not null,
  mount_target_azs text[] not null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, file_system_id)
);

create table efs_access_points (
  account_id text not null,
  region text not null,
  access_point_id text not null,
  file_system_id text not null,
  name text not null,
  root_path text not null,
  posix_uid bigint null,
  posix_gid bigint null,
  raw jsonb not null,
  polled_at timestamptz not null,
  primary key (account_id, region, access_point_id)
);
create index efs_access_points_fs_idx on efs_access_points (account_id, region, file_system_id);
