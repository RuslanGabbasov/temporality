ALTER TABLE cognitive_steps
    ADD COLUMN IF NOT EXISTS render_packet JSONB,
        ADD COLUMN IF NOT EXISTS render_packet_raw BYTEA,
    ADD COLUMN IF NOT EXISTS render_packet_hash TEXT,
    ADD COLUMN IF NOT EXISTS model_provenance JSONB,
    ADD COLUMN IF NOT EXISTS renderer_version TEXT,
    ADD COLUMN IF NOT EXISTS attention_version TEXT;

ALTER TABLE cognitive_steps DROP CONSTRAINT IF EXISTS cognitive_steps_render_packet_hash_check;
ALTER TABLE cognitive_steps ADD CONSTRAINT cognitive_steps_render_packet_hash_check
    CHECK (render_packet_hash IS NULL OR render_packet_hash ~ '^[0-9a-f]{64}$');

ALTER TABLE cognitive_steps DROP CONSTRAINT IF EXISTS cognitive_steps_render_metadata_consistent;
ALTER TABLE cognitive_steps ADD CONSTRAINT cognitive_steps_render_metadata_consistent CHECK (
    (render_packet IS NULL AND render_packet_hash IS NULL AND model_provenance IS NULL AND renderer_version IS NULL AND attention_version IS NULL)
    OR
    (render_packet IS NOT NULL AND render_packet_hash IS NOT NULL AND model_provenance IS NOT NULL AND renderer_version IS NOT NULL AND attention_version IS NOT NULL)
);
