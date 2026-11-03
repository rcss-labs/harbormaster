-- Deploy tab: why a deployment was started and which change set (one Deploy-tab submission) created it.
-- Both stay null for deployments made outside the Deploy tab.
alter table deployment_history add column reason text;
alter table deployment_history add column changeset_id text;
