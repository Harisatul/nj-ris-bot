-- Optional: backfill existing personal v0.2 memory into search index.
-- Replace `me` with MIMO_OWNER_ID, then run in Neon SQL Editor.
INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body)
SELECT scope_id,'memory',id,title||' '||summary
FROM mimo.memory_entries
WHERE scope_type='personal' AND scope_id='me' AND status='active' AND (expires_at IS NULL OR expires_at>now())
ON CONFLICT(owner_id,source_type,source_id) DO UPDATE SET body=EXCLUDED.body,updated_at=now();
-- If embeddings are enabled, queue missing/updated chunks using actual model ID:
-- INSERT INTO mimo.ingestion_jobs(chunk_id,model_id,status)
-- SELECT id,'your-embedding-model','pending' FROM mimo.search_chunks
-- ON CONFLICT(chunk_id,model_id) DO UPDATE SET status='pending',next_run_at=now();
