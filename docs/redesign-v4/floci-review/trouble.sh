#!/usr/bin/env bash
# Adds the access role, failures and log lines to the local floci estate.
set -euo pipefail
A="aws --profile floci --region eu-west-1"; U="aws --profile floci --region us-east-1"
$A iam create-role --role-name harbormaster-access --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sts:AssumeRole"}]}' --query Role.Arn --output text
$A sts assume-role --role-arn arn:aws:iam::000000000000:role/harbormaster-access --role-session-name probe --external-id abc --query 'AssumedRoleUser.Arn' --output text
for t in $($U ecs list-tasks --cluster background-jobs --service-name image-resizer --query 'taskArns' --output text); do $U ecs stop-task --cluster background-jobs --task "$t" --reason 'OutOfMemoryError: Container killed due to memory usage' --query task.lastStatus --output text; done
$A logs create-log-stream --log-group-name /ecs/payments-api --log-stream-name ecs/payments-api/9f2e1a3b
NOW=$(($(date +%s)*1000))
$A logs put-log-events --log-group-name /ecs/payments-api --log-stream-name ecs/payments-api/9f2e1a3b --log-events "[{\"timestamp\":$NOW,\"message\":\"POST /v1/charges 201 143ms provider=stripe\"},{\"timestamp\":$NOW,\"message\":\"WARN slow query took 812ms\"},{\"timestamp\":$NOW,\"message\":\"ERROR card_declined code=insufficient_funds\"}]" >/dev/null
