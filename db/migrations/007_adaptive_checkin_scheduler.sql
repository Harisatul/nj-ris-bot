-- P2A: durable adaptive check-in scheduling and claim/recovery metadata.
-- Additive and forward-only. Outbound delivery remains an application opt-in.
BEGIN;

ALTER TABLE mimo.checkin_settings
  ADD COLUMN IF NOT EXISTS workspace_id uuid REFERENCES mimo.workspaces(id),
  ADD COLUMN IF NOT EXISTS gap_threshold_minutes int NOT NULL DEFAULT 150 CHECK(gap_threshold_minutes BETWEEN 15 AND 1440);
UPDATE mimo.checkin_settings s
SET workspace_id=pwo.workspace_id
FROM mimo.personal_workspace_owners pwo
WHERE s.workspace_id IS NULL AND pwo.owner_id=s.owner_id;
CREATE INDEX IF NOT EXISTS checkin_settings_enabled_idx ON mimo.checkin_settings(enabled,owner_id) WHERE enabled;

ALTER TABLE mimo.checkin_requests
  ADD COLUMN IF NOT EXISTS workspace_id uuid REFERENCES mimo.workspaces(id),
  ADD COLUMN IF NOT EXISTS dedupe_key text,
  ADD COLUMN IF NOT EXISTS expires_at timestamptz,
  ADD COLUMN IF NOT EXISTS snoozed_until timestamptz,
  ADD COLUMN IF NOT EXISTS lease_owner text,
  ADD COLUMN IF NOT EXISTS lease_until timestamptz,
  ADD COLUMN IF NOT EXISTS queued_at timestamptz,
  ADD COLUMN IF NOT EXISTS sending_at timestamptz,
  ADD COLUMN IF NOT EXISTS sent_at timestamptz,
  ADD COLUMN IF NOT EXISTS answered_at timestamptz,
  ADD COLUMN IF NOT EXISTS closed_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_error_at timestamptz;
UPDATE mimo.checkin_requests r
SET workspace_id=s.workspace_id
FROM mimo.checkin_settings s
WHERE r.workspace_id IS NULL AND s.owner_id=r.owner_id AND s.workspace_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS checkin_owner_dedupe_idx ON mimo.checkin_requests(owner_id,dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS checkin_claim_idx ON mimo.checkin_requests(state,lease_until,requested_for) WHERE state IN ('queued','sending');
CREATE INDEX IF NOT EXISTS checkin_gap_idx ON mimo.checkin_requests(owner_id,activity_gap_start,activity_gap_end) WHERE state IN ('pending','queued','sending','sent','snoozed');

CREATE OR REPLACE FUNCTION mimo.validate_checkin_request_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state = NEW.state THEN RETURN NEW; END IF;
  IF (OLD.state,NEW.state) IN (
    ('pending','queued'),('pending','suppressed'),('pending','snoozed'),
    ('queued','sending'),('queued','suppressed'),('queued','snoozed'),
    ('sending','sent'),('sending','failed'),('sending','delivery_unknown'),
    ('sent','answered'),('sent','skipped'),('sent','expired'),('sent','snoozed'),
    ('snoozed','pending'),('snoozed','queued'),('snoozed','suppressed')
  ) THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'invalid check-in request transition: % -> %', OLD.state, NEW.state USING ERRCODE='23514';
END;
$$;
DROP TRIGGER IF EXISTS checkin_request_transition_guard ON mimo.checkin_requests;
CREATE TRIGGER checkin_request_transition_guard BEFORE UPDATE OF state ON mimo.checkin_requests
FOR EACH ROW EXECUTE FUNCTION mimo.validate_checkin_request_transition();

COMMIT;
