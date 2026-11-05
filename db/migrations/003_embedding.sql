-- Semantic memory embeddings for EmbeddingGemma 2 440M (768 dimensions).
-- Existing memory_embeddings/search_embeddings rows are intentionally preserved.
BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE mimo.search_chunks ADD COLUMN IF NOT EXISTS content_hash text;
UPDATE mimo.search_chunks
SET content_hash=encode(digest(body,'sha256'),'hex')
WHERE content_hash IS NULL;
ALTER TABLE mimo.search_chunks ALTER COLUMN content_hash SET NOT NULL;
ALTER TABLE mimo.search_chunks DROP CONSTRAINT IF EXISTS search_chunks_content_hash_check;
ALTER TABLE mimo.search_chunks
  ADD CONSTRAINT search_chunks_content_hash_check CHECK (content_hash ~ '^[a-f0-9]{64}$');

ALTER TABLE mimo.ingestion_jobs ADD COLUMN IF NOT EXISTS content_hash text;
UPDATE mimo.ingestion_jobs j
SET content_hash=c.content_hash
FROM mimo.search_chunks c
WHERE c.id=j.chunk_id AND j.content_hash IS NULL;
ALTER TABLE mimo.ingestion_jobs
  DROP CONSTRAINT IF EXISTS ingestion_jobs_content_hash_check;
ALTER TABLE mimo.ingestion_jobs
  ADD CONSTRAINT ingestion_jobs_content_hash_check CHECK (content_hash IS NULL OR content_hash ~ '^[a-f0-9]{64}$');

CREATE TABLE IF NOT EXISTS mimo.semantic_embeddings (
  chunk_id uuid NOT NULL REFERENCES mimo.search_chunks(id) ON DELETE CASCADE,
  model_id text NOT NULL,
  dimensions smallint NOT NULL DEFAULT 768 CHECK (dimensions=768),
  content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
  embedding vector(768) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(chunk_id,model_id)
);
CREATE INDEX IF NOT EXISTS semantic_embeddings_model_idx ON mimo.semantic_embeddings(model_id);
CREATE INDEX IF NOT EXISTS semantic_embeddings_hash_idx ON mimo.semantic_embeddings(content_hash);

DROP TRIGGER IF EXISTS semantic_embeddings_touch ON mimo.semantic_embeddings;
CREATE TRIGGER semantic_embeddings_touch BEFORE UPDATE ON mimo.semantic_embeddings FOR EACH ROW EXECUTE FUNCTION mimo.touch_updated_at();

COMMIT;
