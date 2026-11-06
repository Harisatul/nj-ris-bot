package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultMigrationDir = "db/migrations"
	migrationLockKey    = int64(701923840)
)

type migration struct{ Name, SQL, Checksum string }
type appliedMigration struct {
	Checksum string
	Applied  time.Time
	Baseline bool
}
type migrator struct {
	conn *pgx.Conn
	dir  string
}

func main() {
	command := "migrate"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command != "migrate" && command != "baseline" && command != "status" {
		fmt.Fprintln(os.Stderr, "usage: mimo-migrate [migrate|baseline|status]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	dir := fs.String("dir", defaultMigrationDir, "migration directory")
	_ = fs.Parse(os.Args[2:])
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fatal(errors.New("DATABASE_URL is required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		fatal(err)
	}
	defer conn.Close(context.Background())
	m := &migrator{conn: conn, dir: *dir}
	switch command {
	case "status":
		err = m.status(ctx, os.Stdout)
	case "baseline":
		err = m.baseline(ctx, os.Stdout)
	default:
		err = m.migrate(ctx, os.Stdout)
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) { fmt.Fprintf(os.Stderr, "migration failed: %v\n", err); os.Exit(1) }

func loadMigrations(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	result := make([]migration, 0, len(files))
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		result = append(result, migration{Name: name, SQL: string(body), Checksum: hex.EncodeToString(sum[:])})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no .sql migrations found in %s", dir)
	}
	return result, nil
}

func (m *migrator) lock(ctx context.Context) error {
	_, err := m.conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey)
	return err
}
func (m *migrator) unlock(ctx context.Context) {
	_, _ = m.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockKey)
}

func (m *migrator) ensureMetadata(ctx context.Context) error {
	_, err := m.conn.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS mimo;
CREATE TABLE IF NOT EXISTS mimo.schema_migrations (
 filename text PRIMARY KEY,
 checksum text NOT NULL CHECK (checksum ~ '^[a-f0-9]{64}$'),
 applied_at timestamptz NOT NULL DEFAULT now(),
 baseline boolean NOT NULL DEFAULT false
)`, pgx.QueryExecModeSimpleProtocol)
	return err
}

func (m *migrator) applied(ctx context.Context) (map[string]appliedMigration, error) {
	rows, err := m.conn.Query(ctx, `SELECT filename,checksum,applied_at,baseline FROM mimo.schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]appliedMigration{}
	for rows.Next() {
		var name, checksum string
		var appliedAt time.Time
		var baseline bool
		if err := rows.Scan(&name, &checksum, &appliedAt, &baseline); err != nil {
			return nil, err
		}
		result[name] = appliedMigration{Checksum: checksum, Applied: appliedAt, Baseline: baseline}
	}
	return result, rows.Err()
}

func verifyChecksums(migrations []migration, applied map[string]appliedMigration) error {
	known := make(map[string]struct{}, len(migrations))
	for _, item := range migrations {
		known[item.Name] = struct{}{}
		if old, ok := applied[item.Name]; ok && old.Checksum != item.Checksum {
			return fmt.Errorf("checksum mismatch for %s: database=%s file=%s; restore the original migration or create a new migration", item.Name, old.Checksum, item.Checksum)
		}
	}
	for name := range applied {
		if _, ok := known[name]; !ok {
			return fmt.Errorf("database records unknown migration %s; restore the migration file before continuing", name)
		}
	}
	return nil
}

func (m *migrator) migrate(ctx context.Context, out io.Writer) error {
	if err := m.lock(ctx); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer m.unlock(context.Background())
	if err := m.ensureMetadata(ctx); err != nil {
		return fmt.Errorf("ensure migration metadata: %w", err)
	}
	migrations, err := loadMigrations(m.dir)
	if err != nil {
		return err
	}
	applied, err := m.applied(ctx)
	if err != nil {
		return fmt.Errorf("read migration history: %w", err)
	}
	if err := verifyChecksums(migrations, applied); err != nil {
		return err
	}
	if err := m.baselineHistorical(ctx, migrations, applied, out, false); err != nil {
		return err
	}
	applied, err = m.applied(ctx)
	if err != nil {
		return fmt.Errorf("refresh migration history: %w", err)
	}
	for _, item := range migrations {
		if _, ok := applied[item.Name]; ok {
			fmt.Fprintf(out, "Skipped %s\n", item.Name)
			continue
		}
		if err := m.applyOne(ctx, item); err != nil {
			return err
		}
		fmt.Fprintf(out, "Applied %s\n", item.Name)
	}
	return nil
}

func (m *migrator) applyOne(ctx context.Context, item migration) error {
	tx, err := m.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", item.Name, err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, stripTransactionEnvelope(item.SQL), pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("execute %s: %w", item.Name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mimo.schema_migrations(filename,checksum,baseline) VALUES($1,$2,false)`, item.Name, item.Checksum); err != nil {
		return fmt.Errorf("record %s: %w", item.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", item.Name, err)
	}
	return nil
}

func stripTransactionEnvelope(script string) string {
	lines := strings.Split(script, "\n")
	start := 0
	for start < len(lines) {
		line := strings.TrimSpace(lines[start])
		if line == "" || strings.HasPrefix(line, "--") {
			start++
			continue
		}
		if strings.EqualFold(line, "BEGIN;") || strings.EqualFold(line, "BEGIN") {
			start++
		}
		break
	}
	end := len(lines)
	for end > start {
		line := strings.TrimSpace(lines[end-1])
		if line == "" || strings.HasPrefix(line, "--") {
			end--
			continue
		}
		if strings.EqualFold(line, "COMMIT;") || strings.EqualFold(line, "COMMIT") {
			end--
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func (m *migrator) status(ctx context.Context, out io.Writer) error {
	if err := m.lock(ctx); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer m.unlock(context.Background())
	if err := m.ensureMetadata(ctx); err != nil {
		return fmt.Errorf("ensure migration metadata: %w", err)
	}
	migrations, err := loadMigrations(m.dir)
	if err != nil {
		return err
	}
	applied, err := m.applied(ctx)
	if err != nil {
		return err
	}
	if err := verifyChecksums(migrations, applied); err != nil {
		return err
	}
	for _, item := range migrations {
		if record, ok := applied[item.Name]; ok {
			kind := "applied"
			if record.Baseline {
				kind = "baselined"
			}
			fmt.Fprintf(out, "%s %s %s\n", kind, item.Name, record.Applied.UTC().Format(time.RFC3339))
		} else {
			fmt.Fprintf(out, "pending %s\n", item.Name)
		}
	}
	return nil
}

func (m *migrator) baseline(ctx context.Context, out io.Writer) error {
	if err := m.lock(ctx); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer m.unlock(context.Background())
	if err := m.ensureMetadata(ctx); err != nil {
		return fmt.Errorf("ensure migration metadata: %w", err)
	}
	migrations, err := loadMigrations(m.dir)
	if err != nil {
		return err
	}
	applied, err := m.applied(ctx)
	if err != nil {
		return err
	}
	if err := verifyChecksums(migrations, applied); err != nil {
		return err
	}
	return m.baselineHistorical(ctx, migrations, applied, out, true)
}

func (m *migrator) baselineHistorical(ctx context.Context, migrations []migration, applied map[string]appliedMigration, out io.Writer, strict bool) error {
	byName := make(map[string]migration, len(migrations))
	for _, item := range migrations {
		byName[item.Name] = item
	}
	toBaseline := make([]migration, 0, 2)
	for _, name := range []string{"001_init.sql", "002_finance_liabilities.sql"} {
		item, ok := byName[name]
		if !ok {
			return fmt.Errorf("cannot baseline missing historical migration %s", name)
		}
		if _, ok := applied[name]; ok {
			continue
		}
		var valid bool
		var err error
		if name == "001_init.sql" {
			valid, err = m.validateLegacy001(ctx)
		} else {
			valid, err = m.validateLegacy002(ctx)
		}
		if err != nil {
			return fmt.Errorf("validate legacy state for %s: %w", name, err)
		}
		if !valid {
			if strict {
				return fmt.Errorf("legacy validation failed for %s; refusing to baseline", name)
			}
			continue
		}
		toBaseline = append(toBaseline, item)
	}
	for _, item := range toBaseline {
		if err := m.recordBaseline(ctx, item); err != nil {
			return err
		}
		applied[item.Name] = appliedMigration{Checksum: item.Checksum, Applied: time.Now(), Baseline: true}
		fmt.Fprintf(out, "Baselined %s after validating legacy objects\n", item.Name)
	}
	return nil
}

func (m *migrator) recordBaseline(ctx context.Context, item migration) error {
	_, err := m.conn.Exec(ctx, `INSERT INTO mimo.schema_migrations(filename,checksum,baseline) VALUES($1,$2,true)`, item.Name, item.Checksum)
	if err != nil {
		return fmt.Errorf("record baseline %s: %w", item.Name, err)
	}
	return nil
}

func (m *migrator) validateLegacy001(ctx context.Context) (bool, error) {
	tables := []string{"memory_entries", "memory_embeddings", "assets", "memory_assets", "scope_members", "audit_events", "notes", "note_revisions", "objects", "object_assets", "finance_accounts", "finance_categories", "finance_transactions", "finance_transaction_revisions", "search_chunks", "search_embeddings", "ingestion_jobs"}
	for _, table := range tables {
		ok, err := m.hasTable(ctx, table)
		if err != nil || !ok {
			return false, err
		}
	}
	columns := map[string][]string{
		"memory_entries": {"id", "scope_type", "scope_id", "title", "summary", "status"}, "notes": {"id", "owner_id", "title", "content", "deleted_at"},
		"finance_accounts": {"id", "owner_id", "name", "kind", "opening_balance_idr"}, "finance_transactions": {"id", "owner_id", "type", "amount_idr", "status"},
		"search_chunks": {"id", "owner_id", "source_type", "source_id", "body"}, "ingestion_jobs": {"id", "chunk_id", "model_id", "status", "attempts"},
	}
	for table, names := range columns {
		for _, column := range names {
			ok, err := m.hasColumn(ctx, table, column)
			if err != nil || !ok {
				return false, err
			}
		}
	}
	checks := []struct{ table, fragment string }{{"memory_entries", "scope_type"}, {"finance_transactions", "amount_idr > 0"}, {"finance_transactions", "type = 'expense'"}, {"search_chunks", "source_type"}}
	for _, check := range checks {
		ok, err := m.hasConstraintFragment(ctx, check.table, check.fragment)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (m *migrator) validateLegacy002(ctx context.Context) (bool, error) {
	for _, table := range []string{"finance_debts", "finance_installments", "finance_installment_payments"} {
		ok, err := m.hasTable(ctx, table)
		if err != nil || !ok {
			return false, err
		}
	}
	columns := map[string][]string{
		"finance_accounts": {"classification", "credit_limit_idr"}, "finance_transactions": {"debt_id", "parent_transaction_id"},
		"finance_debts":        {"liability_account_id", "purchase_transaction_id", "principal_idr", "total_fee_idr", "tenor", "first_due_date", "status"},
		"finance_installments": {"debt_id", "installment_number", "due_date", "principal_due_idr", "fee_due_idr", "principal_paid_idr", "fee_paid_idr", "status"},
	}
	for table, names := range columns {
		for _, column := range names {
			ok, err := m.hasColumn(ctx, table, column)
			if err != nil || !ok {
				return false, err
			}
		}
	}
	for _, check := range []struct{ table, fragment string }{{"finance_accounts", "liability"}, {"finance_transactions", "liability_purchase"}, {"finance_installments", "overdue"}} {
		ok, err := m.hasConstraintFragment(ctx, check.table, check.fragment)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (m *migrator) hasTable(ctx context.Context, table string) (bool, error) {
	var exists bool
	err := m.conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "mimo."+table).Scan(&exists)
	return exists, err
}
func (m *migrator) hasColumn(ctx context.Context, table, column string) (bool, error) {
	var exists bool
	err := m.conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='mimo' AND table_name=$1 AND column_name=$2)`, table, column).Scan(&exists)
	return exists, err
}
func (m *migrator) hasConstraintFragment(ctx context.Context, table, fragment string) (bool, error) {
	var exists bool
	err := m.conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='mimo' AND t.relname=$1 AND lower(pg_get_constraintdef(c.oid)) LIKE '%' || lower($2) || '%')`, table, fragment).Scan(&exists)
	return exists, err
}
