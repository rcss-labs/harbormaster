-- Diagnosis messages: a delivery row that reports an investigation of an alert. Additive, like 0216. An older task
-- writes only the five old events, which still pass the widened check.
--
-- A diagnosis row is invisible to an older task by construction. Its due time lives in diagnosis_due_at, never in
-- next_attempt_at, and it is never 'held', so the older claim (status pending and next_attempt_at <= now), the older
-- held-row release (status held) and the older cancel (event list) all skip it; it would otherwise be rendered as an
-- Opened message. After a rollback the leftover rows are inert and are deleted with their incident.
--
-- The alters take an exclusive lock on a table older tasks keep writing to, and every statement that arrives while
-- they wait queues behind them. The 2 seconds must stay well below the dispatcher's 5 second writeTimeout: otherwise
-- an older sender's result write times out behind the waiting ALTER and a delivered alert is sent again. The runner
-- applies each migration in one transaction, so a timeout rolls the whole file back, records nothing, and the next
-- start retries it from scratch; `set local` ends with the transaction.
set local lock_timeout = '2s';
alter table notification_deliveries drop constraint notification_deliveries_event_check;
alter table notification_deliveries add constraint notification_deliveries_event_check
  check (event in ('opened', 'repeat', 'update', 'resolved', 'test', 'diagnosis'));
alter table notification_deliveries add column diagnosis_due_at timestamptz;
alter table notification_deliveries add constraint notification_deliveries_diagnosis_due check (
  (event <> 'diagnosis' or (next_attempt_at is null and status <> 'held'))
  and (event = 'diagnosis' or diagnosis_due_at is null));
create index notification_deliveries_diagnosis_due_idx on notification_deliveries (diagnosis_due_at)
  where event = 'diagnosis' and status = 'pending';
