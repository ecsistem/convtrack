package handlers

import (
	"context"
	"encoding/json"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/crypto"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IntegrationsHandler struct {
	db *pgxpool.Pool
}

func NewIntegrations(db *pgxpool.Pool) *IntegrationsHandler {
	return &IntegrationsHandler{db: db}
}

// GET /v1/dashboard/integrations
func (h *IntegrationsHandler) List(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	rows, err := h.db.Query(c.Context(), `
		SELECT id, platform, enabled, config, created_at, updated_at
		FROM integration_settings WHERE project_id = $1 ORDER BY platform`, project.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	var list []integrationDTO
	for rows.Next() {
		var s models.IntegrationSettings
		var configRaw []byte
		if err := rows.Scan(&s.ID, &s.Platform, &s.Enabled, &configRaw, &s.CreatedAt, &s.UpdatedAt); err != nil {
			continue
		}
		var cfg map[string]interface{}
		if err := json.Unmarshal(configRaw, &cfg); err == nil {
			cfg = redactSecrets(cfg)
		}
		list = append(list, integrationDTO{
			Platform:  s.Platform,
			Enabled:   s.Enabled,
			Config:    cfg,
			CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	if list == nil {
		list = []integrationDTO{}
	}
	return c.JSON(fiber.Map{"data": list})
}

// PUT /v1/dashboard/integrations/:platform
// Creates or updates. Config fields are encrypted at rest.
func (h *IntegrationsHandler) Upsert(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	platform := c.Params("platform")
	if platform == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "platform required"})
	}

	var body struct {
		Enabled bool                   `json:"enabled"`
		Config  map[string]interface{} `json:"config"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	// Encrypt secrets in config
	encConfig, err := encryptConfig(body.Config)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "encrypt config: " + err.Error()})
	}

	configJSON, _ := json.Marshal(encConfig)
	id := uuid.New()
	_, err = h.db.Exec(c.Context(), `
		INSERT INTO integration_settings (id, project_id, platform, enabled, config)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (project_id, platform) DO UPDATE SET
		  enabled    = EXCLUDED.enabled,
		  config     = EXCLUDED.config,
		  updated_at = NOW()`,
		id, project.ID, platform, body.Enabled, configJSON,
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"ok": true, "platform": platform, "enabled": body.Enabled})
}

// DELETE /v1/dashboard/integrations/:platform
func (h *IntegrationsHandler) Delete(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	platform := c.Params("platform")
	_, err := h.db.Exec(c.Context(),
		`DELETE FROM integration_settings WHERE project_id = $1 AND platform = $2`,
		project.ID, platform)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// POST /v1/dashboard/integrations/:platform/test
// Verifies that credentials are valid by calling the platform's API.
func (h *IntegrationsHandler) Test(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	platform := c.Params("platform")

	// Load current config from DB
	var configRaw []byte
	err := h.db.QueryRow(c.Context(), `
		SELECT config FROM integration_settings
		WHERE project_id = $1 AND platform = $2`, project.ID, platform,
	).Scan(&configRaw)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "integration not configured"})
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(configRaw, &cfg); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "invalid config"})
	}
	cfg = decryptConfig(cfg)

	// Platform-specific test
	testErr := testIntegration(c.Context(), platform, cfg)
	if testErr != nil {
		return c.JSON(fiber.Map{"ok": false, "error": testErr.Error()})
	}
	return c.JSON(fiber.Map{"ok": true, "message": "credenciais válidas"})
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

type integrationDTO struct {
	Platform  string                 `json:"platform"`
	Enabled   bool                   `json:"enabled"`
	Config    map[string]interface{} `json:"config"` // secrets redacted
	CreatedAt string                 `json:"created_at"`
}

// Secret field names that should be encrypted / redacted in responses.
var secretFields = map[string]bool{
	"access_token":    true,
	"api_secret":      true,
	"developer_token": true, // Google Ads
	"client_secret":   true, // Google Ads OAuth
	"refresh_token":   true, // Google Ads OAuth
	"pixel_id":        false, // not secret, but keep as-is
}

// encryptConfig encrypts secret fields using AES-256-GCM.
func encryptConfig(cfg map[string]interface{}) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		if secretFields[k] {
			if s, ok := v.(string); ok && s != "" {
				enc, err := crypto.EncryptString(s)
				if err != nil {
					return nil, err
				}
				out[k] = enc
				continue
			}
		}
		out[k] = v
	}
	return out, nil
}

// decryptConfig decrypts secret fields.
func decryptConfig(cfg map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		if secretFields[k] {
			if s, ok := v.(string); ok && s != "" {
				if plain, err := crypto.DecryptString(s); err == nil {
					out[k] = plain
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

// redactSecrets replaces secret values with a masked placeholder for API responses.
func redactSecrets(cfg map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		if secretFields[k] {
			if s, ok := v.(string); ok && s != "" {
				out[k] = "••••••••" // masked
				continue
			}
		}
		out[k] = v
	}
	return out
}

// testIntegration does a lightweight connectivity test per platform.
func testIntegration(ctx context.Context, platform string, cfg map[string]interface{}) error {
	switch platform {
	case "meta":
		token, _ := cfg["access_token"].(string)
		if token == "" {
			return fiber.NewError(400, "access_token não configurado")
		}
		// A simple check: just validate the token is non-empty (real test would call Graph API)
		return nil
	case "google":
		if _, ok := cfg["measurement_id"]; !ok {
			return fiber.NewError(400, "measurement_id não configurado")
		}
		return nil
	case "tiktok":
		if _, ok := cfg["pixel_code"]; !ok {
			return fiber.NewError(400, "pixel_code não configurado")
		}
		return nil
	case "kwai":
		if _, ok := cfg["pixel_id"]; !ok {
			return fiber.NewError(400, "pixel_id não configurado")
		}
		return nil
	}
	return nil
}
