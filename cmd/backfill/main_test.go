package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBackfillIntegration(t *testing.T) {
	databaseURL := os.Getenv("MIMO_BACKFILL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set MIMO_BACKFILL_TEST_DATABASE_URL to a disposable local pgvector database")
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		t.Fatalf("refusing non-local database host %q", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	resetBackfillSchema(t, ctx, conn)
	for _, name := range []string{"001_init.sql", "002_finance_liabilities.sql", "003_embedding.sql"} {
		body, err := os.ReadFile(filepath.Join("../../db/migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.Exec(ctx, `INSERT INTO mimo.memory_entries(scope_type,scope_id,category,title,summary,source_kind) VALUES ('personal','backfill-owner','fact','SSH VPS','Old host note','manual')`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO mimo.notes(owner_id,title,content) VALUES ('backfill-owner','Deployment note','Use the existing worker')`); err != nil {
		t.Fatal(err)
	}

	result, err := run(ctx, singlePool(t, ctx, databaseURL), "backfill-owner", "embeddinggemma-2:440m", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobsConsidered != 2 {
		t.Fatalf("fresh jobs=%d", result.JobsConsidered)
	}
	// A second run has no vector yet, so jobs remain pending but are not reset.
	result, err = run(ctx, singlePool(t, ctx, databaseURL), "backfill-owner", "embeddinggemma-2:440m", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobsConsidered != 2 {
		t.Fatalf("dry-run candidates=%d", result.JobsConsidered)
	}
	if _, err = conn.Exec(ctx, `INSERT INTO mimo.semantic_embeddings(chunk_id,model_id,dimensions,content_hash,embedding) SELECT id,'embeddinggemma-2:440m',768,content_hash,('['||repeat('0,',767)||'0]')::vector FROM mimo.search_chunks WHERE owner_id='backfill-owner' AND source_type IN ('memory','note')`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `UPDATE mimo.ingestion_jobs SET status='done' WHERE model_id='embeddinggemma-2:440m'`); err != nil {
		t.Fatal(err)
	}
	result, err = run(ctx, singlePool(t, ctx, databaseURL), "backfill-owner", "embeddinggemma-2:440m", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobsConsidered != 0 {
		t.Fatalf("unchanged candidates=%d", result.JobsConsidered)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM mimo.semantic_embeddings WHERE chunk_id=(SELECT source_id FROM mimo.search_chunks WHERE owner_id='backfill-owner' AND source_type='memory')`); err != nil {
		t.Fatal(err)
	}
	result, err = run(ctx, singlePool(t, ctx, databaseURL), "backfill-owner", "embeddinggemma-2:440m", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobsConsidered != 1 {
		t.Fatalf("missing-vector candidates=%d", result.JobsConsidered)
	}
}

func singlePool(t *testing.T, ctx context.Context, url string) *pgxpool.Pool {
	t.Helper()
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pc.MaxConns = 1
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}
func resetBackfillSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS mimo CASCADE`); err != nil {
		t.Fatal(err)
	}
}
