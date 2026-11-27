-- Exact transport message addressing is distinct from the thread anchor:
-- several interactions can be posted inside one thread.
ALTER TABLE public.human_request
    ADD COLUMN IF NOT EXISTS source_message_ref TEXT NOT NULL DEFAULT '';
