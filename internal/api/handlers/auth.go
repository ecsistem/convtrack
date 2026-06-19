package handlers

import (
	"errors"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/ecsistem/convtrack/internal/plans"
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuthHandler struct {
	svc *auth.Service
	db  *pgxpool.Pool
}

func NewAuth(svc *auth.Service, db *pgxpool.Pool) *AuthHandler {
	return &AuthHandler{svc: svc, db: db}
}

// POST /v1/auth/register
func (h *AuthHandler) Register(c *fiber.Ctx) error {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Name == "" || body.Email == "" || len(body.Password) < 6 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name, email e senha (mín 6 chars) obrigatórios"})
	}
	if !validEmail(body.Email) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "email inválido"})
	}

	result, err := h.svc.Register(c.Context(), body.Name, body.Email, body.Password)
	if errors.Is(err, auth.ErrEmailTaken) {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "email já cadastrado"})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Pending accounts are created but cannot log in until approved.
	if result.Account.Status == "pending" {
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"pending": true,
			"message": "Conta criada! Aguarde a aprovação do administrador para acessar o sistema.",
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"account":       result.Account,
		"project":       result.Project,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
	})
}

// POST /v1/auth/login
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	result, err := h.svc.Login(c.Context(), body.Email, body.Password)
	if errors.Is(err, auth.ErrInvalidCreds) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "email ou senha incorretos"})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Carrega projetos para retornar junto ao login
	projects, _ := h.svc.ListProjects(c.Context(), result.Account.ID)

	return c.JSON(fiber.Map{
		"account":       result.Account,
		"projects":      projects,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
	})
}

// POST /v1/auth/refresh
func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.BodyParser(&body); err != nil || body.RefreshToken == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "refresh_token required"})
	}

	access, refresh, err := h.svc.Refresh(c.Context(), body.RefreshToken)
	if errors.Is(err, auth.ErrInvalidToken) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "token inválido ou expirado"})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"access_token": access, "refresh_token": refresh})
}

// POST /v1/auth/logout
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.BodyParser(&body); err == nil && body.RefreshToken != "" {
		_ = h.svc.Logout(c.Context(), body.RefreshToken)
	}
	return c.JSON(fiber.Map{"ok": true})
}

// GET /v1/auth/me
func (h *AuthHandler) Me(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	account, err := h.svc.GetAccount(c.Context(), accountID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "account not found"})
	}

	projects, err := h.svc.ListProjects(c.Context(), accountID)
	if err != nil {
		projects = nil
	}

	return c.JSON(fiber.Map{
		"account":  account,
		"projects": projects,
	})
}

// GET /v1/me/usage
func (h *AuthHandler) Usage(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var accountPlan string
	_ = h.db.QueryRow(c.Context(), `SELECT plan FROM accounts WHERE id = $1`, accountID).Scan(&accountPlan)
	if accountPlan == "" {
		accountPlan = "free"
	}
	lim := plans.Get(accountPlan)

	// Count campaigns across all projects of this account
	var campaigns int
	_ = h.db.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM shield_campaigns sc
		JOIN projects p ON p.id = sc.project_id
		WHERE p.account_id = $1`, accountID,
	).Scan(&campaigns)

	// Count domains across all projects of this account
	var domains int
	_ = h.db.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM shield_domains sd
		JOIN shield_campaigns sc ON sc.id = sd.campaign_id
		JOIN projects p ON p.id = sc.project_id
		WHERE p.account_id = $1`, accountID,
	).Scan(&domains)

	// Count sessions this calendar month
	monthStart := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -time.Now().UTC().Day()+1)
	var sessions int
	_ = h.db.QueryRow(c.Context(), `
		SELECT COUNT(*) FROM sessions s
		JOIN projects p ON p.id = s.project_id
		WHERE p.account_id = $1 AND s.created_at >= $2`, accountID, monthStart,
	).Scan(&sessions)

	return c.JSON(fiber.Map{
		"plan": accountPlan,
		"limits": fiber.Map{
			"max_campaigns": lim.MaxCampaigns,
			"max_domains":   lim.MaxDomains,
			"max_sessions":  lim.MaxSessions,
			"clone_enabled": lim.CloneEnabled,
			"video_enabled": lim.VideoEnabled,
		},
		"usage": fiber.Map{
			"campaigns": campaigns,
			"domains":   domains,
			"sessions":  sessions,
		},
	})
}
