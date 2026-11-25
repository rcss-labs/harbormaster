-- Custom reviews (phase 4): a fifth review type whose text the admin writes, a copy of that text kept on each run
-- row, and test runs. The type list stays closed, which corrects the comment in 0221: a new type is a code and
-- migration change, never data.
--
-- Additive in effect. A task from before this phase writes only the four old types and never names the new columns,
-- so its column lists keep working: instructions default to '' (valid for a built-in type), test to false. An older
-- task that tries to turn a custom review into a built-in one is refused by the pairing check below rather than
-- silently dropping the text.
--
-- The alters take an exclusive lock on tables older tasks keep writing to, and every statement that arrives while
-- they wait queues behind them. The 2 seconds must stay well below the dispatcher's 5 second writeTimeout (see 0222).
-- The runner applies each migration in one transaction, so a timeout rolls the whole file back, records nothing, and
-- the next start retries it; `set local` ends with the transaction.
set local lock_timeout = '2s';

-- The name is the one Postgres gave the inline check of 0221. No `if exists`: a wrong name must fail loudly.
alter table agent_reviews drop constraint agent_reviews_type_check;
alter table agent_reviews add constraint agent_reviews_type_check
  check (type in ('cost', 'security', 'reliability', 'capacity', 'custom'));
alter table agent_reviews add column instructions text not null default '';
alter table agent_reviews add constraint agent_reviews_instructions_length check (char_length(instructions) <= 2000);
alter table agent_reviews add constraint agent_reviews_instructions_type check ((type = 'custom') = (instructions <> ''));

-- The copy is made when a run first starts; the run outlives its review. A test run is manual and is stored, not sent.
alter table agent_review_runs add column instructions text not null default '';
alter table agent_review_runs add constraint agent_review_runs_instructions_length check (char_length(instructions) <= 2000);
alter table agent_review_runs add column test boolean not null default false;
alter table agent_review_runs add constraint agent_review_runs_test_manual check (not test or manual);
-- A test run covers no tags (readers decide by tags and know no test runs). A phase 3 task that claims a test row
-- during a rolling deploy writes tags at start; this check refuses that start before any model call is made.
alter table agent_review_runs add constraint agent_review_runs_test_no_tags check (not test or covered_tags = '{}');
