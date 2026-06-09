package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/cache"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

type projectKey struct{}

func APIKey(db *pgxpool.Pool, rdb *cache.Cache) fiber.Handler {
	return func(c *fiber.Ctx) error {
		apiKey := c.Get("X-API-Key")
		if apiKey == "" {
			apiKey = c.Query("api_key")
		}
		if apiKey == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "missing api key"})
		}

		project, err := resolveProject(c.Context(), db, rdb, apiKey)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid api key"})
		}

		c.Locals("project", project)
		return c.Next()
	}
}

func resolveProject(ctx context.Context, db *pgxpool.Pool, rdb *cache.Cache, apiKey string) (*models.Project, error) {
	if data, err := rdb.GetProject(ctx, apiKey); err == nil {
		var p models.Project
		if json.Unmarshal(data, &p) == nil {
			return &p, nil
		}
	}

	var p models.Project
	err := db.QueryRow(ctx,
		`SELECT id, account_id, name, domain, api_key, clone_protection, created_at
		 FROM projects WHERE api_key = $1`, apiKey,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}

	_ = rdb.SetProject(ctx, apiKey, &p, 5*time.Minute)
	return &p, nil
}

func GetProject(c *fiber.Ctx) *models.Project {
	p, _ := c.Locals("project").(*models.Project)
	return p
}
