package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		panic("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		panic(err)
	}
	defer conn.Close(context.Background())
	entries, err := os.ReadDir("db/migrations")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		bytes, readErr := os.ReadFile("db/migrations/" + entry.Name())
		if readErr != nil {
			panic(readErr)
		}
		script := string(bytes)
		if entry.Name() == "001_init.sql" && !strings.Contains(script, "CREATE SCHEMA IF NOT EXISTS mimo") {
			panic(errors.New("unexpected baseline migration contents"))
		}
		if _, execErr := conn.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol); execErr != nil {
			panic(fmt.Errorf("migration %s: %w", entry.Name(), execErr))
		}
		fmt.Println("Applied", entry.Name())
	}
	var ambiguous int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM mimo.finance_accounts WHERE kind='other' AND classification='asset'`).Scan(&ambiguous); err == nil && ambiguous > 0 {
		fmt.Printf("WARNING: review %d existing 'other' finance account(s); migration classified them as asset by safe default.\n", ambiguous)
	}
}
