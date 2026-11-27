-- A team ask can be posted into distinct chats, with a different transport
-- message/thread id for each recipient copy.
ALTER TABLE public.human_request_delivery
    ADD COLUMN IF NOT EXISTS thread_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE public.human_request_delivery
    ADD COLUMN IF NOT EXISTS source_message_ref TEXT NOT NULL DEFAULT '';
