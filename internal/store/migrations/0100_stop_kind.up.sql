-- Stop classification persisted per task (internal/status/stopkind.go): stop_kind is 'error' or 'expected'
-- (NULL = normal or not stopped) and stop_cause names why an expected/planned stop happened.
-- Idempotent on purpose so the backfill can be replayed in tests.
alter table tasks add column if not exists stop_kind text;
alter table tasks add column if not exists stop_cause text;

-- Backfill STOPPED rows with the same rules as status.StopKind / status.StopCause:
-- hard errors first (failed to start, OOM, failed health checks, crashed essential container),
-- then a platform/operator cause turns a non-zero exit into 'expected', else a non-zero exit is an error.
with flags as (
  select account_id, region, task_arn, stop_code,
         lower(coalesce(stopped_reason, '')) as reason,
         coalesce(exit_code, 0) <> 0 as nonzero_exit,
         exists (
           select 1 from jsonb_array_elements(case when jsonb_typeof(containers) = 'array' then containers else '[]'::jsonb end) c
           where c->>'ExitCode' = '137'
         ) as exit_137
  from tasks
  where last_status = 'STOPPED'
), classified as (
  select *,
         (stop_code = 'TaskFailedToStart' or reason like '%outofmemory%' or exit_137 or reason like '%health checks%'
          or (stop_code = 'EssentialContainerExited' and nonzero_exit)) as hard_error
  from flags
), caused as (
  select *,
         case
           when hard_error then null
           when stop_code = 'SpotInterruption' then 'spot'
           when stop_code = 'TerminationNotice' then 'maintenance'
           when reason like '%via harbormaster%' or stop_code = 'UserInitiated' then 'user'
           when reason like '%desired count%' then 'scale-in'
           when reason like '%scaling activity initiated by (deployment %' then 'scaling'
           when stop_code = 'ServiceSchedulerInitiated' and (reason like '%deployment%' or reason like '%replaced%') then 'deploy'
         end as cause
  from classified
)
update tasks t
set stop_cause = caused.cause,
    stop_kind = case
      when caused.hard_error then 'error'
      when caused.cause is not null and caused.nonzero_exit then 'expected'
      when caused.cause is null and caused.nonzero_exit then 'error'
    end
from caused
where t.account_id = caused.account_id and t.region = caused.region and t.task_arn = caused.task_arn;

-- Spot interruptions used to fall into the default "Replaced by a deployment" group.
update tasks set stop_reason_group = 'Spot interruption'
where last_status = 'STOPPED' and stop_code in ('SpotInterruption', 'TerminationNotice')
  and stop_reason_group = 'Replaced by a deployment';
