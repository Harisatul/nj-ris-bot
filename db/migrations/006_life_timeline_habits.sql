-- Life timeline, deterministic coverage, habits, and durable check-in state.
-- Additive migration: no legacy tables or ownership models are changed.
BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS mimo.api_idempotency (
  owner_id text NOT NULL,
  idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 200),
  request_hash text NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  status_code int NOT NULL CHECK (status_code BETWEEN 200 AND 599),
  response_json jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(owner_id,idempotency_key)
);

CREATE TABLE IF NOT EXISTS mimo.activity_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id text NOT NULL CHECK (length(btrim(owner_id)) BETWEEN 1 AND 200),
  principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  workspace_id uuid REFERENCES mimo.workspaces(id),
  title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 500),
  description text NOT NULL DEFAULT '' CHECK (length(description) <= 10000),
  category text NOT NULL DEFAULT 'other' CHECK (length(btrim(category)) BETWEEN 1 AND 100),
  tags text[] NOT NULL DEFAULT '{}',
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  event_type text NOT NULL DEFAULT 'activity' CHECK(event_type IN ('activity','habit','checkin')),
  time_precision text NOT NULL DEFAULT 'exact' CHECK(time_precision IN ('exact','approximate','date_only','unknown')),
  source_kind text NOT NULL DEFAULT 'manual' CHECK(source_kind IN ('whatsapp','manual','api','checkin','system')),
  source_message_id text,
  status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','voided')),
  version int NOT NULL DEFAULT 1 CHECK(version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK(ended_at IS NULL OR ended_at >= started_at),
  UNIQUE(owner_id,id)
);
CREATE INDEX IF NOT EXISTS activity_owner_time_idx ON mimo.activity_events(owner_id,started_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS activity_owner_category_idx ON mimo.activity_events(owner_id,category,started_at DESC);
CREATE INDEX IF NOT EXISTS activity_workspace_time_idx ON mimo.activity_events(workspace_id,started_at DESC);
CREATE INDEX IF NOT EXISTS activity_source_message_idx ON mimo.activity_events(owner_id,source_message_id) WHERE source_message_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS mimo.activity_revisions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  activity_id uuid NOT NULL REFERENCES mimo.activity_events(id) ON DELETE CASCADE,
  version int NOT NULL,
  snapshot jsonb NOT NULL,
  changed_by uuid REFERENCES mimo.principals(id),
  changed_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(activity_id,version)
);

CREATE TABLE IF NOT EXISTS mimo.habit_definitions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id text NOT NULL,
  principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  workspace_id uuid REFERENCES mimo.workspaces(id),
  name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 300),
  metric text NOT NULL CHECK(metric IN ('frequency','duration','completion')),
  target numeric(12,2) NOT NULL CHECK(target > 0),
  period text NOT NULL CHECK(period IN ('day','week')),
  category text,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS habit_owner_name_active_idx ON mimo.habit_definitions(owner_id,lower(name)) WHERE active;

CREATE TABLE IF NOT EXISTS mimo.habit_observations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  habit_id uuid NOT NULL REFERENCES mimo.habit_definitions(id) ON DELETE CASCADE,
  owner_id text NOT NULL,
  activity_id uuid REFERENCES mimo.activity_events(id) ON DELETE SET NULL,
  observed_on date NOT NULL,
  value numeric(12,2) NOT NULL CHECK(value >= 0),
  source text NOT NULL DEFAULT 'manual' CHECK(source IN ('manual','activity','checkin')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(habit_id,activity_id,observed_on,source)
);
CREATE INDEX IF NOT EXISTS habit_observation_period_idx ON mimo.habit_observations(owner_id,observed_on);

CREATE TABLE IF NOT EXISTS mimo.checkin_settings (
  owner_id text PRIMARY KEY,
  principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  enabled boolean NOT NULL DEFAULT false,
  strategy text NOT NULL DEFAULT 'adaptive' CHECK(strategy='adaptive'),
  interval_minutes int NOT NULL DEFAULT 150 CHECK(interval_minutes BETWEEN 15 AND 1440),
  cooldown_minutes int NOT NULL DEFAULT 150 CHECK(cooldown_minutes BETWEEN 0 AND 1440),
  max_per_day int NOT NULL DEFAULT 4 CHECK(max_per_day BETWEEN 0 AND 24),
  quiet_start time NOT NULL DEFAULT '23:00', quiet_end time NOT NULL DEFAULT '07:00',
  timezone text NOT NULL DEFAULT 'Asia/Jakarta',
  pause_after_unanswered int NOT NULL DEFAULT 2 CHECK(pause_after_unanswered BETWEEN 1 AND 20),
  snoozed_until timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mimo.checkin_requests (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_id text NOT NULL,
  principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  requested_for timestamptz NOT NULL,
  state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','queued','sending','sent','answered','skipped','expired','suppressed','failed','delivery_unknown','snoozed')),
  activity_gap_start timestamptz, activity_gap_end timestamptz,
  provider_message_id text,
  attempts int NOT NULL DEFAULT 0,
  error_message text,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK(activity_gap_end IS NULL OR activity_gap_start IS NULL OR activity_gap_end > activity_gap_start)
);
CREATE INDEX IF NOT EXISTS checkin_ready_idx ON mimo.checkin_requests(state,requested_for);
CREATE INDEX IF NOT EXISTS checkin_owner_idx ON mimo.checkin_requests(owner_id,created_at DESC);

DROP TRIGGER IF EXISTS activity_touch ON mimo.activity_events;
CREATE TRIGGER activity_touch BEFORE UPDATE ON mimo.activity_events FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS habit_touch ON mimo.habit_definitions;
CREATE TRIGGER habit_touch BEFORE UPDATE ON mimo.habit_definitions FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS checkin_settings_touch ON mimo.checkin_settings;
CREATE TRIGGER checkin_settings_touch BEFORE UPDATE ON mimo.checkin_settings FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS checkin_request_touch ON mimo.checkin_requests;
CREATE TRIGGER checkin_request_touch BEFORE UPDATE ON mimo.checkin_requests FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();

ALTER TABLE mimo.permissions DROP CONSTRAINT IF EXISTS permissions_permission_check;
ALTER TABLE mimo.permissions ADD CONSTRAINT permissions_permission_check CHECK(permission IN ('memory:read','memory:write','knowledge:read','knowledge:write','moments:read','moments:write','finance:read','finance:write','timeline:read','timeline:write','legacy:manage'));
COMMIT;
