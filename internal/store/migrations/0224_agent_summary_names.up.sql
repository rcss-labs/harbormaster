-- The ids of the clusters and services whose random-looking names a stored summary uses. The check at send time
-- decodes the names from them; they are never shown.
--
-- Additive. A task from before names its columns, so it neither reads nor writes this one: what it stores has the
-- default, and a summary with such a name is replaced when it sends it, as before. A constant default rewrites no rows.
--
-- The alters take an exclusive lock on tables older tasks keep writing to, and every statement that arrives while they
-- wait queues behind them. The 2 seconds must stay well below the dispatcher's 5 second writeTimeout (see 0222). The
-- runner applies each migration in one transaction, so a timeout rolls the whole file back, records nothing, and the
-- next start retries it; `set local` ends with the transaction.
-- The wait can come once for each table. Review runs come first, the order in which ClaimReviewRun takes the two tables:
-- the other way round the two would deadlock (40P01).
set local lock_timeout = '2s';
alter table agent_review_runs
  add column summary_name_ids text[] not null default '{}',
  add constraint agent_review_runs_summary_name_ids check (cardinality(summary_name_ids) <= 64);
alter table agent_investigations
  add column summary_name_ids text[] not null default '{}',
  add constraint agent_investigations_summary_name_ids check (cardinality(summary_name_ids) <= 64);
