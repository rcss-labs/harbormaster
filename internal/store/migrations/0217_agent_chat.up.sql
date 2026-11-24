-- AI agent chat: per-user switch, conversations, runs with a firm monthly budget, and the admin settings.
-- Additive only: old tasks ignore these tables and the new column during a deploy.
-- Tool results are never stored: agent_messages has no column able to hold one.
alter table users add column ai_chat boolean not null default false;

-- A single row. The budget is integer micro-dollars; 0 refuses every run until an admin sets one.
create table agent_settings (
  id boolean primary key default true check (id),
  monthly_budget_micro_usd bigint not null default 0 check (monthly_budget_micro_usd >= 0),
  unredacted_providers text[] not null default '{}'
);
insert into agent_settings (id) values (true);

create table agent_role_models (
  role text primary key,
  provider text not null,
  model text not null
);

create table agent_conversations (
  id bigserial primary key,
  user_id bigint not null references users(id) on delete cascade,
  title text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index agent_conversations_user_idx on agent_conversations (user_id, updated_at desc);
create index agent_conversations_updated_idx on agent_conversations (updated_at);

create table agent_messages (
  id bigserial primary key,
  conversation_id bigint not null references agent_conversations(id) on delete cascade,
  role text not null check (role in ('user', 'assistant')),
  text text not null,
  tools text[] not null default '{}',
  interrupted boolean not null default false,
  created_at timestamptz not null default now()
);
create index agent_messages_conversation_idx on agent_messages (conversation_id, id);

-- Runs outlive the user and the conversation so usage history survives their deletion.
create table agent_runs (
  id uuid primary key,
  role text not null,
  user_id bigint references users(id) on delete set null,
  conversation_id bigint references agent_conversations(id) on delete set null,
  provider text not null,
  model text not null,
  state text not null check (state in ('running', 'done', 'failed')),
  reserved_micro_usd bigint not null default 0 check (reserved_micro_usd >= 0),
  cost_micro_usd bigint not null default 0 check (cost_micro_usd >= 0),
  input_tokens bigint not null default 0,
  output_tokens bigint not null default 0,
  tools text[] not null default '{}',
  started_at timestamptz not null default now(),
  finished_at timestamptz,
  last_usage_at timestamptz
);
create index agent_runs_started_idx on agent_runs (started_at);
