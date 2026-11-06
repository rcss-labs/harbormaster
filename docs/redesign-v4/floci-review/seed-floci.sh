#!/usr/bin/env bash
# Seeds a small demo ECS estate into a local floci emulator so Harbormaster's real AWS code path has data.
# Usage: AWS_CONFIG_FILE=... AWS_SHARED_CREDENTIALS_FILE=... seed-floci.sh
set -euo pipefail
PROFILE=floci
[ "$(aws --profile "$PROFILE" --region eu-west-1 configure get endpoint_url)" = "http://127.0.0.1:4566" ] || { echo "profile does not target local floci; refusing" >&2; exit 2; }

seed_region() { # region cidr-prefix "cluster:svc,svc;cluster:svc" 
  local region=$1 prefix=$2 spec=$3
  local a="aws --profile $PROFILE --region $region"
  local vpc sub1 sub2 sub3 sg
  vpc=$($a ec2 create-vpc --cidr-block "$prefix.0.0/16" --query Vpc.VpcId --output text)
  $a ec2 create-tags --resources "$vpc" --tags "Key=Name,Value=apps-$region"
  sub1=$($a ec2 create-subnet --vpc-id "$vpc" --cidr-block "$prefix.1.0/24" --availability-zone "${region}a" --query Subnet.SubnetId --output text)
  sub2=$($a ec2 create-subnet --vpc-id "$vpc" --cidr-block "$prefix.2.0/24" --availability-zone "${region}b" --query Subnet.SubnetId --output text)
  sub3=$($a ec2 create-subnet --vpc-id "$vpc" --cidr-block "$prefix.3.0/24" --availability-zone "${region}c" --query Subnet.SubnetId --output text)
  $a ec2 create-tags --resources "$sub1" --tags Key=Name,Value=private-a
  $a ec2 create-tags --resources "$sub2" --tags Key=Name,Value=private-b
  $a ec2 create-tags --resources "$sub3" --tags Key=Name,Value=public-c
  sg=$($a ec2 create-security-group --group-name apps --description apps --vpc-id "$vpc" --query GroupId --output text)
  $a ec2 create-internet-gateway --query InternetGateway.InternetGatewayId --output text | xargs -I{} $a ec2 attach-internet-gateway --vpc-id "$vpc" --internet-gateway-id {}
  $a efs create-file-system --creation-token "shared-$region" --tags "Key=Name,Value=shared-$region" --query FileSystemId --output text >/dev/null
  local entry cluster svcs svc
  IFS=';' read -ra entries <<<"$spec"
  for entry in "${entries[@]}"; do
    cluster=${entry%%:*}; svcs=${entry#*:}
    $a ecs create-cluster --cluster-name "$cluster" --settings name=containerInsights,value=enabled --tags key=env,value=demo >/dev/null
    IFS=',' read -ra names <<<"$svcs"
    for svc in "${names[@]}"; do
      $a logs create-log-group --log-group-name "/ecs/$svc" 2>/dev/null || true
      $a ecs register-task-definition --family "$svc" --network-mode awsvpc --requires-compatibilities FARGATE --cpu 512 --memory 1024 \
        --container-definitions "[{\"name\":\"$svc\",\"image\":\"nginx:1.27-alpine\",\"essential\":true,\"portMappings\":[{\"containerPort\":8080}],\"logConfiguration\":{\"logDriver\":\"awslogs\",\"options\":{\"awslogs-group\":\"/ecs/$svc\",\"awslogs-region\":\"$region\",\"awslogs-stream-prefix\":\"ecs\"}}}]" >/dev/null
      $a ecs create-service --cluster "$cluster" --service-name "$svc" --task-definition "$svc" --desired-count 2 --launch-type FARGATE \
        --enable-execute-command --network-configuration "awsvpcConfiguration={subnets=[$sub1,$sub2],securityGroups=[$sg]}" >/dev/null
      echo "seeded $region/$cluster/$svc"
    done
  done
}
seed_region eu-west-1 10.10 "core-services:payments-api,notifications,auth-gateway;eu-apps:catalog-api,checkout-web"
seed_region us-east-1 10.20 "background-jobs:image-resizer,report-worker;edge-proxy:edge-router"
seed_region eu-central-1 10.30 "staging-web:storefront-staging"
