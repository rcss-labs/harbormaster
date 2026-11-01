-- The task's own enableExecuteCommand (DescribeTasks). Standalone tasks have no service to ask,
-- so the terminal needs it per task. Rows polled before this migration read false until re-polled.
ALTER TABLE tasks ADD COLUMN enable_execute_command boolean NOT NULL DEFAULT false;
