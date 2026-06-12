// Package heatmap agrega cliques capturados pelo tracker.js para gerar
// visualizações de heatmap por URL.
package heatmap

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// Click é um clique individual enviado pelo tracker.
type Click struct {
	X        int     `json:"x"`
	Y        int     `json:"y"`
	XP       float64 `json:"xp"`
	YP       float64 `json:"yp"`
	VW       int     `json:"vw"`
	VH       int     `json:"vh"`
	DW       int     `json:"dw"`
	DH       int     `json:"dh"`
	Selector string  `json:"sel"`
	Tag      string  `json:"tag"`
	Text     string  `json:"text"`
	URL      string  `json:"url"`
}

// InsertClicks persiste um lote de cliques de uma sessão.
func (s *Service) InsertClicks(ctx context.Context, projectID, sessionID uuid.UUID, clicks []Click) error {
	if len(clicks) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(clicks))
	for _, c := range clicks {
		url := c.URL
		if url == "" {
			url = "/"
		}
		// trunca texto por segurança
		txt := c.Text
		if len(txt) > 60 {
			txt = txt[:60]
		}
		rows = append(rows, []any{
			projectID, sessionID, url,
			c.X, c.Y, clamp01(c.XP), clamp01(c.YP),
			c.VW, c.VH, c.DW, c.DH,
			c.Selector, c.Tag, txt,
		})
	}
	_, err := s.db.CopyFrom(ctx,
		pgx.Identifier{"heatmap_clicks"},
		[]string{"project_id", "session_id", "url_path", "x", "y", "xp", "yp", "vw", "vh", "dw", "dh", "selector", "tag", "text"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("heatmap: insert clicks: %w", err)
	}
	return nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Point é um ponto agregado do heatmap (posição relativa + intensidade).
type Point struct {
	XP    float64 `json:"xp"`
	YP    float64 `json:"yp"`
	Count int     `json:"count"`
}

// ElementStat agrega cliques por seletor (top elementos clicados).
type ElementStat struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Count    int    `json:"count"`
}

// URLStat lista URLs com cliques (para o seletor de página).
type URLStat struct {
	URL   string `json:"url"`
	Count int    `json:"count"`
}

// Aggregate retorna os pontos, top elementos e total de cliques de uma URL.
func (s *Service) Aggregate(ctx context.Context, projectID uuid.UUID, urlPath string, since time.Time) ([]Point, []ElementStat, int, error) {
	// Pontos: arredonda posição relativa a uma grade de ~0.5% para reduzir cardinalidade.
	pointRows, err := s.db.Query(ctx, `
		SELECT ROUND(xp::numeric, 3)::float8 AS gx, ROUND(yp::numeric, 3)::float8 AS gy, COUNT(*)
		FROM heatmap_clicks
		WHERE project_id = $1 AND url_path = $2 AND created_at >= $3
		GROUP BY gx, gy
		ORDER BY COUNT(*) DESC
		LIMIT 5000`, projectID, urlPath, since)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("heatmap: aggregate points: %w", err)
	}
	defer pointRows.Close()

	points := make([]Point, 0)
	total := 0
	for pointRows.Next() {
		var p Point
		if err := pointRows.Scan(&p.XP, &p.YP, &p.Count); err != nil {
			return nil, nil, 0, err
		}
		total += p.Count
		points = append(points, p)
	}

	// Top elementos por seletor.
	elemRows, err := s.db.Query(ctx, `
		SELECT selector, MAX(tag), MAX(text), COUNT(*)
		FROM heatmap_clicks
		WHERE project_id = $1 AND url_path = $2 AND created_at >= $3 AND selector <> ''
		GROUP BY selector
		ORDER BY COUNT(*) DESC
		LIMIT 25`, projectID, urlPath, since)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("heatmap: aggregate elements: %w", err)
	}
	defer elemRows.Close()

	elements := make([]ElementStat, 0)
	for elemRows.Next() {
		var e ElementStat
		if err := elemRows.Scan(&e.Selector, &e.Tag, &e.Text, &e.Count); err != nil {
			return nil, nil, 0, err
		}
		elements = append(elements, e)
	}

	return points, elements, total, nil
}

// ListURLs retorna as URLs com mais cliques no período.
func (s *Service) ListURLs(ctx context.Context, projectID uuid.UUID, since time.Time) ([]URLStat, error) {
	rows, err := s.db.Query(ctx, `
		SELECT url_path, COUNT(*)
		FROM heatmap_clicks
		WHERE project_id = $1 AND created_at >= $2
		GROUP BY url_path
		ORDER BY COUNT(*) DESC
		LIMIT 100`, projectID, since)
	if err != nil {
		return nil, fmt.Errorf("heatmap: list urls: %w", err)
	}
	defer rows.Close()

	urls := make([]URLStat, 0)
	for rows.Next() {
		var u URLStat
		if err := rows.Scan(&u.URL, &u.Count); err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	return urls, nil
}
