package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mimo/personal-os/internal/app"
	"github.com/mimo/personal-os/internal/config"
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
	pc.MaxConns = 4
	pc.MinConns = 0
	pc.MaxConnLifetime = 30 * time.Minute
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		log.Fatalf("database ping: %v", err)
	}
	a := app.New(db, c)
	srv := &http.Server{Addr: c.ListenAddr, Handler: a.Routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 65 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		sd, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = srv.Shutdown(sd)
	}()
	log.Printf("Mimo API listening on %s (owner %s)", c.ListenAddr, c.OwnerID)
	if err = srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
