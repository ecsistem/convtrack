// Package retention remove dados antigos periodicamente para evitar que as
// tabelas de alto volume cresçam indefinidamente.
package retention

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Runner struct {
	db       *pgxpool.Pool
	days     int
	interval time.Duration
}

// New cria o runner. RETENTION_DAYS controla a janela (default 90 dias).
// RETENTION_INTERVAL_HOURS controla a frequência (default 6h).
func New(db *pgxpool.Pool) *Runner {
	days := envInt("RETENTION_DAYS", 90)
	hours := envInt("RETENTION_INTERVAL_HOURS", 6)
	return &Runner{
		db:       db,
		days:     days,
		interval: time.Duration(hours) * time.Hour,
	}
}

// Start roda uma limpeza imediata e depois a cada `interval` até o ctx ser cancelado.
func (r *Runner) Start(ctx context.Context) {
	if r.days <= 0 {
		fmt.Println("retention: desabilitado (RETENTION_DAYS <= 0)")
		return
	}
	fmt.Printf("retention: ativo — janela=%dd intervalo=%s\n", r.days, r.interval)

	r.runOnce(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

// runOnce executa todas as limpezas, logando quantas linhas foram removidas.
func (r *Runner) runOnce(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -r.days)

	// Tabelas append-only: deleta tudo abaixo do cutoff.
	tables := []string{
		"events",
		"shield_visits",
		"shield_logs",
		"heatmap_clicks",
		"failed_jobs",
	}
	for _, tbl := range tables {
		r.delete(ctx, tbl,
			fmt.Sprintf("DELETE FROM %s WHERE created_at < $1", tbl), cutoff)
	}

	// Sessões: preserva as que geraram conversão (FK em conversions sem CASCADE).
	// As dependências (attributions, interactions, replays) caem por CASCADE.
	r.delete(ctx, "sessions", `
		DELETE FROM sessions
		WHERE created_at < $1
		  AND id NOT IN (SELECT session_id FROM conversions WHERE session_id IS NOT NULL)`,
		cutoff)

	// Tokens de auth expirados ou já usados (independe da janela de retenção).
	now := time.Now()
	r.delete(ctx, "password_reset_tokens",
		`DELETE FROM password_reset_tokens WHERE expires_at < $1 OR used_at IS NOT NULL`, now)
	r.delete(ctx, "email_verification_tokens",
		`DELETE FROM email_verification_tokens WHERE expires_at < $1 OR used_at IS NOT NULL`, now)
}

func (r *Runner) delete(ctx context.Context, label, query string, arg any) {
	tag, err := r.db.Exec(ctx, query, arg)
	if err != nil {
		fmt.Printf("retention: erro limpando %s: %v\n", label, err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		fmt.Printf("retention: %s — %d linhas removidas\n", label, n)
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
