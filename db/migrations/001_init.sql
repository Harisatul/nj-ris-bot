-- Mimo Personal OS v1: compatible with the provided mimo v0.2 tables.
-- Re-running CREATE statements is safe. This is NOT a general-purpose schema diff migrator.
BEGIN;
CREATE SCHEMA IF NOT EXISTS mimo;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS mimo.memory_entries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 scope_type text NOT NULL CHECK (scope_type IN ('personal','shared','group')),
 scope_id text NOT NULL,
 category text NOT NULL CHECK (category IN ('fact','preference','moment','plan','summary','journal')),
 title text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
 summary text NOT NULL CHECK (length(summary) BETWEEN 1 AND 8000),
 source_kind text CHECK (source_kind IN ('whatsapp','document','photo','manual','system')),
 source_ref text, occurred_at timestamptz,
 importance smallint NOT NULL DEFAULT 3 CHECK (importance BETWEEN 1 AND 5),
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','deleted')),
 expires_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple',coalesce(title,'')||' '||coalesce(summary,''))) STORED
);
CREATE INDEX IF NOT EXISTS memory_scope_recent_idx ON mimo.memory_entries(scope_type,scope_id,occurred_at DESC NULLS LAST) WHERE status='active';
CREATE INDEX IF NOT EXISTS memory_search_idx ON mimo.memory_entries USING gin(search_tsv);
ALTER TABLE mimo.memory_entries ADD COLUMN IF NOT EXISTS subject_key text;
ALTER TABLE mimo.memory_entries ADD COLUMN IF NOT EXISTS confidence numeric(3,2) CHECK (confidence BETWEEN 0 AND 1);
ALTER TABLE mimo.memory_entries ADD COLUMN IF NOT EXISTS superseded_by uuid REFERENCES mimo.memory_entries(id);

CREATE TABLE IF NOT EXISTS mimo.memory_embeddings (
 memory_id uuid NOT NULL REFERENCES mimo.memory_entries(id) ON DELETE CASCADE,
 model_id text NOT NULL, embedding vector NOT NULL,
 content_hash text CHECK (content_hash IS NULL OR content_hash ~ '^[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(memory_id,model_id),
 CONSTRAINT embedding_dimension_range CHECK(vector_dims(embedding) BETWEEN 1 AND 2000)
);
CREATE INDEX IF NOT EXISTS memory_embeddings_model_idx ON mimo.memory_embeddings(model_id);

CREATE TABLE IF NOT EXISTS mimo.assets (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 scope_type text NOT NULL CHECK(scope_type IN ('personal','shared','group')),
 scope_id text NOT NULL, bucket_name text NOT NULL, object_key text NOT NULL,
 mime_type text, size_bytes bigint CHECK(size_bytes>=0),
 sha256_hex text CHECK (sha256_hex IS NULL OR sha256_hex ~ '^[a-f0-9]{64}$'),
 caption text, source_ref text, captured_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','deleted')),
 UNIQUE(bucket_name,object_key)
);
CREATE INDEX IF NOT EXISTS assets_scope_date_idx ON mimo.assets(scope_type,scope_id,captured_at DESC NULLS LAST) WHERE status='active';
CREATE TABLE IF NOT EXISTS mimo.memory_assets (
 memory_id uuid NOT NULL REFERENCES mimo.memory_entries(id) ON DELETE CASCADE,
 asset_id uuid NOT NULL REFERENCES mimo.assets(id) ON DELETE CASCADE,
 PRIMARY KEY(memory_id,asset_id)
);
CREATE TABLE IF NOT EXISTS mimo.scope_members (
 scope_type text NOT NULL CHECK(scope_type IN ('personal','shared','group')),
 scope_id text NOT NULL, principal text NOT NULL,
 role text NOT NULL CHECK(role IN('owner','editor','reader')),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(scope_type,scope_id,principal)
);
CREATE TABLE IF NOT EXISTS mimo.audit_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 occurred_at timestamptz NOT NULL DEFAULT now(), principal text,
 scope_type text,scope_id text,action text NOT NULL, resource_id uuid,
 outcome text NOT NULL CHECK(outcome IN('allowed','denied','error'))
);
CREATE INDEX IF NOT EXISTS audit_date_idx ON mimo.audit_events(occurred_at);

CREATE TABLE IF NOT EXISTS mimo.notes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_id text NOT NULL,
 kind text NOT NULL DEFAULT 'general' CHECK(kind IN ('general','work','idea','journal')),
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 300),
 content text NOT NULL DEFAULT '' CHECK(length(content)<=100000),
 tags text[] NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz,
 search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple',coalesce(title,'')||' '||coalesce(content,''))) STORED
);
CREATE INDEX IF NOT EXISTS notes_owner_idx ON mimo.notes(owner_id,updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS notes_search_idx ON mimo.notes USING gin(search_tsv);
CREATE TABLE IF NOT EXISTS mimo.note_revisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 note_id uuid NOT NULL REFERENCES mimo.notes(id) ON DELETE CASCADE,
 previous_title text NOT NULL,previous_content text NOT NULL,
 saved_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS mimo.objects (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),owner_id text NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 300),
 description text NOT NULL DEFAULT '',category text,
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS objects_owner_idx ON mimo.objects(owner_id,updated_at DESC) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS mimo.object_assets (
 object_id uuid NOT NULL REFERENCES mimo.objects(id) ON DELETE CASCADE,
 asset_id uuid NOT NULL REFERENCES mimo.assets(id) ON DELETE CASCADE,
 PRIMARY KEY(object_id,asset_id)
);

CREATE TABLE IF NOT EXISTS mimo.finance_accounts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),owner_id text NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
 kind text NOT NULL CHECK(kind IN('cash','bank','ewallet','other')),
 opening_balance_idr bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),archived_at timestamptz,
 UNIQUE(owner_id,name)
);
CREATE TABLE IF NOT EXISTS mimo.finance_categories (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), owner_id text NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
 kind text NOT NULL CHECK(kind IN('income','expense')),
 UNIQUE(owner_id,name,kind)
);
CREATE TABLE IF NOT EXISTS mimo.finance_transactions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),owner_id text NOT NULL,
 type text NOT NULL CHECK(type IN('income','expense','transfer')),
 amount_idr bigint NOT NULL CHECK(amount_idr>0),
 from_account_id uuid REFERENCES mimo.finance_accounts(id),
 to_account_id uuid REFERENCES mimo.finance_accounts(id),
 category_id uuid REFERENCES mimo.finance_categories(id),
 description text NOT NULL DEFAULT '',
 occurred_at timestamptz NOT NULL DEFAULT now(),
 status text NOT NULL DEFAULT 'posted' CHECK(status IN('posted','voided')),
 idempotency_key text,request_hash text,
 replaces_id uuid REFERENCES mimo.finance_transactions(id),
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (
  (type='expense' AND from_account_id IS NOT NULL AND to_account_id IS NULL)
  OR (type='income' AND to_account_id IS NOT NULL AND from_account_id IS NULL)
  OR (type='transfer' AND from_account_id IS NOT NULL AND to_account_id IS NOT NULL AND from_account_id<>to_account_id AND category_id IS NULL)
 )
);
CREATE UNIQUE INDEX IF NOT EXISTS finance_idem_idx ON mimo.finance_transactions(owner_id,idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS finance_owner_time_idx ON mimo.finance_transactions(owner_id,occurred_at DESC) WHERE status='posted';
CREATE TABLE IF NOT EXISTS mimo.finance_transaction_revisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 transaction_id uuid NOT NULL REFERENCES mimo.finance_transactions(id) ON DELETE CASCADE,
 snapshot jsonb NOT NULL, saved_at timestamptz NOT NULL DEFAULT now()
);

-- Search index is derived from canonical notes, objects, assets, memory records.
CREATE TABLE IF NOT EXISTS mimo.search_chunks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),owner_id text NOT NULL,
 source_type text NOT NULL CHECK(source_type IN('note','object','asset','memory')),
 source_id uuid NOT NULL,body text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 search_tsv tsvector GENERATED ALWAYS AS(to_tsvector('simple',body)) STORED,
 UNIQUE(owner_id,source_type,source_id)
);
CREATE INDEX IF NOT EXISTS search_chunks_tsv_idx ON mimo.search_chunks USING gin(search_tsv);
CREATE INDEX IF NOT EXISTS search_chunks_owner_idx ON mimo.search_chunks(owner_id,source_type);
CREATE TABLE IF NOT EXISTS mimo.search_embeddings (
 chunk_id uuid NOT NULL REFERENCES mimo.search_chunks(id) ON DELETE CASCADE,
 model_id text NOT NULL, embedding vector NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(chunk_id,model_id),
 CONSTRAINT search_embedding_dims CHECK(vector_dims(embedding) BETWEEN 1 AND 2000)
);
CREATE TABLE IF NOT EXISTS mimo.ingestion_jobs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 chunk_id uuid NOT NULL REFERENCES mimo.search_chunks(id) ON DELETE CASCADE,
 model_id text NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','running','done','failed')),
 attempts int NOT NULL DEFAULT 0, next_run_at timestamptz NOT NULL DEFAULT now(),
 error_message text,created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(chunk_id,model_id)
);
CREATE INDEX IF NOT EXISTS ingestion_job_ready_idx ON mimo.ingestion_jobs(next_run_at,id) WHERE status IN('pending','failed');

CREATE OR REPLACE FUNCTION mimo.touch_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.updated_at=now();RETURN NEW; END; $$;
DROP TRIGGER IF EXISTS notes_touch ON mimo.notes;
CREATE TRIGGER notes_touch BEFORE UPDATE ON mimo.notes FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS objects_touch ON mimo.objects;
CREATE TRIGGER objects_touch BEFORE UPDATE ON mimo.objects FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS finance_touch ON mimo.finance_transactions;
CREATE TRIGGER finance_touch BEFORE UPDATE ON mimo.finance_transactions FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
DROP TRIGGER IF EXISTS memory_touch ON mimo.memory_entries;
CREATE TRIGGER memory_touch BEFORE UPDATE ON mimo.memory_entries FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();
COMMIT;
