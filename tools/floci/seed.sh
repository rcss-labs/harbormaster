#!/usr/bin/env bash
# Seeds the local floci emulator with a demo ECS estate for Harbormaster's real AWS code path.
#
# Usage: tools/floci/seed.sh [--help]
#
# Needs floci running (make floci-up). Idempotent: a region whose first cluster already exists is skipped.
# Every aws call uses --profile floci and an explicit --region, against the isolated config in this directory.
# Exit codes: 0 seeded, 2 refused (environment not local floci or bad usage), 1 failure.
set -euo pipefail

usage() { sed -n '2,8p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }
case "${1:-}" in
  -h | --help) usage; exit 0 ;;
  "") ;;
  *) usage >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# env.sh refuses (2) a non-local endpoint or a profile other than floci.
# shellcheck source=env.sh
. "$HERE/env.sh"
PROFILE=floci
[ "${AWS_PROFILE:-}" = "$PROFILE" ] || { echo "seed: AWS_PROFILE is not floci; refusing" >&2; exit 2; }
[ "$(aws --profile "$PROFILE" --region eu-west-1 configure get endpoint_url)" = "http://127.0.0.1:4566" ] \
  || { echo "seed: profile does not target local floci; refusing" >&2; exit 2; }

ACCOUNT=000000000000
ROLE_NAME=harbormaster-access

aws_in() { local region=$1; shift; aws --profile "$PROFILE" --region "$region" "$@"; }

wait_for_floci() {
  local i
  for i in $(seq 1 60); do
    aws_in eu-west-1 ecs list-clusters >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "seed: floci did not answer on 127.0.0.1:4566; run make floci-up" >&2
  exit 1
}

# The role trust is enforced by floci on AssumeRole, so the role must exist before an account is connected.
create_role() {
  local trust='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sts:AssumeRole"}]}'
  if aws_in eu-west-1 iam get-role --role-name "$ROLE_NAME" >/dev/null 2>&1; then
    echo "role $ROLE_NAME already exists"
  else
    aws_in eu-west-1 iam create-role --role-name "$ROLE_NAME" --assume-role-policy-document "$trust" --query Role.Arn --output text
  fi
}

# Container definition JSON for one task definition. $1 service, $2 region, $3 extra mode: plain|payments|efs
container_definitions() {
  local svc=$1 region=$2 mode=$3
  jq -cn --arg svc "$svc" --arg region "$region" --arg mode "$mode" '
    def logcfg($name): {logDriver:"awslogs", options:{"awslogs-group":("/ecs/"+$svc), "awslogs-region":$region, "awslogs-stream-prefix":"ecs"}};
    def main: {name:$svc, image:"nginx:1.27-alpine", essential:true, portMappings:[{containerPort:8080}], logConfiguration:logcfg($svc)};
    if $mode == "payments" then
      [ (main + {
          environment:[{name:"APP_ENV",value:"floci"},{name:"PAYMENT_PROVIDER",value:"stripe"},{name:"LOG_LEVEL",value:"info"}],
          healthCheck:{command:["CMD-SHELL","curl -f http://localhost:8080/healthz || exit 1"], interval:30, timeout:5, retries:3, startPeriod:10}
        }),
        {name:"otel-collector", image:"otel/opentelemetry-collector:0.104.0", essential:false, logConfiguration:logcfg("otel-collector")} ]
    elif $mode == "efs" then
      [ (main + {mountPoints:[{sourceVolume:"shared", containerPath:"/data", readOnly:false}]}) ]
    else [ main ] end'
}

register_task_definition() { # region svc mode [efs-id]
  local region=$1 svc=$2 mode=$3 efs=${4:-}
  local args=(ecs register-task-definition --family "$svc" --network-mode awsvpc --requires-compatibilities FARGATE
    --cpu 512 --memory 1024 --container-definitions "$(container_definitions "$svc" "$region" "$mode")")
  if [ "$mode" = efs ]; then
    args+=(--volumes "[{\"name\":\"shared\",\"efsVolumeConfiguration\":{\"fileSystemId\":\"$efs\",\"transitEncryption\":\"ENABLED\"}}]")
  fi
  aws_in "$region" "${args[@]}" >/dev/null
}

# Seeds the network pieces of one region and sets VPC/subnet/SG/EFS globals.
seed_network() { # region cidr-prefix
  local region=$1 prefix=$2 igw alloc
  VPC=$(aws_in "$region" ec2 create-vpc --cidr-block "$prefix.0.0/16" --query Vpc.VpcId --output text)
  aws_in "$region" ec2 create-tags --resources "$VPC" --tags "Key=Name,Value=apps-$region"
  SUB1=$(aws_in "$region" ec2 create-subnet --vpc-id "$VPC" --cidr-block "$prefix.1.0/24" --availability-zone "${region}a" --query Subnet.SubnetId --output text)
  SUB2=$(aws_in "$region" ec2 create-subnet --vpc-id "$VPC" --cidr-block "$prefix.2.0/24" --availability-zone "${region}b" --query Subnet.SubnetId --output text)
  SUB3=$(aws_in "$region" ec2 create-subnet --vpc-id "$VPC" --cidr-block "$prefix.3.0/24" --availability-zone "${region}c" --query Subnet.SubnetId --output text)
  aws_in "$region" ec2 create-tags --resources "$SUB1" --tags Key=Name,Value=private-a
  aws_in "$region" ec2 create-tags --resources "$SUB2" --tags Key=Name,Value=private-b
  aws_in "$region" ec2 create-tags --resources "$SUB3" --tags Key=Name,Value=public-c
  SG=$(aws_in "$region" ec2 create-security-group --group-name apps --description apps --vpc-id "$VPC" --query GroupId --output text)
  igw=$(aws_in "$region" ec2 create-internet-gateway --query InternetGateway.InternetGatewayId --output text)
  aws_in "$region" ec2 attach-internet-gateway --vpc-id "$VPC" --internet-gateway-id "$igw"
  alloc=$(aws_in "$region" ec2 allocate-address --domain vpc --query AllocationId --output text)
  aws_in "$region" ec2 create-nat-gateway --subnet-id "$SUB3" --allocation-id "$alloc" >/dev/null
  EFS=$(aws_in "$region" efs create-file-system --creation-token "shared-$region" --tags "Key=Name,Value=shared-$region" --query FileSystemId --output text)
  aws_in "$region" efs create-mount-target --file-system-id "$EFS" --subnet-id "$SUB1" --security-groups "$SG" >/dev/null
}

create_service() { # region cluster svc mode
  local region=$1 cluster=$2 svc=$3 mode=$4
  aws_in "$region" logs create-log-group --log-group-name "/ecs/$svc" 2>/dev/null || true
  register_task_definition "$region" "$svc" "$mode" "$EFS"
  aws_in "$region" ecs create-service --cluster "$cluster" --service-name "$svc" --task-definition "$svc" --desired-count 2 \
    --launch-type FARGATE --enable-execute-command \
    --network-configuration "awsvpcConfiguration={subnets=[$SUB1,$SUB2],securityGroups=[$SG]}" >/dev/null
  echo "seeded $region/$cluster/$svc"
}

seed_cluster() { # region cluster insights(true|false) svc[:mode]...
  local region=$1 cluster=$2 insights=$3; shift 3
  local value=enabled; [ "$insights" = true ] || value=disabled
  aws_in "$region" ecs create-cluster --cluster-name "$cluster" --settings "name=containerInsights,value=$value" --tags key=env,value=demo >/dev/null
  local entry
  for entry in "$@"; do
    create_service "$region" "$cluster" "${entry%%:*}" "$( [[ "$entry" == *:* ]] && echo "${entry#*:}" || echo plain )"
  done
}

# Task id (last ARN segment) for each running task of a service, one per line.
running_task_ids() { # region cluster svc
  aws_in "$1" ecs list-tasks --cluster "$2" --service-name "$3" --desired-status RUNNING --query 'taskArns[]' --output text | tr '\t' '\n' | sed 's#.*/##' | sed '/^$/d'
}

# Log streams ecs/<container>/<taskId> with a few INFO/WARN/ERROR lines for each running task of a service.
seed_logs() { # region cluster svc containers...
  local region=$1 cluster=$2 svc=$3; shift 3
  local now container task events
  now=$(($(date +%s) * 1000))
  while read -r task; do
    [ -n "$task" ] || continue
    for container in "$@"; do
      aws_in "$region" logs create-log-stream --log-group-name "/ecs/$svc" --log-stream-name "ecs/$container/$task" 2>/dev/null || true
      events=$(jq -cn --argjson now "$now" --arg svc "$svc" --arg task "$task" '[
        {timestamp:($now-30000), message:("INFO "+$svc+" started task "+$task)},
        {timestamp:($now-20000), message:"INFO GET /healthz 200 2ms"},
        {timestamp:($now-10000), message:"WARN slow request took 812ms"},
        {timestamp:($now-5000),  message:"ERROR upstream timeout after 5000ms"},
        {timestamp:$now,         message:"INFO POST /v1/charges 201 143ms"}]')
      aws_in "$region" logs put-log-events --log-group-name "/ecs/$svc" --log-stream-name "ecs/$container/$task" --log-events "$events" >/dev/null
    done
  done < <(running_task_ids "$region" "$cluster" "$svc")
}

# Container Insights series: 24 points, 5 minutes apart, with service+cluster and cluster-only dimensions.
seed_metrics() { # region cluster svc...
  local region=$1 cluster=$2; shift 2
  local svc batch
  for svc in "$@"; do
    # One put-metric-data call per service keeps each request body small.
    batch=$(jq -cn --arg cluster "$cluster" --arg svc "$svc" '
      [ range(0;24) as $i | (now - 300*(23-$i) | floor | todate) as $ts
        | ( [ {n:"CpuUtilized",   v:(120 + (($i*37) % 90))},
              {n:"MemoryUtilized",v:(380 + (($i*53) % 160))},
              {n:"CpuReserved",   v:512},
              {n:"MemoryReserved",v:1024},
              {n:"RunningTaskCount", v:2} ] )[]
        | . as $m
        | ( [{Name:"ClusterName",Value:$cluster},{Name:"ServiceName",Value:$svc}],
            [{Name:"ClusterName",Value:$cluster}] )
        | {MetricName:$m.n, Dimensions:., Timestamp:$ts, Value:$m.v, Unit:"None"} ]')
    # floci rejects PutMetricData bodies with more than 20 datums, so send 20 at a time.
    local file chunk; file=$(mktemp)
    while read -r chunk; do
      printf '%s' "$chunk" >"$file"
      aws_in "$region" cloudwatch put-metric-data --namespace ECS/ContainerInsights --metric-data "file://$file"
    done < <(printf '%s' "$batch" | jq -c '_nwise(20)')
    rm -f "$file"
  done
}

seed_region_eu_west_1() {
  local r=eu-west-1
  seed_network "$r" 10.10
  seed_cluster "$r" core-services true payments-api:payments notifications auth-gateway
  seed_cluster "$r" eu-apps true catalog-api checkout-web
  seed_cluster "$r" ci-tools true
  # Scalable target and a CPU target-tracking policy on payments-api.
  aws_in "$r" application-autoscaling register-scalable-target --service-namespace ecs \
    --resource-id service/core-services/payments-api --scalable-dimension ecs:service:DesiredCount --min-capacity 2 --max-capacity 6 >/dev/null
  aws_in "$r" application-autoscaling put-scaling-policy --service-namespace ecs --resource-id service/core-services/payments-api \
    --scalable-dimension ecs:service:DesiredCount --policy-name cpu-target --policy-type TargetTrackingScaling \
    --target-tracking-scaling-policy-configuration '{"TargetValue":60.0,"PredefinedMetricSpecification":{"PredefinedMetricType":"ECSServiceAverageCPUUtilization"},"ScaleInCooldown":120,"ScaleOutCooldown":60}' >/dev/null
  # catalog-api: one task interrupted the way a Spot reclaim looks.
  local catalog_task
  catalog_task=$(running_task_ids "$r" eu-apps catalog-api | head -n 1)
  if [ -n "$catalog_task" ]; then
    aws_in "$r" ecs stop-task --cluster eu-apps --task "$catalog_task" --reason 'Your Spot Task was interrupted.' >/dev/null
  fi
  # A standalone task (no service): a CI build run in ci-tools.
  register_task_definition "$r" release-build plain
  aws_in "$r" logs create-log-group --log-group-name /ecs/release-build 2>/dev/null || true
  aws_in "$r" ecs run-task --cluster ci-tools --task-definition release-build --launch-type FARGATE --started-by release-pipeline \
    --network-configuration "awsvpcConfiguration={subnets=[$SUB1],securityGroups=[$SG]}" >/dev/null
  seed_logs "$r" core-services payments-api payments-api otel-collector
  seed_logs "$r" core-services notifications notifications
  seed_logs "$r" core-services auth-gateway auth-gateway
  seed_logs "$r" eu-apps catalog-api catalog-api
  seed_logs "$r" eu-apps checkout-web checkout-web
  seed_metrics "$r" core-services payments-api notifications auth-gateway
  seed_metrics "$r" eu-apps catalog-api checkout-web
}

seed_region_us_east_1() {
  local r=us-east-1
  seed_network "$r" 10.20
  seed_cluster "$r" background-jobs true image-resizer report-worker:efs
  # Container Insights stays off for edge-proxy on purpose, and no metrics are written for its services.
  seed_cluster "$r" edge-proxy false edge-router
  local task
  while read -r task; do
    [ -n "$task" ] || continue
    aws_in "$r" ecs stop-task --cluster background-jobs --task "$task" --reason 'OutOfMemoryError: Container killed due to memory usage' >/dev/null
  done < <(running_task_ids "$r" background-jobs image-resizer)
  seed_logs "$r" background-jobs image-resizer image-resizer
  seed_logs "$r" background-jobs report-worker report-worker
  seed_logs "$r" edge-proxy edge-router edge-router
  seed_metrics "$r" background-jobs image-resizer report-worker
}

seed_region_eu_central_1() {
  local r=eu-central-1
  seed_network "$r" 10.30
  seed_cluster "$r" staging-web true storefront-staging
  seed_logs "$r" staging-web storefront-staging storefront-staging
  seed_metrics "$r" staging-web storefront-staging
}

region_seeded() { # region first-cluster
  aws_in "$1" ecs describe-clusters --clusters "$2" --query 'clusters[?status==`ACTIVE`].clusterName' --output text | grep -q .
}

main() {
  wait_for_floci
  create_role
  region_seeded eu-west-1 core-services && echo "eu-west-1 already seeded" || seed_region_eu_west_1
  region_seeded us-east-1 background-jobs && echo "us-east-1 already seeded" || seed_region_us_east_1
  region_seeded eu-central-1 staging-web && echo "eu-central-1 already seeded" || seed_region_eu_central_1
  echo "seed summary:"
  local r
  for r in eu-west-1 us-east-1 eu-central-1; do
    echo "  $r: $(aws_in "$r" ecs list-clusters --query 'length(clusterArns)' --output text) clusters"
  done
  echo "  role: arn:aws:iam::$ACCOUNT:role/$ROLE_NAME"
}

main
