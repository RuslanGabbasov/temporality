-- Reverses 0021_agent_evolution_v2: drops the v2 additions and restores the
-- evaluation suite/run, golden-case candidate and judge-calibration tables
-- exactly as migration 0001_baseline_v2026.2.21 created them (empty tables —
-- the up-migration dropped their data by design). Like the baseline, every
-- statement is guarded so the file may be re-applied safely.

DROP TABLE IF EXISTS public.workspace_agent_check_case;
DROP TABLE IF EXISTS public.workspace_agent_version_metrics;

ALTER TABLE public.workspace_agent DROP COLUMN IF EXISTS observation_min_runs;
ALTER TABLE public.workspace_agent DROP COLUMN IF EXISTS observation_days;

ALTER TABLE public.workspace_agent_proposal DROP COLUMN IF EXISTS applied_version;
ALTER TABLE public.workspace_agent_proposal DROP COLUMN IF EXISTS context;
ALTER TABLE public.workspace_agent_proposal DROP COLUMN IF EXISTS change_summary;
ALTER TABLE public.workspace_agent_proposal DROP COLUMN IF EXISTS source;

ALTER TABLE public.workspace_agent_version DROP COLUMN IF EXISTS change_summary;
ALTER TABLE public.workspace_agent_version DROP COLUMN IF EXISTS rollback_of;
ALTER TABLE public.workspace_agent_version DROP COLUMN IF EXISTS baseline_version;
ALTER TABLE public.workspace_agent_version DROP COLUMN IF EXISTS kind;

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

ALTER TABLE ONLY public.workspace_agent_evaluation_run ALTER COLUMN id SET DEFAULT nextval('public.workspace_agent_evaluation_run_id_seq'::regclass);

CREATE TABLE IF NOT EXISTS public.workspace_agent_evaluation_suite (
    agent_id text NOT NULL,
    cases jsonb DEFAULT '[]'::jsonb NOT NULL,
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

ALTER TABLE ONLY public.workspace_eval_case_candidate ALTER COLUMN id SET DEFAULT nextval('public.workspace_eval_case_candidate_id_seq'::regclass);

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

ALTER TABLE ONLY public.workspace_judge_calibration ALTER COLUMN id SET DEFAULT nextval('public.workspace_judge_calibration_id_seq'::regclass);

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_agent_evaluation_run_pkey') THEN
        ALTER TABLE ONLY public.workspace_agent_evaluation_run ADD CONSTRAINT workspace_agent_evaluation_run_pkey PRIMARY KEY (id);
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_agent_evaluation_suite_pkey') THEN
        ALTER TABLE ONLY public.workspace_agent_evaluation_suite ADD CONSTRAINT workspace_agent_evaluation_suite_pkey PRIMARY KEY (agent_id);
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_eval_case_candidate_pkey') THEN
        ALTER TABLE ONLY public.workspace_eval_case_candidate ADD CONSTRAINT workspace_eval_case_candidate_pkey PRIMARY KEY (id);
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_judge_calibration_pkey') THEN
        ALTER TABLE ONLY public.workspace_judge_calibration ADD CONSTRAINT workspace_judge_calibration_pkey PRIMARY KEY (id);
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_agent_evaluation_run_agent_id_fkey') THEN
        ALTER TABLE ONLY public.workspace_agent_evaluation_run ADD CONSTRAINT workspace_agent_evaluation_run_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_agent_evaluation_run_proposal_id_fkey') THEN
        ALTER TABLE ONLY public.workspace_agent_evaluation_run ADD CONSTRAINT workspace_agent_evaluation_run_proposal_id_fkey FOREIGN KEY (proposal_id) REFERENCES public.workspace_agent_proposal(id);
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_agent_evaluation_suite_agent_id_fkey') THEN
        ALTER TABLE ONLY public.workspace_agent_evaluation_suite ADD CONSTRAINT workspace_agent_evaluation_suite_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_eval_case_candidate_agent_id_fkey') THEN
        ALTER TABLE ONLY public.workspace_eval_case_candidate ADD CONSTRAINT workspace_eval_case_candidate_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
    END IF;
END $con$;

DO $con$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'workspace_judge_calibration_agent_id_fkey') THEN
        ALTER TABLE ONLY public.workspace_judge_calibration ADD CONSTRAINT workspace_judge_calibration_agent_id_fkey FOREIGN KEY (agent_id) REFERENCES public.workspace_agent(id) ON DELETE CASCADE;
    END IF;
END $con$;

CREATE INDEX IF NOT EXISTS idx_eval_case_candidate_review ON public.workspace_eval_case_candidate USING btree (agent_id, verdict, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_eval_case_candidate_run ON public.workspace_eval_case_candidate USING btree (run_id);

CREATE INDEX IF NOT EXISTS idx_workspace_agent_evaluation_run ON public.workspace_agent_evaluation_run USING btree (agent_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_agent_evaluation_run_gate ON public.workspace_agent_evaluation_run USING btree (agent_id, agent_version, verdict, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_judge_calibration_agent ON public.workspace_judge_calibration USING btree (agent_id, checked_at DESC);

CREATE INDEX IF NOT EXISTS idx_workspace_judge_calibration_model ON public.workspace_judge_calibration USING btree (judge_model, checked_at DESC);
