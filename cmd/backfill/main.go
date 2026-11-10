package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mimo/personal-os/internal/config"
)

const maxEmbeddingAttempts = 8

type summary struct {
	MemoryChunksChanged int64
	NoteChunksChanged   int64
	StaleChunksDeleted  int64
	JobsConsidered      int64
	PendingJobs         int64
	FailedJobsSkipped   int64
	DryRun              bool
}

func main() {
	dryRun := flag.Bool("dry-run", false, "report changes and roll them back")
	retryFailed := flag.Bool("retry-failed", false, "reset failed jobs with the current content hash for another bounded retry cycle")
	flag.Parse()

	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("MIMO_OWNER_ID") == "" {
		log.Fatal("MIMO_OWNER_ID must be set explicitly for backfill")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pc, err := pgxpool.ParseConfig(c.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	pc.MaxConns = 2
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	result, err := run(ctx, db, c.OwnerID, c.EmbeddingModel, *dryRun, *retryFailed)
	if err != nil {
		log.Fatal(err)
	}
	printSummary(result, c.OwnerID, c.EmbeddingModel, *retryFailed)
}

func run(ctx context.Context, db *pgxpool.Pool, ownerID, model string, dryRun, retryFailed bool) (summary, error) {
	if ownerID == "" {
		return summary{}, fmt.Errorf("owner ID is required")
	}
	if model == "" {
		return summary{}, fmt.Errorf("embedding model is required")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return summary{}, err
	}
	defer tx.Rollback(ctx)
	result := summary{DryRun: dryRun}

	if result.MemoryChunksChanged, err = changedChunks(ctx, tx, `
WITH source AS (
 SELECT id,scope_id AS owner_id,concat_ws(' ',coalesce(title,''),coalesce(summary,'')) AS body
 FROM mimo.memory_entries
 WHERE scope_type='personal' AND scope_id=$1 AND status='active'
   AND (expires_at IS NULL OR expires_at>now())
), upserted AS (
 INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
 SELECT owner_id,'memory',id,body,encode(digest(body,'sha256'),'hex') FROM source
 ON CONFLICT(owner_id,source_type,source_id) DO UPDATE
 SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now()
 WHERE mimo.search_chunks.content_hash IS DISTINCT FROM EXCLUDED.content_hash
 RETURNING 1
)
SELECT count(*) FROM upserted`, ownerID); err != nil {
		return result, fmt.Errorf("sync memory chunks: %w", err)
	}
	if result.NoteChunksChanged, err = changedChunks(ctx, tx, `
WITH source AS (
 SELECT id,owner_id,concat_ws(E'\n',coalesce(title,''),coalesce(content,'')) AS body
 FROM mimo.notes
 WHERE owner_id=$1 AND deleted_at IS NULL
), upserted AS (
 INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
 SELECT owner_id,'note',id,body,encode(digest(body,'sha256'),'hex') FROM source
 ON CONFLICT(owner_id,source_type,source_id) DO UPDATE
 SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now()
 WHERE mimo.search_chunks.content_hash IS DISTINCT FROM EXCLUDED.content_hash
 RETURNING 1
)
SELECT count(*) FROM upserted`, ownerID); err != nil {
		return result, fmt.Errorf("sync note chunks: %w", err)
	}

	if result.StaleChunksDeleted, err = execCount(ctx, tx, `DELETE FROM mimo.search_chunks c
WHERE c.owner_id=$1 AND c.source_type IN ('memory','note')
AND NOT EXISTS (
 SELECT 1 FROM mimo.memory_entries m
 WHERE c.source_type='memory' AND m.id=c.source_id AND m.scope_type='personal' AND m.scope_id=$1
   AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>now())
)
AND NOT EXISTS (
 SELECT 1 FROM mimo.notes n
 WHERE c.source_type='note' AND n.id=c.source_id AND n.owner_id=$1 AND n.deleted_at IS NULL
)`, ownerID); err != nil {
		return result, fmt.Errorf("delete stale chunks: %w", err)
	}

	if result.JobsConsidered, err = countOrQueue(ctx, tx, ownerID, model, retryFailed); err != nil {
		return result, fmt.Errorf("queue embedding jobs: %w", err)
	}
	if result.PendingJobs, err = countPending(ctx, tx, ownerID, model); err != nil {
		return result, fmt.Errorf("count pending jobs: %w", err)
	}
	if result.FailedJobsSkipped, err = countFailed(ctx, tx, ownerID, model); err != nil {
		return result, fmt.Errorf("count failed jobs: %w", err)
	}

	if dryRun {
		return result, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}

func changedChunks(ctx context.Context, tx pgx.Tx, query, owner string) (int64, error) {
	var count int64
	if err := tx.QueryRow(ctx, query, owner).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func execCount(ctx context.Context, tx pgx.Tx, query, owner string) (int64, error) {
	tag, err := tx.Exec(ctx, query, owner)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func countOrQueue(ctx context.Context, tx pgx.Tx, owner, model string, retryFailed bool) (int64, error) {
	var count int64
	if err := tx.QueryRow(ctx, `
WITH candidates AS (
 SELECT c.id,c.content_hash
 FROM mimo.search_chunks c
 LEFT JOIN mimo.semantic_embeddings e ON e.chunk_id=c.id AND e.model_id=$2 AND e.dimensions=768 AND e.content_hash=c.content_hash
 LEFT JOIN mimo.ingestion_jobs j ON j.chunk_id=c.id AND j.model_id=$2
 WHERE c.owner_id=$1 AND c.source_type IN ('memory','note')
   AND (e.chunk_id IS NULL OR ($3 AND j.status='failed' AND j.content_hash=c.content_hash AND j.attempts < $4)
        OR EXISTS (SELECT 1 FROM mimo.ingestion_jobs old WHERE old.chunk_id=c.id AND old.model_id=$2 AND old.content_hash IS DISTINCT FROM c.content_hash))
), upserted AS (
 INSERT INTO mimo.ingestion_jobs(chunk_id,model_id,content_hash,status,attempts,next_run_at)
 SELECT id,$2,content_hash,'pending',0,now() FROM candidates
 ON CONFLICT(chunk_id,model_id) DO UPDATE SET
   content_hash=EXCLUDED.content_hash,
   status=CASE
     WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash THEN 'pending'
     WHEN mimo.ingestion_jobs.status='done' THEN 'pending'
     WHEN $3 AND mimo.ingestion_jobs.status='failed' THEN 'pending'
     ELSE mimo.ingestion_jobs.status
   END,
   attempts=CASE
     WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash THEN 0
     WHEN mimo.ingestion_jobs.status='done' THEN 0
     WHEN $3 AND mimo.ingestion_jobs.status='failed' THEN 0
     ELSE mimo.ingestion_jobs.attempts
   END,
   next_run_at=CASE
     WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash THEN now()
     WHEN mimo.ingestion_jobs.status='done' THEN now()
     WHEN $3 AND mimo.ingestion_jobs.status='failed' THEN now()
     ELSE mimo.ingestion_jobs.next_run_at
   END,
   error_message=CASE
     WHEN mimo.ingestion_jobs.content_hash IS DISTINCT FROM EXCLUDED.content_hash THEN NULL
     WHEN mimo.ingestion_jobs.status='done' THEN NULL
     WHEN $3 AND mimo.ingestion_jobs.status='failed' THEN NULL
     ELSE mimo.ingestion_jobs.error_message
   END,
   updated_at=now()
 RETURNING 1
)
SELECT count(*) FROM upserted`, owner, model, retryFailed, maxEmbeddingAttempts).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func countPending(ctx context.Context, tx pgx.Tx, owner, model string) (int64, error) {
	var count int64
	err := tx.QueryRow(ctx, `SELECT count(*) FROM mimo.ingestion_jobs j JOIN mimo.search_chunks c ON c.id=j.chunk_id WHERE c.owner_id=$1 AND j.model_id=$2 AND j.status IN ('pending','running')`, owner, model).Scan(&count)
	return count, err
}

func countFailed(ctx context.Context, tx pgx.Tx, owner, model string) (int64, error) {
	var count int64
	err := tx.QueryRow(ctx, `SELECT count(*) FROM mimo.ingestion_jobs j JOIN mimo.search_chunks c ON c.id=j.chunk_id WHERE c.owner_id=$1 AND j.model_id=$2 AND j.status='failed'`, owner, model).Scan(&count)
	return count, err
}

func printSummary(s summary, owner, model string, retryFailed bool) {
	mode := "applied"
	if s.DryRun {
		mode = "dry-run (rolled back)"
	}
	fmt.Printf("Backfill %s for owner=%s model=%s\n", mode, owner, model)
	fmt.Printf("memory chunks changed: %d\nnote chunks changed: %d\nstale chunks deleted: %d\njobs considered: %d\npending/running jobs: %d\nfailed jobs remaining: %d\n", s.MemoryChunksChanged, s.NoteChunksChanged, s.StaleChunksDeleted, s.JobsConsidered, s.PendingJobs, s.FailedJobsSkipped)
	if retryFailed {
		fmt.Println("failed-job retry: enabled for attempts below", maxEmbeddingAttempts)
	} else {
		fmt.Println("failed-job retry: disabled; use --retry-failed explicitly")
	}
}
