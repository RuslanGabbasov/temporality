-- HITL forms (docs/interactive-HITL.md §7): the optional form definition of an
-- ask_human request. The workflow bounds it (8KB, properties required) before
-- the row is written; submissions are validated against it server-side before
-- the answer closes the row. NULL = plain question (options + free text), the
-- historical shape — every existing row and caller is unaffected.
ALTER TABLE human_request ADD COLUMN IF NOT EXISTS schema JSONB;
