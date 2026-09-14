ALTER TABLE cognitive_steps
    DROP CONSTRAINT IF EXISTS cognitive_steps_render_metadata_consistent,
    DROP CONSTRAINT IF EXISTS cognitive_steps_render_packet_hash_check,
    DROP COLUMN IF EXISTS attention_version,
    DROP COLUMN IF EXISTS renderer_version,
    DROP COLUMN IF EXISTS model_provenance,
    DROP COLUMN IF EXISTS render_packet_hash,
    DROP COLUMN IF EXISTS render_packet;
