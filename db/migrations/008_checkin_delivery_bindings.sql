-- P2B: trusted WhatsApp recipient bindings, durable delivery attempts,
-- and idempotent inbound response correlation.
BEGIN;

CREATE TABLE IF NOT EXISTS mimo.checkin_delivery_bindings (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id text NOT NULL,
  principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
  agent_id text NOT NULL CHECK (agent_id IN ('neura','najwa')),
  channel text NOT NULL DEFAULT 'whatsapp' CHECK (channel='whatsapp'),
  account_id text NOT NULL CHECK (length(btrim(account_id)) BETWEEN 1 AND 200),
  recipient_jid text NOT NULL CHECK (recipient_jid ~ '^[^@[:space:]]+@s\.whatsapp\.net$'),
  recipient_kind text NOT NULL DEFAULT 'direct' CHECK (recipient_kind='direct'),
  verified boolean NOT NULL DEFAULT false,
  enabled boolean NOT NULL DEFAULT false,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id,principal_id,workspace_id,channel),
  UNIQUE(channel,account_id,recipient_jid)
);
CREATE INDEX IF NOT EXISTS checkin_binding_lookup_idx ON mimo.checkin_delivery_bindings(owner_id,principal_id,workspace_id,channel) WHERE enabled AND verified;

CREATE TABLE IF NOT EXISTS mimo.checkin_delivery_attempts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  checkin_request_id uuid NOT NULL REFERENCES mimo.checkin_requests(id) ON DELETE CASCADE,
  attempt_number int NOT NULL CHECK (attempt_number > 0),
  account_id text NOT NULL,
  recipient_jid text NOT NULL,
  status text NOT NULL CHECK(status IN ('started','provider_accepted','confirmed_failed','unknown')),
  provider_message_id text,
  error_class text,
  started_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  UNIQUE(checkin_request_id,attempt_number)
);
CREATE INDEX IF NOT EXISTS checkin_attempt_provider_idx ON mimo.checkin_delivery_attempts(provider_message_id) WHERE provider_message_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS mimo.checkin_inbound_responses (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id text NOT NULL,
  principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
  checkin_request_id uuid REFERENCES mimo.checkin_requests(id) ON DELETE SET NULL,
  channel text NOT NULL DEFAULT 'whatsapp' CHECK(channel='whatsapp'),
  account_id text NOT NULL,
  sender_jid text NOT NULL CHECK (sender_jid ~ '^[^@[:space:]]+@s\.whatsapp\.net$'),
  message_id text NOT NULL CHECK(length(btrim(message_id)) BETWEEN 1 AND 300),
  reply_to_provider_message_id text,
  response_hash text NOT NULL CHECK(response_hash ~ '^[a-f0-9]{64}$'),
  outcome text NOT NULL CHECK(outcome IN ('activities','skip','snooze','clarification','rejected')),
  received_at timestamptz NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(owner_id,message_id)
);
CREATE INDEX IF NOT EXISTS checkin_inbound_reply_idx ON mimo.checkin_inbound_responses(owner_id,reply_to_provider_message_id) WHERE reply_to_provider_message_id IS NOT NULL;

DROP TRIGGER IF EXISTS checkin_binding_touch ON mimo.checkin_delivery_bindings;
CREATE TRIGGER checkin_binding_touch BEFORE UPDATE ON mimo.checkin_delivery_bindings FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();

COMMIT;
