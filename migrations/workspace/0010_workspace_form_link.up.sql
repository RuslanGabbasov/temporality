-- Hosted form links (docs/interactive-HITL.md §6.2): a scoped, expiring token
-- that lets a chat recipient open one specific ask_human question as a
-- minimalist web form — without an account in the harness. The token IS the
-- credential for exactly one question: it carries no user identity, answers
-- are audited as 'form-link', and it stops working the moment the question is
-- answered/cancelled/expired.
CREATE TABLE IF NOT EXISTS workspace_form_link (
    token text NOT NULL,
    operation_id text NOT NULL,
    run_id text NOT NULL DEFAULT ''::text,
    project_id text NOT NULL DEFAULT ''::text,
    agent_id text NOT NULL DEFAULT ''::text,
    created_by text NOT NULL DEFAULT ''::text,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    expires_at timestamp with time zone NOT NULL,
    answered_at timestamp with time zone,
    PRIMARY KEY (token)
);
CREATE INDEX IF NOT EXISTS idx_workspace_form_link_operation ON workspace_form_link(operation_id);
