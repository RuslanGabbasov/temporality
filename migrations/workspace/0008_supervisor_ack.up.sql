-- Supervisor acknowledgements become durable state (a set of closed verdict
-- event ids) instead of a replay of the newest ack events: the event-window
-- replay resurrected closed rows once more than ~500 acknowledgements
-- accumulated, which made single and bulk queue actions unreliable.
CREATE TABLE IF NOT EXISTS public.supervisor_ack (
    verdict_event_id text NOT NULL,
    actor text NOT NULL DEFAULT ''::text,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (verdict_event_id)
);
