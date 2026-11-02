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
	// 001_init.sql contains idempotent DDL. pgx simple protocol supports multi-statement SQL.
	bytes, err := os.ReadFile("db/migrations/001_init.sql")
	if err != nil {
		panic(err)
	}
	script := string(bytes)
	if !strings.Contains(script, "CREATE SCHEMA IF NOT EXISTS mimo") {
		panic(errors.New("unexpected migration contents"))
	}
	_, err = conn.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		panic(fmt.Errorf("migration: %w", err))
	}
	fmt.Println("Mimo schema migration applied successfully")
}
