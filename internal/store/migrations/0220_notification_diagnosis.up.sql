-- Diagnosis messages: a delivery row that reports an investigation of an alert. Additive, like 0216. An older task
-- writes only the five old events, which still pass the widened check.
--
-- A diagnosis row is invisible to an older task by construction. Its due time lives in diagnosis_due_at, never in
-- next_attempt_at, and it is never 'held', so the older claim (status pending and next_attempt_at <= now), the older
-- held-row release (status held) and the older cancel (event list) all skip it; it would otherwise be rendered as an
-- Opened message. After a rollback the leftover rows are inert and are deleted with their incident.
alter table notification_deliveries drop constraint notification_deliveries_event_check;
alter table notification_deliveries add constraint notification_deliveries_event_check
  check (event in ('opened', 'repeat', 'update', 'resolved', 'test', 'diagnosis'));
alter table notification_deliveries add column diagnosis_due_at timestamptz;
alter table notification_deliveries add constraint notification_deliveries_diagnosis_due check (
  (event <> 'diagnosis' or (next_attempt_at is null and status <> 'held'))
  and (event = 'diagnosis' or diagnosis_due_at is null));
create index notification_deliveries_diagnosis_due_idx on notification_deliveries (diagnosis_due_at)
  where event = 'diagnosis' and status = 'pending';
