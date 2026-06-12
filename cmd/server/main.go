package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ecsistem/convtrack/internal/api"
	"github.com/ecsistem/convtrack/internal/cache"
	"github.com/ecsistem/convtrack/internal/db"
	"github.com/ecsistem/convtrack/internal/migrator"
	"github.com/ecsistem/convtrack/internal/queue"
	"github.com/ecsistem/convtrack/internal/retention"
	convmigrations "github.com/ecsistem/convtrack/migrations"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	// Contexto amplo para inicialização (migrations podem demorar em cold start)
	initCtx, initCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer initCancel()

	pool, err := db.NewPool(initCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// ── Auto-migrate ────────────────────────────────────────────────────────
	// Aplica todas as migrations pendentes antes de iniciar o servidor.
	// Seguro para re-executar: cada migration é rastreada em schema_migrations.
	if err := migrator.Run(initCtx, pool, convmigrations.FS); err != nil {
		fmt.Fprintf(os.Stderr, "migrator: %v\n", err)
		os.Exit(1)
	}

	rdb := cache.New()
	if err := rdb.Ping(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "redis: %v\n", err)
		os.Exit(1)
	}

	app := api.NewApp(pool, rdb, rdb.Client())

	// Inicia o worker de fila com retry exponencial
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	w := queue.NewWorker(queue.New(rdb.Client()), pool, rdb.Client())
	go w.Start(workerCtx)
	fmt.Println("queue worker started")

	// Job de retenção: limpa dados antigos periodicamente.
	go retention.New(pool).Start(workerCtx)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		fmt.Printf("convtrack api listening on :%s\n", port)
		if err := app.Listen(":" + port); err != nil {
			fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		}
	}()

	<-quit
	fmt.Println("shutting down...")
	workerCancel()
	_ = app.ShutdownWithTimeout(5 * time.Second)
}
