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
	"github.com/ecsistem/convtrack/internal/queue"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	rdb := cache.New()
	if err := rdb.Ping(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "redis: %v\n", err)
		os.Exit(1)
	}

	app := api.NewApp(pool, rdb, rdb.Client())

	// Inicia o worker de fila com retry exponencial
	// Contexto com cancel para shutdown gracioso
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	w := queue.NewWorker(queue.New(rdb.Client()), pool, rdb.Client())
	go w.Start(workerCtx)
	fmt.Println("queue worker started")

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
	workerCancel() // para o worker primeiro
	_ = app.ShutdownWithTimeout(5 * time.Second)
}
