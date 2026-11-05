package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mimo/personal-os/internal/config"
	"github.com/mimo/personal-os/internal/embedding"
)

func main() {
	c, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	var ec *embedding.Client
	if c.HasEmbedding() {
		ec = embedding.New(c.EmbeddingURL, c.EmbeddingKey, c.EmbeddingModel, c.EmbeddingDimensions)
	}
	log.Printf("Mimo worker started; embeddings enabled=%v", ec != nil)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ec != nil {
			did, err := processOne(ctx, db, ec)
			if err != nil && ctx.Err() == nil {
				log.Printf("index worker error: %v", err)
			}
			if did && err == nil {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func processOne(ctx context.Context, db *pgxpool.Pool, ec *embedding.Client) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var jobID int64
	var chunkID, body, contentHash string
	err = tx.QueryRow(ctx, `SELECT j.id,j.chunk_id::text,c.body,c.content_hash FROM mimo.ingestion_jobs j JOIN mimo.search_chunks c ON c.id=j.chunk_id
 WHERE j.model_id=$1 AND ( (j.status IN('pending','failed') AND j.next_run_at<=now()) OR (j.status='running' AND j.updated_at<now()-interval '10 minutes') )
	ORDER BY j.next_run_at,j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, ec.Model).Scan(&jobID, &chunkID, &body, &contentHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE mimo.ingestion_jobs SET status='running',attempts=attempts+1,updated_at=now() WHERE id=$1`, jobID)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	vector, embedErr := ec.Embed(ctx, body)
	finished, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer finished.Rollback(ctx)
	var currentStatus, currentHash string
	err = finished.QueryRow(ctx, `SELECT j.status,c.content_hash FROM mimo.ingestion_jobs j JOIN mimo.search_chunks c ON c.id=j.chunk_id WHERE j.id=$1 FOR UPDATE OF j`, jobID).Scan(&currentStatus, &currentHash)
	if err != nil {
		return false, err
	}
	if currentStatus != "running" || currentHash != contentHash {
		return true, finished.Commit(ctx)
	} // changed by an edit; newer job remains queued
	if embedErr != nil {
		_, err = finished.Exec(ctx, `UPDATE mimo.ingestion_jobs SET status='failed',error_message=left($2,500),
 next_run_at=now()+least(interval '1 hour',interval '1 minute'*power(2,least(attempts,6))),updated_at=now() WHERE id=$1`, jobID, embedErr.Error())
		if err == nil {
			err = finished.Commit(ctx)
		}
		return true, err
	}
	_, err = finished.Exec(ctx, `INSERT INTO mimo.semantic_embeddings(chunk_id,model_id,dimensions,content_hash,embedding)
		VALUES($1::uuid,$2,$3,$4,($5::text)::vector)
		ON CONFLICT(chunk_id,model_id) DO UPDATE SET dimensions=EXCLUDED.dimensions,content_hash=EXCLUDED.content_hash,embedding=EXCLUDED.embedding,updated_at=now()`, chunkID, ec.Model, ec.Dimensions, contentHash, embedding.VectorText(vector))
	if err == nil {
		_, err = finished.Exec(ctx, `UPDATE mimo.ingestion_jobs SET status='done',error_message=NULL,updated_at=now() WHERE id=$1`, jobID)
	}
	if err == nil {
		err = finished.Commit(ctx)
	}
	return true, err
}
