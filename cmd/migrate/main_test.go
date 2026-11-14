package main

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestStripTransactionEnvelope(t *testing.T) {
	input := "-- comment\nBEGIN;\nCREATE TABLE x(id int);\nCOMMIT;\n"
	if got := stripTransactionEnvelope(input); got != "CREATE TABLE x(id int);" {
		t.Fatalf("got %q", got)
	}
	plain := "CREATE TABLE x(id int);"
	if got := stripTransactionEnvelope(plain); got != plain {
		t.Fatalf("plain script changed: %q", got)
	}
}

func TestVerifyChecksums(t *testing.T) {
	items := []migration{{Name: "001.sql", Checksum: "new"}}
	if err := verifyChecksums(items, map[string]appliedMigration{"001.sql": {Checksum: "old"}}); err == nil {
		t.Fatal("expected checksum mismatch")
	}
	if err := verifyChecksums(items, map[string]appliedMigration{"001.sql": {Checksum: "new"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationIntegration(t *testing.T) {
	databaseURL := os.Getenv("MIMO_MIGRATION_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set MIMO_MIGRATION_TEST_DATABASE_URL to a disposable local pgvector database")
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		t.Fatalf("refusing non-local migration test database host %q", host)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	m := &migrator{conn: conn, dir: "../../db/migrations"}

	t.Run("first install and repeat", func(t *testing.T) {
		resetTestSchema(t, ctx, conn)
		if err := m.migrate(ctx, new(bytes.Buffer)); err != nil {
			t.Fatal(err)
		}
		assertMigrationCount(t, ctx, conn, 5)
		if err := m.migrate(ctx, new(bytes.Buffer)); err != nil {
			t.Fatal(err)
		}
		assertMigrationCount(t, ctx, conn, 5)
	})

	t.Run("legacy baseline", func(t *testing.T) {
		resetTestSchema(t, ctx, conn)
		for _, name := range []string{"001_init.sql", "002_finance_liabilities.sql"} {
			body, err := os.ReadFile(filepath.Join(m.dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = conn.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.baseline(ctx, new(bytes.Buffer)); err != nil {
			t.Fatal(err)
		}
		assertMigrationCount(t, ctx, conn, 2)
		if err := m.migrate(ctx, new(bytes.Buffer)); err != nil {
			t.Fatal(err)
		}
		assertMigrationCount(t, ctx, conn, 5)
	})

	t.Run("failure rolls back and concurrency is serialized", func(t *testing.T) {
		resetTestSchema(t, ctx, conn)
		badDir := t.TempDir()
		for _, name := range []string{"001_init.sql", "002_finance_liabilities.sql", "003_embedding.sql"} {
			copyFile(t, filepath.Join(m.dir, name), filepath.Join(badDir, name))
		}
		if err := m.migrate(ctx, new(bytes.Buffer)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(badDir, "004_failure.sql"), []byte("BEGIN;\nCREATE TABLE mimo.migration_failure_probe(id int);\nSELECT * FROM mimo.no_such_table;\nCOMMIT;\n"), 0600); err != nil {
			t.Fatal(err)
		}
		failed := &migrator{conn: conn, dir: badDir}
		if err := failed.migrate(ctx, new(bytes.Buffer)); err == nil {
			t.Fatal("expected migration failure")
		}
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT to_regclass('mimo.migration_failure_probe') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("failed migration was not rolled back")
		}

		resetTestSchema(t, ctx, conn)
		conn2, err := pgx.Connect(ctx, databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn2.Close(context.Background())
		m2 := &migrator{conn: conn2, dir: "../../db/migrations"}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs <- m.migrate(ctx, new(bytes.Buffer)) }()
		go func() { defer wg.Done(); errs <- m2.migrate(ctx, new(bytes.Buffer)) }()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		assertMigrationCount(t, ctx, conn, 5)
	})
}

func resetTestSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `DROP SCHEMA IF EXISTS mimo CASCADE`); err != nil {
		t.Fatal(err)
	}
}
func assertMigrationCount(t *testing.T, ctx context.Context, conn *pgx.Conn, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM mimo.schema_migrations`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("migration count=%d want=%d", got, want)
	}
}
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dst, body, 0600); err != nil {
		t.Fatal(fmt.Errorf("copy %s: %w", src, err))
	}
}
