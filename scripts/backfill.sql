-- Legacy SQL fallback. Prefer: docker compose run --rm --no-deps api mimo-backfill
-- Required psql variables: -v owner_id="$MIMO_OWNER_ID" -v model_id="$EMBEDDING_MODEL"
-- Optional: -v retry_failed=false
\set ON_ERROR_STOP on
\if :{?retry_failed}
\else
\set retry_failed false
\endif

BEGIN;

WITH source AS (
  SELECT id,owner_id,concat_ws(' ',coalesce(title,''),coalesce(summary,'')) AS body
  FROM mimo.memory_entries
  WHERE scope_type='personal' AND scope_id :'owner_id' AND status='active'
    AND (expires_at IS NULL OR expires_at>now())
), upserted AS (
  INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
  SELECT owner_id,'memory',id,body,encode(digest(body,'sha256'),'hex') FROM source
  ON CONFLICT(owner_id,source_type,source_id) DO UPDATE
  SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now()
  WHERE mimo.search_chunks.content_hash IS DISTINCT FROM EXCLUDED.content_hash
  RETURNING 1
)
SELECT 'memory chunks changed' AS metric,count(*) FROM upserted;

WITH source AS (
  SELECT id,owner_id,concat_ws(E'\n',coalesce(title,''),coalesce(content,'')) AS body
  FROM mimo.notes
  WHERE owner_id :'owner_id' AND deleted_at IS NULL
), upserted AS (
  INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
  SELECT owner_id,'note',id,body,encode(digest(body,'sha256'),'hex') FROM source
  ON CONFLICT(owner_id,source_type,source_id) DO UPDATE
  SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now()
  WHERE mimo.search_chunks.content_hash IS DISTINCT FROM EXCLUDED.content_hash
  RETURNING 1
)
SELECT 'note chunks changed' AS metric,count(*) FROM upserted;

DELETE FROM mimo.search_chunks c
WHERE c.owner_id :'owner_id' AND c.source_type IN ('memory','note')
AND NOT EXISTS (
  SELECT 1 FROM mimo.memory_entries m
  WHERE c.source_type='memory' AND m.id=c.source_id AND m.scope_type='personal' AND m.scope_id :'owner_id'
    AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>now())
)
AND NOT EXISTS (
  SELECT 1 FROM mimo.notes n
  WHERE c.source_type='note' AND n.id=c.source_id AND n.owner_id :'owner_id' AND n.deleted_at IS NULL
);

WITH candidates AS (
  SELECT c.id,c.content_hash
  FROM mimo.search_chunks c
  LEFT JOIN mimo.semantic_embeddings e ON e.chunk_id=c.id AND e.model_id :'model_id' AND e.dimensions=768 AND e.content_hash=c.content_hash
  LEFT JOIN mimo.ingestion_jobs j ON j.chunk_id=c.id AND j.model_id :'model_id'
  WHERE c.owner_id :'owner_id' AND c.source_type IN ('memory','note')
    AND (e.chunk_id IS NULL OR (:retry_failed AND j.status='failed' AND j.content_hash=c.content_hash AND j.attempts<8)
         OR EXISTS (SELECT 1 FROM mimo.ingestion_jobs old WHERE old.chunk_id=c.id AND old.model_id :'model_id' AND old.content_hash IS DISTINCT FROM c.content_hash))
), upserted AS (
  INSERT INTO mimo.ingestion_jobs(chunk_id,model_id,content_hash,status,attempts,next_run_at)
  SELECT id,:'model_id',content_hash,'pending',0,now() FROM candidates
  ON CONFLICT(chunk_id,model_id) DO UPDATE SET
    content_hash=EXCLUDED.content_hash,
    status=CASE WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash OR mimo.ingestion_jobs.status='done' OR (:retry_failed AND mimo.ingestion_jobs.status='failed') THEN 'pending' ELSE mimo.ingestion_jobs.status END,
    attempts=CASE WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash OR mimo.ingestion_jobs.status='done' OR (:retry_failed AND mimo.ingestion_jobs.status='failed') THEN 0 ELSE mimo.ingestion_jobs.attempts END,
    next_run_at=CASE WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash OR mimo.ingestion_jobs.status='done' OR (:retry_failed AND mimo.ingestion_jobs.status='failed') THEN now() ELSE mimo.ingestion_jobs.next_run_at END,
    error_message=CASE WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash OR mimo.ingestion_jobs.status='done' OR (:retry_failed AND mimo.ingestion_jobs.status='failed') THEN NULL ELSE mimo.ingestion_jobs.error_message END,
    updated_at=now()
  RETURNING 1
)
SELECT 'jobs considered' AS metric,count(*) FROM upserted;

COMMIT;
