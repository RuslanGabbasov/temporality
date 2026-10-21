-- Baseline of the workspace schema as of release v2026.2.19 (the squash of
-- migrations/workspace 000021-000075). Two sections:
--
--   1. The final schema: tables, sequences, indexes, constraints in their
--      current shape, all idempotent (IF NOT EXISTS / guarded constraint
--      adds). Fresh installs build everything from here.
--
--   2. Upgrade repairs for installs predating the squash: the columns the
--      chain added over time (ADD COLUMN IF NOT EXISTS), shape relaxations
--      (DROP NOT NULL), the channel_transport seed and the one-time data
--      fixes. On a fresh install every statement here is a no-op; on an old
--      one it repairs the shape and replays the backfills.
--
-- After this baseline, new migrations continue as 0002_*.up.sql onward, one
-- idempotent file each. Squashing again at a later release: regenerate
-- section 1 from a fresh pg_dump, fold section 2 into it, restart numbering.

-- ==========================================================================
-- 1. Final schema
-- ==========================================================================

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;
CREATE TABLE IF NOT EXISTS public.access_audit_log (
    id bigint NOT NULL,
    actor text NOT NULL,
    action text NOT NULL,
    entity_kind text NOT NULL,
    entity_id text NOT NULL,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.access_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.access_audit_log_id_seq OWNED BY public.access_audit_log.id;
CREATE TABLE IF NOT EXISTS public.auth_claim_mapping (
    id bigint NOT NULL,
    claim text NOT NULL,
    value text NOT NULL,
    install_role text DEFAULT ''::text NOT NULL,
    org_unit_id text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.auth_claim_mapping_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.auth_claim_mapping_id_seq OWNED BY public.auth_claim_mapping.id;
CREATE TABLE IF NOT EXISTS public.auth_idp_config (
    id integer DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    issuer text DEFAULT ''::text NOT NULL,
    client_id text DEFAULT ''::text NOT NULL,
    client_secret_ref text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT 'SSO'::text NOT NULL,
    scopes text DEFAULT 'openid profile email'::text NOT NULL,
    username_claim text DEFAULT 'preferred_username'::text NOT NULL,
    email_claim text DEFAULT 'email'::text NOT NULL,
    groups_claim text DEFAULT 'groups'::text NOT NULL,
    default_role text DEFAULT 'reader'::text NOT NULL,
    jit_enabled boolean DEFAULT true NOT NULL,
    redirect_base text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT auth_idp_config_id_check CHECK ((id = 1))
);
CREATE TABLE IF NOT EXISTS public.auth_request (
    state text NOT NULL,
    nonce text NOT NULL,
    verifier text NOT NULL,
    redirect_uri text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone NOT NULL
);
CREATE TABLE IF NOT EXISTS public.auth_session (
    id text NOT NULL,
    user_id text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    last_seen timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    ip text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL
);
CREATE TABLE IF NOT EXISTS public.channel_transport (
    id integer DEFAULT 1 NOT NULL,
    ui_url text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT channel_transport_id_check CHECK ((id = 1))
);
CREATE TABLE IF NOT EXISTS public.execution_identity (
    id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    org_unit_id text,
    allowed_agents jsonb DEFAULT '["*"]'::jsonb NOT NULL,
    allowed_mcp jsonb DEFAULT '["*"]'::jsonb NOT NULL,
    allowed_providers jsonb DEFAULT '["*"]'::jsonb NOT NULL,
    allowed_projects jsonb DEFAULT '["*"]'::jsonb NOT NULL,
    human_targets jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.human_request (
    id text NOT NULL,
    run_id text NOT NULL,
    project_id text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    recipient text DEFAULT ''::text NOT NULL,
    resolved_user text DEFAULT ''::text NOT NULL,
    question text NOT NULL,
    context text DEFAULT ''::text NOT NULL,
    options jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    channel text DEFAULT ''::text NOT NULL,
    response text DEFAULT ''::text NOT NULL,
    answered_by text DEFAULT ''::text NOT NULL,
    timeout_seconds integer DEFAULT 0 NOT NULL,
    timeout_policy text DEFAULT 'fallback'::text NOT NULL,
    execution_identity_id text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    delivered_at timestamp with time zone,
    answered_at timestamp with time zone,
    expires_at timestamp with time zone,
    agent_version integer DEFAULT 0 NOT NULL,
    thread_ref text DEFAULT ''::text NOT NULL
);
CREATE TABLE IF NOT EXISTS public.human_request_delivery (
    request_id text NOT NULL,
    user_id text NOT NULL,
    channel text NOT NULL,
    status text DEFAULT 'delivered'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.org_policy (
    id text NOT NULL,
    org_unit_id text,
    name text NOT NULL,
    allowed_models jsonb DEFAULT '[]'::jsonb NOT NULL,
    allowed_mcp jsonb DEFAULT '[]'::jsonb NOT NULL,
    max_tokens integer,
    max_budget_usd numeric(12,4),
    timeout_seconds integer,
    network_mode text,
    sandbox_mode text,
    approval_mode text,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    intake_mode text DEFAULT ''::text NOT NULL,
    CONSTRAINT org_policy_approval_mode_check CHECK (((approval_mode IS NULL) OR (approval_mode = ANY (ARRAY[''::text, 'auto'::text, 'tools'::text])))),
    CONSTRAINT org_policy_intake_mode_check CHECK ((intake_mode = ANY (ARRAY[''::text, 'deny'::text]))),
    CONSTRAINT org_policy_max_budget_usd_check CHECK (((max_budget_usd IS NULL) OR (max_budget_usd > (0)::numeric))),
    CONSTRAINT org_policy_max_tokens_check CHECK (((max_tokens IS NULL) OR (max_tokens > 0))),
    CONSTRAINT org_policy_network_mode_check CHECK (((network_mode IS NULL) OR (network_mode = ANY (ARRAY[''::text, 'allow'::text, 'deny'::text])))),
    CONSTRAINT org_policy_sandbox_mode_check CHECK (((sandbox_mode IS NULL) OR (sandbox_mode = ANY (ARRAY[''::text, 'standard'::text, 'read_only'::text])))),
    CONSTRAINT org_policy_timeout_seconds_check CHECK (((timeout_seconds IS NULL) OR (timeout_seconds > 0)))
);
CREATE TABLE IF NOT EXISTS public.org_unit (
    id text NOT NULL,
    parent_id text,
    kind text DEFAULT 'department'::text NOT NULL,
    name text NOT NULL,
    path text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    head_user_id text,
    CONSTRAINT org_unit_kind_check CHECK ((kind = ANY (ARRAY['organization'::text, 'department'::text, 'team'::text])))
);
CREATE TABLE IF NOT EXISTS public.org_unit_role (
    user_id text NOT NULL,
    org_unit_id text NOT NULL,
    role text NOT NULL,
    granted_by text DEFAULT ''::text NOT NULL,
    granted_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_agent (
    id text NOT NULL,
    project_id text,
    name text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    system_prompt text DEFAULT ''::text NOT NULL,
    skills jsonb DEFAULT '[]'::jsonb NOT NULL,
    mcp_servers jsonb DEFAULT '[]'::jsonb NOT NULL,
    sandbox_profile text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    provider text DEFAULT ''::text NOT NULL,
    temperature double precision,
    max_tokens integer,
    network_access boolean,
    read_only boolean,
    max_turns integer,
    approval_mode text,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    definition jsonb DEFAULT '{}'::jsonb NOT NULL,
    definition_version integer DEFAULT 0 NOT NULL,
    org_unit_id text,
    secrets jsonb DEFAULT '[]'::jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_agent_evaluation_run (
    id bigint NOT NULL,
    agent_id text NOT NULL,
    agent_version integer NOT NULL,
    passed integer DEFAULT 0 NOT NULL,
    failed integer DEFAULT 0 NOT NULL,
    cases jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    proposal_id bigint,
    verdict text DEFAULT 'unknown'::text NOT NULL,
    cause text DEFAULT ''::text NOT NULL,
    threshold jsonb DEFAULT '{}'::jsonb NOT NULL,
    judge_model text DEFAULT ''::text NOT NULL,
    judge_same_model boolean DEFAULT false NOT NULL,
    judge_degraded boolean DEFAULT false NOT NULL,
    trigger text DEFAULT 'manual'::text NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_agent_evaluation_run_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_agent_evaluation_run_id_seq OWNED BY public.workspace_agent_evaluation_run.id;
CREATE TABLE IF NOT EXISTS public.workspace_agent_evaluation_suite (
    agent_id text NOT NULL,
    cases jsonb DEFAULT '[]'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_agent_proposal (
    id bigint NOT NULL,
    agent_id text NOT NULL,
    base_version integer NOT NULL,
    definition jsonb DEFAULT '{}'::jsonb NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    problem text NOT NULL,
    change text NOT NULL,
    effect text DEFAULT ''::text NOT NULL,
    evidence jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    author text DEFAULT ''::text NOT NULL,
    generator_model text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    decided_by text DEFAULT ''::text NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_agent_proposal_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_agent_proposal_id_seq OWNED BY public.workspace_agent_proposal.id;
CREATE TABLE IF NOT EXISTS public.workspace_agent_version (
    agent_id text NOT NULL,
    version integer NOT NULL,
    definition jsonb DEFAULT '{}'::jsonb NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    compiled_prompt text DEFAULT ''::text NOT NULL,
    prompt_source text DEFAULT 'manual'::text NOT NULL,
    generator_model text DEFAULT ''::text NOT NULL,
    author text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_artifact (
    id text NOT NULL,
    title text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    provider text DEFAULT 'builtin'::text NOT NULL,
    external_ref text DEFAULT ''::text NOT NULL,
    content text DEFAULT ''::text NOT NULL,
    content_hash text DEFAULT ''::text NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    mime_type text DEFAULT 'text/markdown'::text NOT NULL,
    org_unit_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    status text DEFAULT 'approved'::text NOT NULL,
    kind text DEFAULT 'reference'::text NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    origin_type text DEFAULT 'human'::text NOT NULL,
    origin_user text,
    origin_run text,
    saga_id text
);
CREATE TABLE IF NOT EXISTS public.workspace_artifact_review (
    id text NOT NULL,
    artifact_id text NOT NULL,
    artifact_version integer NOT NULL,
    reviewers text[] NOT NULL,
    mode text DEFAULT 'all'::text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    requested_by text DEFAULT ''::text NOT NULL,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    decisions jsonb DEFAULT '{}'::jsonb NOT NULL,
    resolved_by text,
    resolved_at timestamp with time zone
);
CREATE TABLE IF NOT EXISTS public.workspace_channel_cursor (
    channel text NOT NULL,
    cursor text DEFAULT ''::text NOT NULL,
    identity text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_chat (
    id text NOT NULL,
    project_id text DEFAULT ''::text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    transcript jsonb DEFAULT '[]'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_eval_case_candidate (
    id bigint NOT NULL,
    agent_id text NOT NULL,
    run_id text NOT NULL,
    task_id text DEFAULT ''::text NOT NULL,
    project_id text DEFAULT ''::text NOT NULL,
    agent_version integer DEFAULT 0 NOT NULL,
    input text DEFAULT ''::text NOT NULL,
    expected_output text DEFAULT ''::text NOT NULL,
    source text DEFAULT 'succeeded_run'::text NOT NULL,
    verdict text DEFAULT 'pending'::text NOT NULL,
    redaction_applied boolean DEFAULT false NOT NULL,
    redactions integer DEFAULT 0 NOT NULL,
    reviewed_by text DEFAULT ''::text NOT NULL,
    reviewed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT eval_case_candidate_source CHECK ((source = ANY (ARRAY['succeeded_run'::text, 'human_corrected'::text]))),
    CONSTRAINT eval_case_candidate_verdict CHECK ((verdict = ANY (ARRAY['pending'::text, 'accepted'::text, 'rejected'::text])))
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_eval_case_candidate_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_eval_case_candidate_id_seq OWNED BY public.workspace_eval_case_candidate.id;
CREATE TABLE IF NOT EXISTS public.workspace_inbound_message (
    id text NOT NULL,
    channel text NOT NULL,
    message_id text NOT NULL,
    sender_raw text DEFAULT ''::text NOT NULL,
    sender_user_id text,
    room text DEFAULT ''::text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    attachments jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'received'::text NOT NULL,
    classification text DEFAULT ''::text NOT NULL,
    intake_run_id text,
    target_run_id text,
    project_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    retired_at timestamp with time zone
);
CREATE TABLE IF NOT EXISTS public.workspace_judge_calibration (
    id bigint NOT NULL,
    agent_id text NOT NULL,
    judge_model text NOT NULL,
    agreement_rate double precision NOT NULL,
    mean_abs_deviation double precision NOT NULL,
    histogram jsonb DEFAULT '[]'::jsonb NOT NULL,
    degraded boolean DEFAULT false NOT NULL,
    cases_checked integer DEFAULT 0 NOT NULL,
    cases_snapshot jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    checked_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_judge_calibration_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_judge_calibration_id_seq OWNED BY public.workspace_judge_calibration.id;
CREATE TABLE IF NOT EXISTS public.workspace_mcp_server (
    id text NOT NULL,
    project_id text,
    name text NOT NULL,
    type text NOT NULL,
    url text DEFAULT ''::text NOT NULL,
    command text DEFAULT ''::text NOT NULL,
    args jsonb DEFAULT '[]'::jsonb NOT NULL,
    env jsonb DEFAULT '[]'::jsonb NOT NULL,
    headers jsonb DEFAULT '{}'::jsonb NOT NULL,
    allowed_tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    approval_tools jsonb DEFAULT '[]'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    org_unit_id text,
    CONSTRAINT workspace_mcp_server_type_check CHECK ((type = ANY (ARRAY['stdio'::text, 'sse'::text, 'http'::text])))
);
CREATE TABLE IF NOT EXISTS public.workspace_project (
    id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    default_agent_id text,
    default_model text DEFAULT ''::text NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    intake_enabled boolean DEFAULT false NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_project_member (
    project_id text NOT NULL,
    user_id text NOT NULL,
    role text DEFAULT 'writer'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by text DEFAULT ''::text NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_project_org_unit (
    project_id text NOT NULL,
    org_unit_id text NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_provider (
    id text NOT NULL,
    name text NOT NULL,
    base_url text NOT NULL,
    api_key_ref text DEFAULT ''::text NOT NULL,
    models text[] DEFAULT '{}'::text[] NOT NULL,
    labels jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    org_unit_id text
);
CREATE TABLE IF NOT EXISTS public.workspace_research_session (
    id bigint NOT NULL,
    project text NOT NULL,
    title text NOT NULL,
    created_by text DEFAULT ''::text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_research_session_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_research_session_id_seq OWNED BY public.workspace_research_session.id;
CREATE TABLE IF NOT EXISTS public.workspace_run (
    id text NOT NULL,
    task_id text,
    project_id text NOT NULL,
    agent_id text,
    run_id text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    answer text DEFAULT ''::text NOT NULL,
    turns integer DEFAULT 0 NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    agent_version integer DEFAULT 0 NOT NULL,
    exec_context jsonb DEFAULT '{}'::jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_sandbox_profile (
    id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    image text DEFAULT ''::text NOT NULL,
    toolchains text[] DEFAULT '{}'::text[] NOT NULL,
    memory text DEFAULT ''::text NOT NULL,
    cpus text DEFAULT ''::text NOT NULL,
    scratch_size text DEFAULT ''::text NOT NULL,
    tmp_size text DEFAULT ''::text NOT NULL,
    cache_env text[] DEFAULT '{}'::text[] NOT NULL,
    builtin boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    base text DEFAULT ''::text NOT NULL,
    packages text[] DEFAULT '{}'::text[] NOT NULL,
    docker_socket boolean DEFAULT false NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_secret (
    id text NOT NULL,
    name text NOT NULL,
    provider text DEFAULT 'builtin'::text NOT NULL,
    external_ref text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    org_unit_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT workspace_secret_provider_check CHECK ((provider = ANY (ARRAY['builtin'::text, 'env'::text])))
);
CREATE TABLE IF NOT EXISTS public.workspace_secret_value (
    secret_id text NOT NULL,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_setting (
    key text NOT NULL,
    value text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_skill (
    id text NOT NULL,
    project_id text,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    version text DEFAULT '1.0.0'::text NOT NULL,
    markdown text DEFAULT ''::text NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    org_unit_id text
);
CREATE TABLE IF NOT EXISTS public.workspace_skill_evaluation_run (
    id bigint NOT NULL,
    skill_id text NOT NULL,
    skill_version text NOT NULL,
    passed integer DEFAULT 0 NOT NULL,
    failed integer DEFAULT 0 NOT NULL,
    cases jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_skill_evaluation_run_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_skill_evaluation_run_id_seq OWNED BY public.workspace_skill_evaluation_run.id;
CREATE TABLE IF NOT EXISTS public.workspace_skill_evaluation_suite (
    skill_id text NOT NULL,
    cases jsonb DEFAULT '[]'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_skill_execution (
    id bigint NOT NULL,
    skill_id text NOT NULL,
    skill_version text NOT NULL,
    project_id text NOT NULL,
    run_id text NOT NULL,
    agent_id text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_skill_execution_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_skill_execution_id_seq OWNED BY public.workspace_skill_execution.id;
CREATE TABLE IF NOT EXISTS public.workspace_skill_version (
    skill_id text NOT NULL,
    version text NOT NULL,
    markdown text DEFAULT ''::text NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    origin text DEFAULT 'initial'::text NOT NULL,
    source_runs text[] DEFAULT '{}'::text[] NOT NULL,
    evidence_refs text[] DEFAULT '{}'::text[] NOT NULL,
    knowledge_ids text[] DEFAULT '{}'::text[] NOT NULL,
    change_summary text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    observed_problem text DEFAULT ''::text NOT NULL,
    proposed_change text DEFAULT ''::text NOT NULL,
    expected_effect text DEFAULT ''::text NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_task (
    id text NOT NULL,
    project_id text NOT NULL,
    agent_id text,
    title text NOT NULL,
    prompt text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_team (
    id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    version text DEFAULT '1.0.0'::text NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    org_unit_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_team_version (
    team_id text NOT NULL,
    version text NOT NULL,
    manifest jsonb DEFAULT '{}'::jsonb NOT NULL,
    origin text DEFAULT 'initial'::text NOT NULL,
    change_summary text DEFAULT ''::text NOT NULL,
    observed_problem text DEFAULT ''::text NOT NULL,
    proposed_change text DEFAULT ''::text NOT NULL,
    expected_effect text DEFAULT ''::text NOT NULL,
    source_runs text[] DEFAULT '{}'::text[] NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE IF NOT EXISTS public.workspace_trigger (
    id text NOT NULL,
    project_id text NOT NULL,
    agent_id text,
    name text NOT NULL,
    type text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    org_unit_id text,
    execution_identity_id text,
    CONSTRAINT workspace_trigger_type_check CHECK ((type = ANY (ARRAY['schedule'::text, 'webhook'::text, 'event'::text])))
);
CREATE TABLE IF NOT EXISTS public.workspace_user (
    id text NOT NULL,
    name text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    role text DEFAULT 'viewer'::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    token text DEFAULT ''::text NOT NULL,
    channels jsonb DEFAULT '[]'::jsonb NOT NULL,
    preferred_channel text DEFAULT ''::text NOT NULL,
    org_unit_id text,
    password_hash text DEFAULT ''::text NOT NULL,
    external_issuer text DEFAULT ''::text NOT NULL,
    external_id text DEFAULT ''::text NOT NULL,
    sourced text DEFAULT 'local'::text NOT NULL,
    last_login_at timestamp with time zone
);
CREATE TABLE IF NOT EXISTS public.workspace_user_fact (
    id bigint NOT NULL,
    user_id text NOT NULL,
    fact text NOT NULL,
    origin text DEFAULT 'manual'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS public.workspace_user_fact_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
ALTER SEQUENCE public.workspace_user_fact_id_seq OWNED BY public.workspace_user_fact.id;
ALTER TABLE ONLY public.access_audit_log ALTER COLUMN id SET DEFAULT nextval('public.access_audit_log_id_seq'::regclass);
ALTER TABLE ONLY public.auth_claim_mapping ALTER COLUMN id SET DEFAULT nextval('public.auth_claim_mapping_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_agent_evaluation_run ALTER COLUMN id SET DEFAULT nextval('public.workspace_agent_evaluation_run_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_agent_proposal ALTER COLUMN id SET DEFAULT nextval('public.workspace_agent_proposal_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_eval_case_candidate ALTER COLUMN id SET DEFAULT nextval('public.workspace_eval_case_candidate_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_judge_calibration ALTER COLUMN id SET DEFAULT nextval('public.workspace_judge_calibration_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_research_session ALTER COLUMN id SET DEFAULT nextval('public.workspace_research_session_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_skill_evaluation_run ALTER COLUMN id SET DEFAULT nextval('public.workspace_skill_evaluation_run_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_skill_execution ALTER COLUMN id SET DEFAULT nextval('public.workspace_skill_execution_id_seq'::regclass);
ALTER TABLE ONLY public.workspace_user_fact ALTER COLUMN id SET DEFAULT nextval('public.workspace_user_fact_id_seq'::regclass);
ALTER TABLE ONLY public.access_audit_log
    ADD CONSTRAINT access_audit_log_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.auth_claim_mapping
    ADD CONSTRAINT auth_claim_mapping_claim_value_key UNIQUE (claim, value);
ALTER TABLE ONLY public.auth_claim_mapping
    ADD CONSTRAINT auth_claim_mapping_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.auth_idp_config
    ADD CONSTRAINT auth_idp_config_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.auth_request
    ADD CONSTRAINT auth_request_pkey PRIMARY KEY (state);
ALTER TABLE ONLY public.auth_session
    ADD CONSTRAINT auth_session_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.channel_transport
    ADD CONSTRAINT channel_transport_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.execution_identity
    ADD CONSTRAINT execution_identity_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.human_request_delivery
    ADD CONSTRAINT human_request_delivery_pkey PRIMARY KEY (request_id, user_id);
ALTER TABLE ONLY public.human_request
    ADD CONSTRAINT human_request_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.org_policy
    ADD CONSTRAINT org_policy_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.org_unit
    ADD CONSTRAINT org_unit_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.org_unit_role
    ADD CONSTRAINT org_unit_role_pkey PRIMARY KEY (user_id, org_unit_id);
ALTER TABLE ONLY public.workspace_agent_evaluation_run
    ADD CONSTRAINT workspace_agent_evaluation_run_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_agent_evaluation_suite
    ADD CONSTRAINT workspace_agent_evaluation_suite_pkey PRIMARY KEY (agent_id);
ALTER TABLE ONLY public.workspace_agent
    ADD CONSTRAINT workspace_agent_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_agent_proposal
    ADD CONSTRAINT workspace_agent_proposal_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_agent_version
    ADD CONSTRAINT workspace_agent_version_pkey PRIMARY KEY (agent_id, version);
ALTER TABLE ONLY public.workspace_artifact
    ADD CONSTRAINT workspace_artifact_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_artifact_review
    ADD CONSTRAINT workspace_artifact_review_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_channel_cursor
    ADD CONSTRAINT workspace_channel_cursor_pkey PRIMARY KEY (channel);
ALTER TABLE ONLY public.workspace_chat
    ADD CONSTRAINT workspace_chat_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_eval_case_candidate
    ADD CONSTRAINT workspace_eval_case_candidate_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_inbound_message
    ADD CONSTRAINT workspace_inbound_message_channel_message_id_key UNIQUE (channel, message_id);
ALTER TABLE ONLY public.workspace_inbound_message
    ADD CONSTRAINT workspace_inbound_message_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_judge_calibration
    ADD CONSTRAINT workspace_judge_calibration_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_mcp_server
    ADD CONSTRAINT workspace_mcp_server_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_project_member
    ADD CONSTRAINT workspace_project_member_pkey PRIMARY KEY (project_id, user_id);
ALTER TABLE ONLY public.workspace_project_org_unit
    ADD CONSTRAINT workspace_project_org_unit_pkey PRIMARY KEY (project_id, org_unit_id);
ALTER TABLE ONLY public.workspace_project
    ADD CONSTRAINT workspace_project_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_provider
    ADD CONSTRAINT workspace_provider_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_research_session
    ADD CONSTRAINT workspace_research_session_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_run
    ADD CONSTRAINT workspace_run_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_sandbox_profile
    ADD CONSTRAINT workspace_sandbox_profile_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_secret
    ADD CONSTRAINT workspace_secret_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_secret_value
    ADD CONSTRAINT workspace_secret_value_pkey PRIMARY KEY (secret_id);
ALTER TABLE ONLY public.workspace_setting
    ADD CONSTRAINT workspace_setting_pkey PRIMARY KEY (key);
ALTER TABLE ONLY public.workspace_skill_evaluation_run
    ADD CONSTRAINT workspace_skill_evaluation_run_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_skill_evaluation_suite
    ADD CONSTRAINT workspace_skill_evaluation_suite_pkey PRIMARY KEY (skill_id);
ALTER TABLE ONLY public.workspace_skill_execution
    ADD CONSTRAINT workspace_skill_execution_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_skill
    ADD CONSTRAINT workspace_skill_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_skill_version
    ADD CONSTRAINT workspace_skill_version_pkey PRIMARY KEY (skill_id, version);
ALTER TABLE ONLY public.workspace_task
    ADD CONSTRAINT workspace_task_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_team
    ADD CONSTRAINT workspace_team_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_team_version
    ADD CONSTRAINT workspace_team_version_pkey PRIMARY KEY (team_id, version);
ALTER TABLE ONLY public.workspace_trigger
    ADD CONSTRAINT workspace_trigger_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_user_fact
    ADD CONSTRAINT workspace_user_fact_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.workspace_user
    ADD CONSTRAINT workspace_user_pkey PRIMARY KEY (id);
CREATE INDEX IF NOT EXISTS access_audit_log_created_idx ON public.access_audit_log USING btree (created_at);
CREATE INDEX IF NOT EXISTS access_audit_log_entity_idx ON public.access_audit_log USING btree (entity_kind, entity_id);
CREATE INDEX IF NOT EXISTS execution_identity_org_idx ON public.execution_identity USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS idx_auth_request_created ON public.auth_request USING btree (created_at);
CREATE INDEX IF NOT EXISTS idx_auth_session_expires ON public.auth_session USING btree (expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_session_user ON public.auth_session USING btree (user_id);
CREATE INDEX IF NOT EXISTS idx_eval_case_candidate_review ON public.workspace_eval_case_candidate USING btree (agent_id, verdict, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_eval_case_candidate_run ON public.workspace_eval_case_candidate USING btree (run_id);
CREATE INDEX IF NOT EXISTS idx_hrd_user_open ON public.human_request_delivery USING btree (user_id, request_id);
CREATE INDEX IF NOT EXISTS idx_human_request_project ON public.human_request USING btree (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_human_request_resolved_user ON public.human_request USING btree (resolved_user, status);
CREATE INDEX IF NOT EXISTS idx_human_request_run ON public.human_request USING btree (run_id);
CREATE INDEX IF NOT EXISTS idx_human_request_status ON public.human_request USING btree (status);
CREATE INDEX IF NOT EXISTS idx_inbound_message_created_at ON public.workspace_inbound_message USING btree (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_inbound_message_unretired ON public.workspace_inbound_message USING btree (created_at) WHERE (retired_at IS NULL);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_fact_unique ON public.workspace_user_fact USING btree (user_id, fact);
CREATE INDEX IF NOT EXISTS idx_user_fact_user ON public.workspace_user_fact USING btree (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_evaluation_run ON public.workspace_agent_evaluation_run USING btree (agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_evaluation_run_gate ON public.workspace_agent_evaluation_run USING btree (agent_id, agent_version, verdict, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_project ON public.workspace_agent USING btree (project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_proposal ON public.workspace_agent_proposal USING btree (agent_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_agent_version_agent ON public.workspace_agent_version USING btree (agent_id);
CREATE INDEX IF NOT EXISTS idx_workspace_artifact_org ON public.workspace_artifact USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS idx_workspace_chat_project ON public.workspace_chat USING btree (project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_judge_calibration_agent ON public.workspace_judge_calibration USING btree (agent_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_judge_calibration_model ON public.workspace_judge_calibration USING btree (judge_model, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_mcp_server_project ON public.workspace_mcp_server USING btree (project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_research_session_project ON public.workspace_research_session USING btree (project, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_run_agent ON public.workspace_run USING btree (agent_id);
CREATE INDEX IF NOT EXISTS idx_workspace_run_project ON public.workspace_run USING btree (project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_run_task ON public.workspace_run USING btree (task_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_secret_name ON public.workspace_secret USING btree (name);
CREATE INDEX IF NOT EXISTS idx_workspace_secret_org ON public.workspace_secret USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_evaluation_run ON public.workspace_skill_evaluation_run USING btree (skill_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_execution_run ON public.workspace_skill_execution USING btree (run_id);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_execution_skill ON public.workspace_skill_execution USING btree (skill_id);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_project ON public.workspace_skill USING btree (project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_version_status ON public.workspace_skill_version USING btree (skill_id, status);
CREATE INDEX IF NOT EXISTS idx_workspace_task_project ON public.workspace_task USING btree (project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_team_org ON public.workspace_team USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS idx_workspace_trigger_project ON public.workspace_trigger USING btree (project_id);
CREATE INDEX IF NOT EXISTS idx_workspace_trigger_type ON public.workspace_trigger USING btree (type);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_user_external ON public.workspace_user USING btree (external_issuer, external_id) WHERE (external_id <> ''::text);
CREATE INDEX IF NOT EXISTS idx_workspace_user_token ON public.workspace_user USING btree (token) WHERE (token <> ''::text);
CREATE INDEX IF NOT EXISTS org_policy_unit_idx ON public.org_policy USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS org_unit_parent_idx ON public.org_unit USING btree (parent_id);
CREATE INDEX IF NOT EXISTS org_unit_role_unit_idx ON public.org_unit_role USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_agent_org_idx ON public.workspace_agent USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_artifact_review_artifact ON public.workspace_artifact_review USING btree (artifact_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS workspace_artifact_status ON public.workspace_artifact USING btree (status);
CREATE INDEX IF NOT EXISTS workspace_mcp_server_org_idx ON public.workspace_mcp_server USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_project_member_user_idx ON public.workspace_project_member USING btree (user_id);
CREATE INDEX IF NOT EXISTS workspace_provider_org_idx ON public.workspace_provider USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_skill_org_idx ON public.workspace_skill USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_trigger_org_unit_idx ON public.workspace_trigger USING btree (org_unit_id);
CREATE INDEX IF NOT EXISTS workspace_user_org_idx ON public.workspace_user USING btree (org_unit_id);
ALTER TABLE ONLY public.auth_session
    ADD CONSTRAINT auth_session_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.workspace_user(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.execution_identity
    ADD CONSTRAINT execution_identity_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.human_request_delivery
    ADD CONSTRAINT human_request_delivery_request_id_fkey FOREIGN KEY (request_id) REFERENCES public.human_request(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.org_policy
    ADD CONSTRAINT org_policy_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.org_unit
    ADD CONSTRAINT org_unit_head_user_id_fkey FOREIGN KEY (head_user_id) REFERENCES public.workspace_user(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.org_unit
    ADD CONSTRAINT org_unit_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.org_unit_role
    ADD CONSTRAINT org_unit_role_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.org_unit_role
    ADD CONSTRAINT org_unit_role_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.workspace_user(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_agent_evaluation_run
    ADD CONSTRAINT workspace_agent_evaluation_run_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_agent_evaluation_run
    ADD CONSTRAINT workspace_agent_evaluation_run_proposal_id_fkey FOREIGN KEY (proposal_id) REFERENCES public.workspace_agent_proposal(id);
ALTER TABLE ONLY public.workspace_agent_evaluation_suite
    ADD CONSTRAINT workspace_agent_evaluation_suite_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_agent
    ADD CONSTRAINT workspace_agent_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_agent
    ADD CONSTRAINT workspace_agent_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_agent_proposal
    ADD CONSTRAINT workspace_agent_proposal_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_agent_version
    ADD CONSTRAINT workspace_agent_version_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_artifact
    ADD CONSTRAINT workspace_artifact_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_artifact_review
    ADD CONSTRAINT workspace_artifact_review_artifact_id_fkey FOREIGN KEY (artifact_id) REFERENCES public.workspace_artifact(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_eval_case_candidate
    ADD CONSTRAINT workspace_eval_case_candidate_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_judge_calibration
    ADD CONSTRAINT workspace_judge_calibration_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_mcp_server
    ADD CONSTRAINT workspace_mcp_server_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_mcp_server
    ADD CONSTRAINT workspace_mcp_server_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_project
    ADD CONSTRAINT workspace_project_default_agent_id_fkey FOREIGN KEY (default_agent_id) REFERENCES public.workspace_agent(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.workspace_project_member
    ADD CONSTRAINT workspace_project_member_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_project_member
    ADD CONSTRAINT workspace_project_member_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.workspace_user(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_project_org_unit
    ADD CONSTRAINT workspace_project_org_unit_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_project_org_unit
    ADD CONSTRAINT workspace_project_org_unit_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_provider
    ADD CONSTRAINT workspace_provider_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_run
    ADD CONSTRAINT workspace_run_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.workspace_run
    ADD CONSTRAINT workspace_run_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_run
    ADD CONSTRAINT workspace_run_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.workspace_task(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_secret
    ADD CONSTRAINT workspace_secret_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_secret_value
    ADD CONSTRAINT workspace_secret_value_secret_id_fkey FOREIGN KEY (secret_id) REFERENCES public.workspace_secret(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_skill_evaluation_run
    ADD CONSTRAINT workspace_skill_evaluation_run_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES public.workspace_skill(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_skill_evaluation_suite
    ADD CONSTRAINT workspace_skill_evaluation_suite_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES public.workspace_skill(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_skill_execution
    ADD CONSTRAINT workspace_skill_execution_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES public.workspace_skill(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_skill
    ADD CONSTRAINT workspace_skill_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_skill
    ADD CONSTRAINT workspace_skill_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_skill_version
    ADD CONSTRAINT workspace_skill_version_skill_id_fkey FOREIGN KEY (skill_id) REFERENCES public.workspace_skill(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_task
    ADD CONSTRAINT workspace_task_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.workspace_task
    ADD CONSTRAINT workspace_task_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_team
    ADD CONSTRAINT workspace_team_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_team_version
    ADD CONSTRAINT workspace_team_version_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.workspace_team(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_trigger
    ADD CONSTRAINT workspace_trigger_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE SET NULL;
ALTER TABLE ONLY public.workspace_trigger
    ADD CONSTRAINT workspace_trigger_execution_identity_id_fkey FOREIGN KEY (execution_identity_id) REFERENCES public.execution_identity(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_trigger
    ADD CONSTRAINT workspace_trigger_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;
ALTER TABLE ONLY public.workspace_trigger
    ADD CONSTRAINT workspace_trigger_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.workspace_project(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_user_fact
    ADD CONSTRAINT workspace_user_fact_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.workspace_user(id) ON DELETE CASCADE;
ALTER TABLE ONLY public.workspace_user
    ADD CONSTRAINT workspace_user_org_unit_id_fkey FOREIGN KEY (org_unit_id) REFERENCES public.org_unit(id) ON DELETE RESTRICT;

-- ==========================================================================
-- 2. Upgrade repairs (pre-baseline installs)
-- ==========================================================================

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS temperature DOUBLE PRECISION;

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS max_tokens INT;

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS network_access BOOLEAN;

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS read_only BOOLEAN;

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS max_turns INT;

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS approval_mode TEXT;

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}';

ALTER TABLE workspace_agent ALTER COLUMN project_id DROP NOT NULL;

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS token TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_project ADD COLUMN IF NOT EXISTS default_agent_id TEXT REFERENCES workspace_agent(id) ON DELETE SET NULL;

ALTER TABLE workspace_project ADD COLUMN IF NOT EXISTS default_model TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_project ADD COLUMN IF NOT EXISTS archived BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE workspace_project ADD COLUMN IF NOT EXISTS allowed_users JSONB NOT NULL DEFAULT '"*"';

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS tools JSONB NOT NULL DEFAULT '[]';

ALTER TABLE workspace_agent
    ADD COLUMN IF NOT EXISTS definition JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS definition_version INT NOT NULL DEFAULT 0;

ALTER TABLE workspace_run ADD COLUMN IF NOT EXISTS agent_version INT NOT NULL DEFAULT 0;

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS channels JSONB NOT NULL DEFAULT '[]';

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS preferred_channel TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_skill ADD COLUMN IF NOT EXISTS project_id TEXT;

ALTER TABLE workspace_mcp_server ADD COLUMN IF NOT EXISTS project_id TEXT;

ALTER TABLE workspace_skill ALTER COLUMN project_id DROP NOT NULL;

ALTER TABLE workspace_mcp_server ALTER COLUMN project_id DROP NOT NULL;

UPDATE workspace_skill SET project_id = NULL WHERE project_id IS NOT NULL;

UPDATE workspace_mcp_server SET project_id = NULL WHERE project_id IS NOT NULL;

ALTER TABLE workspace_agent      ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

ALTER TABLE workspace_skill      ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

ALTER TABLE workspace_mcp_server ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

ALTER TABLE workspace_provider   ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

ALTER TABLE workspace_user       ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

ALTER TABLE workspace_run ADD COLUMN IF NOT EXISTS exec_context JSONB NOT NULL DEFAULT '{}';

ALTER TABLE workspace_trigger ADD COLUMN IF NOT EXISTS org_unit_id TEXT NULL REFERENCES org_unit(id) ON DELETE RESTRICT;

ALTER TABLE workspace_trigger ADD COLUMN IF NOT EXISTS execution_identity_id TEXT NULL REFERENCES execution_identity(id) ON DELETE RESTRICT;

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'initial';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS source_runs TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS evidence_refs TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS knowledge_ids TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS change_summary TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';

ALTER TABLE workspace_project DROP COLUMN IF EXISTS allowed_users;

ALTER TABLE workspace_user DROP COLUMN IF EXISTS projects;

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS observed_problem TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS proposed_change TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_skill_version ADD COLUMN IF NOT EXISTS expected_effect TEXT NOT NULL DEFAULT '';

INSERT INTO channel_transport (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_agent ADD COLUMN IF NOT EXISTS secrets JSONB NOT NULL DEFAULT '[]';

ALTER TABLE channel_transport ADD COLUMN IF NOT EXISTS payload JSONB NOT NULL DEFAULT '{}';

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'channel_transport' AND column_name = 'matrix_homeserver') THEN
        UPDATE channel_transport SET payload = (
            SELECT COALESCE(jsonb_object_agg(channel, fields), '{}'::jsonb)
            FROM (VALUES
                ('matrix',   jsonb_strip_nulls(jsonb_build_object(
                                'homeserver',   NULLIF(matrix_homeserver, ''),
                                'access_token', NULLIF(matrix_access_token, '')))),
                ('telegram', jsonb_strip_nulls(jsonb_build_object(
                                'bot_token',    NULLIF(telegram_bot_token, '')))),
                ('webhook',  jsonb_strip_nulls(jsonb_build_object(
                                'secret',       NULLIF(webhook_secret, ''))))
            ) AS channels(channel, fields)
        ) WHERE id = 1;
        ALTER TABLE channel_transport
            DROP COLUMN matrix_homeserver,
            DROP COLUMN matrix_access_token,
            DROP COLUMN telegram_bot_token,
            DROP COLUMN webhook_secret;
    END IF;
END $$;

ALTER TABLE org_unit ADD COLUMN IF NOT EXISTS head_user_id TEXT REFERENCES workspace_user(id) ON DELETE SET NULL;

ALTER TABLE workspace_project
    ADD COLUMN IF NOT EXISTS intake_enabled BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE workspace_inbound_message
    ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ;

ALTER TABLE org_policy
    ADD COLUMN IF NOT EXISTS intake_mode TEXT NOT NULL DEFAULT ''
    CHECK (intake_mode IN ('', 'deny'));

UPDATE workspace_project SET intake_enabled = true
 WHERE NOT intake_enabled
   AND id IN (SELECT DISTINCT project_id FROM workspace_inbound_message
              WHERE project_id IS NOT NULL);

ALTER TABLE human_request ADD COLUMN IF NOT EXISTS agent_version INT NOT NULL DEFAULT 0;

ALTER TABLE workspace_agent_evaluation_run
    ADD COLUMN IF NOT EXISTS proposal_id BIGINT REFERENCES workspace_agent_proposal(id);

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS verdict TEXT NOT NULL DEFAULT 'unknown';

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS cause TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS threshold JSONB NOT NULL DEFAULT '{}';

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS judge_model TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS judge_same_model BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS judge_degraded BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE workspace_agent_evaluation_run ADD COLUMN IF NOT EXISTS trigger TEXT NOT NULL DEFAULT 'manual';

UPDATE workspace_trigger
SET config = (config #>> '{}')::jsonb
WHERE jsonb_typeof(config) = 'string';

ALTER TABLE workspace_artifact
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'approved',
    ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'reference',
    ADD COLUMN IF NOT EXISTS version INT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS origin_type TEXT NOT NULL DEFAULT 'human',
    ADD COLUMN IF NOT EXISTS origin_user TEXT,
    ADD COLUMN IF NOT EXISTS origin_run TEXT,
    ADD COLUMN IF NOT EXISTS saga_id TEXT;

ALTER TABLE human_request ADD COLUMN IF NOT EXISTS thread_ref TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS external_issuer TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS external_id TEXT NOT NULL DEFAULT '';

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS sourced TEXT NOT NULL DEFAULT 'local';

ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;

UPDATE workspace_agent SET sandbox_profile = ''
WHERE sandbox_profile IN ('restricted', 'standard', 'privileged');

ALTER TABLE workspace_sandbox_profile
    ADD COLUMN IF NOT EXISTS base TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS packages TEXT[] NOT NULL DEFAULT '{}';

ALTER TABLE workspace_run ALTER COLUMN task_id DROP NOT NULL;

ALTER TABLE workspace_sandbox_profile
    ADD COLUMN IF NOT EXISTS docker_socket BOOLEAN NOT NULL DEFAULT FALSE;
