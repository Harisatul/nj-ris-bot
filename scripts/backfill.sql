-- Optional: backfill existing personal v0.2 memory into search index.
-- Replace `me` with MIMO_OWNER_ID, then run in Neon SQL Editor.
INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
SELECT scope_id,'memory',id,title||' '||summary,encode(digest(title||' '||summary,'sha256'),'hex')
FROM mimo.memory_entries
WHERE scope_type='personal' AND scope_id='me' AND status='active' AND (expires_at IS NULL OR expires_at>now())
ON CONFLICT(owner_id,source_type,source_id) DO UPDATE SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now();
-- Queue all existing personal memory and note chunks for EmbeddingGemma.
INSERT INTO mimo.ingestion_jobs(chunk_id,model_id,content_hash,status,next_run_at)
SELECT id,'embeddinggemma-2:440m',content_hash,'pending',now() FROM mimo.search_chunks
WHERE owner_id='me' AND source_type IN ('memory','note')
ON CONFLICT(chunk_id,model_id) DO UPDATE SET content_hash=EXCLUDED.content_hash,status='pending',next_run_at=now(),error_message=NULL;
