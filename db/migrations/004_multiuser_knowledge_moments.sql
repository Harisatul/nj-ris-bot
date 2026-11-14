-- Mimo v0.3 additive multi-user identity, shared knowledge, moments, and curated legacy.
BEGIN;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
ALTER TABLE mimo.search_chunks DROP CONSTRAINT IF EXISTS search_chunks_source_type_check;
ALTER TABLE mimo.search_chunks ADD CONSTRAINT search_chunks_source_type_check CHECK(source_type IN('note','object','asset','memory','knowledge_note','knowledge_discussion','moment'));

CREATE TABLE IF NOT EXISTS mimo.principals (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), slug text NOT NULL UNIQUE,
 kind text NOT NULL CHECK (kind IN ('human','agent')), display_name text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS mimo.workspaces (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), slug text NOT NULL UNIQUE,
 name text NOT NULL, kind text NOT NULL CHECK (kind IN ('personal','shared')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS mimo.workspace_members (
 workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id) ON DELETE CASCADE,
 principal_id uuid NOT NULL REFERENCES mimo.principals(id) ON DELETE CASCADE,
 role text NOT NULL CHECK (role IN ('owner','editor','reader','agent')),
 revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id, principal_id)
);
CREATE TABLE IF NOT EXISTS mimo.agent_credentials (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL UNIQUE,
 agent_principal_id uuid NOT NULL REFERENCES mimo.principals(id),
 token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[a-f0-9]{64}$'),
 revoked_at timestamptz, expires_at timestamptz, last_used_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE mimo.agent_credentials ADD COLUMN IF NOT EXISTS owner_id text;
ALTER TABLE mimo.agent_credentials ADD COLUMN IF NOT EXISTS default_workspace_id uuid REFERENCES mimo.workspaces(id);
CREATE TABLE IF NOT EXISTS mimo.permissions (
 principal_id uuid NOT NULL REFERENCES mimo.principals(id) ON DELETE CASCADE,
 workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id) ON DELETE CASCADE,
 permission text NOT NULL CHECK (permission IN ('memory:read','memory:write','knowledge:read','knowledge:write','moments:read','moments:write','finance:read','finance:write','legacy:manage')),
 PRIMARY KEY(principal_id,workspace_id,permission)
);
CREATE INDEX IF NOT EXISTS agent_credentials_active_idx ON mimo.agent_credentials(token_hash) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS mimo.knowledge_sources (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
 source_type text NOT NULL CHECK (source_type IN ('book','article','paper','website','document','other')),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 500), authors text[] NOT NULL DEFAULT '{}',
 url text, doi text, isbn text, publication_date date, description text NOT NULL DEFAULT '',
 metadata jsonb NOT NULL DEFAULT '{}', verified boolean NOT NULL DEFAULT false,
 created_by uuid NOT NULL REFERENCES mimo.principals(id), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS knowledge_sources_doi_idx ON mimo.knowledge_sources(workspace_id,doi) WHERE doi IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS knowledge_sources_isbn_idx ON mimo.knowledge_sources(workspace_id,isbn) WHERE isbn IS NOT NULL;
CREATE TABLE IF NOT EXISTS mimo.knowledge_notes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), source_id uuid REFERENCES mimo.knowledge_sources(id) ON DELETE SET NULL,
 workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id), author_principal_id uuid NOT NULL REFERENCES mimo.principals(id),
 title text NOT NULL, body text NOT NULL, note_type text NOT NULL DEFAULT 'interpretation',
 visibility text NOT NULL DEFAULT 'workspace' CHECK (visibility IN ('private','workspace')),
 authored_by text NOT NULL DEFAULT 'human' CHECK(authored_by IN ('human','ai')), source_verified boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS mimo.knowledge_discussions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
 source_id uuid REFERENCES mimo.knowledge_sources(id) ON DELETE SET NULL, title text NOT NULL, body text NOT NULL,
 perspectives jsonb NOT NULL DEFAULT '[]', synthesis text, unresolved_questions text[] NOT NULL DEFAULT '{}',
 authored_by text NOT NULL DEFAULT 'human' CHECK(authored_by IN ('human','ai')), created_by uuid NOT NULL REFERENCES mimo.principals(id), discussed_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE mimo.knowledge_discussions ADD COLUMN IF NOT EXISTS source_refs jsonb NOT NULL DEFAULT '[]';
CREATE TABLE IF NOT EXISTS mimo.knowledge_relations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
 from_note_id uuid REFERENCES mimo.knowledge_notes(id) ON DELETE CASCADE, to_note_id uuid REFERENCES mimo.knowledge_notes(id) ON DELETE CASCADE,
 relation text NOT NULL CHECK(relation IN ('related_to','supports','contradicts','inspired_by','revises','discusses')),
 created_by uuid NOT NULL REFERENCES mimo.principals(id), UNIQUE(from_note_id,to_note_id,relation)
);

CREATE TABLE IF NOT EXISTS mimo.moments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
 title text NOT NULL, description text NOT NULL DEFAULT '', occurred_at timestamptz, recorded_at timestamptz NOT NULL DEFAULT now(),
 created_by uuid NOT NULL REFERENCES mimo.principals(id), participants uuid[] NOT NULL DEFAULT '{}', location text,
 tags text[] NOT NULL DEFAULT '{}', visibility text NOT NULL DEFAULT 'workspace' CHECK(visibility IN ('private','workspace')),
 source_refs jsonb NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS mimo.moment_assets (
 moment_id uuid NOT NULL REFERENCES mimo.moments(id) ON DELETE CASCADE, asset_id uuid NOT NULL REFERENCES mimo.assets(id) ON DELETE CASCADE,
 PRIMARY KEY(moment_id,asset_id)
);
CREATE INDEX IF NOT EXISTS moments_timeline_idx ON mimo.moments(workspace_id,occurred_at DESC NULLS LAST);

CREATE TABLE IF NOT EXISTS mimo.legacy_collections (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES mimo.workspaces(id),
 slug text NOT NULL, title text NOT NULL, description text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','approved','archived')),
 created_by uuid NOT NULL REFERENCES mimo.principals(id), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(workspace_id,slug)
);
CREATE TABLE IF NOT EXISTS mimo.legacy_collection_items (
 collection_id uuid NOT NULL REFERENCES mimo.legacy_collections(id) ON DELETE CASCADE,
 item_type text NOT NULL CHECK(item_type IN ('knowledge_note','moment','discussion')), item_id uuid NOT NULL, position int NOT NULL DEFAULT 0,
 PRIMARY KEY(collection_id,item_type,item_id)
);
CREATE TABLE IF NOT EXISTS mimo.legacy_approvals (
 collection_id uuid NOT NULL REFERENCES mimo.legacy_collections(id) ON DELETE CASCADE,
 principal_id uuid NOT NULL REFERENCES mimo.principals(id), approved_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(collection_id,principal_id)
);
COMMIT;
