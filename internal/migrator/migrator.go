// Package migrator executa migrations SQL de forma idempotente na inicialização.
// Rastreia versões aplicadas na tabela schema_migrations.
package migrator

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run aplica todas as migrations pendentes em ordem alfabética.
// Cada arquivo .sql é executado apenas uma vez; execuções seguintes são ignoradas.
func Run(ctx context.Context, db *pgxpool.Pool, fsys fs.FS) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Garante que a tabela de controle existe
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT        PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	if err != nil {
		return fmt.Errorf("migrator: criar tabela de controle: %w", err)
	}

	// Carrega versões já aplicadas
	rows, err := db.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("migrator: ler migrações aplicadas: %w", err)
	}
	applied := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil {
			applied[v] = true
		}
	}
	rows.Close()

	// Lista arquivos .sql
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("migrator: listar arquivos: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	// Aplica pendentes
	pending := 0
	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			continue
		}

		content, err := fs.ReadFile(fsys, f)
		if err != nil {
			return fmt.Errorf("migrator: ler %s: %w", f, err)
		}

		if _, err := db.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("migrator: aplicar %s: %w", f, err)
		}

		if _, err := db.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("migrator: registrar %s: %w", f, err)
		}

		fmt.Printf("[migrator] ✓ %s\n", f)
		pending++
	}

	if pending == 0 {
		fmt.Println("[migrator] schema atualizado, nenhuma migração pendente")
	} else {
		fmt.Printf("[migrator] %d migração(ões) aplicada(s)\n", pending)
	}
	return nil
}
