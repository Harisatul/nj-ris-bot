-- Security hotfix: bind credential owner IDs to exactly one personal workspace.
-- No rows are inserted automatically. Existing deployments must provision the
-- mapping explicitly after applying this additive migration.
BEGIN;

CREATE TABLE IF NOT EXISTS mimo.personal_workspace_owners (
  workspace_id uuid PRIMARY KEY REFERENCES mimo.workspaces(id) ON DELETE CASCADE,
  owner_id text NOT NULL UNIQUE CHECK (length(btrim(owner_id)) BETWEEN 1 AND 200),
  human_principal_id uuid NOT NULL REFERENCES mimo.principals(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS personal_workspace_owners_owner_idx
  ON mimo.personal_workspace_owners(owner_id);

COMMIT;
