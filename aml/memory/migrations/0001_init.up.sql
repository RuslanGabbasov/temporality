create table if not exists aml_assets (
    id uuid primary key,
    dedup_key text unique not null,
    service text not null,
    problem text not null,
    kind text not null,
    proposition text not null,
    recommendation jsonb not null,
    evidence jsonb not null default '[]',
    confidence double precision not null,
    status text not null,
    created_at timestamptz not null,
    last_confirmed_at timestamptz,
    last_contradicted_at timestamptz,
    confirmation_count integer not null default 0,
    contradiction_count integer not null default 0,
    consecutive_contradictions integer not null default 0,
    environment text not null,
    version_context text not null default '',
    source_sessions text[] not null default '{}',
    last_confirmed_session integer not null default 0
);

create table if not exists aml_events (
    id bigserial primary key,
    occurred_at timestamptz not null,
    session_id text not null,
    task_id text not null,
    asset_id uuid,
    event_type text not null,
    payload jsonb not null default '{}'
);

create index if not exists aml_events_session_idx on aml_events (session_id, id);
create index if not exists aml_events_type_idx on aml_events (event_type);

create table if not exists aml_tool_calls (
    id bigserial primary key,
    occurred_at timestamptz not null default now(),
    session_id text not null,
    task_id text not null,
    step integer not null,
    seq integer not null,
    tool text not null,
    service text not null default '',
    resource text not null default '',
    auth text not null default '',
    environment text not null default '',
    params jsonb not null default '{}',
    status integer not null,
    ok boolean not null,
    cause text not null default '',
    summary text not null default ''
);

create index if not exists aml_tool_calls_session_idx on aml_tool_calls (session_id, seq);
