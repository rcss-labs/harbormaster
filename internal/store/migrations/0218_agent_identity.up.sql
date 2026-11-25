-- The agent identity: a user of kind 'agent' that can never sign in or administer and holds only View and Logs.
-- Additive only, and no row is inserted (plan call 1): a seeded row would make an older task's user count non-zero on a
-- fresh database and skip first-run setup. New code creates the row lazily, when the agent is on.
-- An older task inserts users without naming kind, so they default to 'human'. An older task that lists the identity on
-- its Users page for the minutes of a deploy cannot give it a privilege: the shape check below refuses its admin switch,
-- password reset, break-glass and SSO link with a constraint error (a 500 on that task). Its permission editor can store
-- more than View and Logs; new code masks every action but View and Logs when it reads them.
alter table users add column kind text not null default 'human' check (kind in ('human', 'agent'));

-- An agent has no credential at all, so the rule that every user has one applies to humans only.
alter table users drop constraint users_has_credential;
alter table users add constraint users_has_credential check (kind = 'agent' or password_hash is not null or external_id is not null);

alter table users add constraint users_agent_shape check (
  kind = 'human' or (
    not is_admin and password_hash is null and not break_glass and not must_change_password and not ai_chat
    and idp_entity_id is null and external_id is null and external_id_format is null and pending_link_name_id is null));

-- At most one agent identity.
create unique index users_single_agent_idx on users ((true)) where kind = 'agent';
