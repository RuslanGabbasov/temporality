CREATE OR REPLACE FUNCTION guard_claim_mutation() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' OR current_setting('temporality.claim_transition', true) IS DISTINCT FROM '1' THEN
        RAISE EXCEPTION 'FRP claims may only change through a canonical transition';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS claims_canonical_transitions ON claims;
CREATE TRIGGER claims_canonical_transitions
BEFORE UPDATE OR DELETE ON claims
FOR EACH ROW EXECUTE FUNCTION guard_claim_mutation();
