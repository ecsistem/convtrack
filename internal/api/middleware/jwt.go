package middleware

import (
	"strings"

	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type accountKey struct{}

// JWTAuth validates the Bearer JWT and, if an X-Project-Id header is present,
// loads and validates the project belongs to the authenticated account.
// It sets c.Locals("project") so GetProject() works transparently in handlers.
func JWTAuth(authSvc *auth.Service, db *pgxpool.Pool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Suporte a Bearer header E query param ?token= (necessário para EventSource/SSE)
		tokenStr := ""
		authHeader := c.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenStr = authHeader[7:]
		} else if t := c.Query("token"); t != "" {
			tokenStr = t
		}
		if tokenStr == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "missing bearer token"})
		}

		claims, err := authSvc.ValidateAccessToken(tokenStr)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid or expired token"})
		}

		// Check account status directly in DB so suspensions take effect immediately.
		var status string
		_ = db.QueryRow(c.Context(),
			`SELECT status FROM accounts WHERE id = $1`, claims.AccountID,
		).Scan(&status)
		if status == "suspended" {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "conta suspensa — entre em contato com o suporte"})
		}
		if status == "pending" {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "conta aguardando aprovação"})
		}

		c.Locals("account_id", claims.AccountID)
		c.Locals("account_email", claims.Email)
		c.Locals("account_name", claims.Name)

		// Load project from X-Project-Id header if provided
		projectIDStr := c.Get("X-Project-Id")
		if projectIDStr == "" {
			projectIDStr = c.Query("project_id")
		}
		if projectIDStr != "" {
			projectID, parseErr := uuid.Parse(projectIDStr)
			if parseErr == nil {
				project, loadErr := authSvc.LoadProjectForAccount(c.Context(), projectID, claims.AccountID)
				if loadErr == nil {
					c.Locals("project", project)
				}
			}
		}

		return c.Next()
	}
}

// GetAccount returns the authenticated account ID from the request context.
func GetAccountID(c *fiber.Ctx) (uuid.UUID, bool) {
	id, ok := c.Locals("account_id").(uuid.UUID)
	return id, ok
}

// GetAccountEmail returns the authenticated account's email from JWT claims.
func GetAccountEmail(c *fiber.Ctx) string {
	email, _ := c.Locals("account_email").(string)
	return email
}

// RequireProject is a middleware that ensures a project is loaded (X-Project-Id was valid).
// Use this on routes that need project context.
func RequireProject(c *fiber.Ctx) error {
	p, _ := c.Locals("project").(*models.Project)
	if p == nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "X-Project-Id header required"})
	}
	return c.Next()
}
