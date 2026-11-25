-- Review messages: a delivery row that reports a finished review run, in the same follow-up lane as diagnosis rows.
-- Additive, like 0220. An older task writes only the old events, which still pass the widened check.
--
-- A review row is invisible to an older task by construction. Its due time lives in diagnosis_due_at, never in
-- next_attempt_at, it is never 'held' and it has no incident, so the older claim (status pending and next_attempt_at
-- <= now), the older diagnosis claim (event = 'diagnosis'), the older held-row release (status held) and the older
-- cancel (event list, by incident) all skip it; it would otherwise be rendered as an Opened message. An older task's
-- manual retry of a failed review row writes next_attempt_at and is refused by the check below. After a rollback the
-- leftover rows are inert and age out with the notification log. review_run_id has no foreign key: the log outlives
-- the run rows it reports.
--
-- The alter takes an exclusive lock on a table older tasks keep writing to, and every statement that arrives while
-- it waits queues behind it. The 2 seconds must stay well below the dispatcher's 5 second writeTimeout: otherwise
-- an older sender's result write times out behind the waiting ALTER and a delivered alert is sent again. The runner
-- applies each migration in one transaction, so a timeout rolls the whole file back, records nothing, and the next
-- start retries it from scratch; `set local` ends with the transaction.
set local lock_timeout = '2s';
-- One statement, so the checks are validated in one scan of the log under the lock, not one scan each.
alter table notification_deliveries
  add column review_run_id bigint,
  drop constraint notification_deliveries_event_check,
  add constraint notification_deliveries_event_check
    check (event in ('opened', 'repeat', 'update', 'resolved', 'test', 'diagnosis', 'review')),
  drop constraint notification_deliveries_diagnosis_due,
  add constraint notification_deliveries_diagnosis_due check (
    (event not in ('diagnosis', 'review') or (next_attempt_at is null and status <> 'held'))
    and (event in ('diagnosis', 'review') or diagnosis_due_at is null)),
  add constraint notification_deliveries_review_run
    check ((event = 'review') = (review_run_id is not null)),
  add constraint notification_deliveries_review_no_incident
    check (event <> 'review' or incident_id is null);
create unique index notification_deliveries_review_key_idx on notification_deliveries (route_id, review_run_id)
  where review_run_id is not null and route_id is not null;
create index notification_deliveries_review_due_idx on notification_deliveries (diagnosis_due_at)
  where event = 'review' and status = 'pending';
