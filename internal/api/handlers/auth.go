package handlers

import (
	"errors"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/gofiber/fiber/v2"
)

type AuthHandler struct {
	svc *auth.Service
}

func NewAuth(svc *auth.Service) *AuthHandler {
	return &AuthHandler{svc: svc}
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

	projects, err := h.svc.ListProjects(c.Context(), accountID)
	if err != nil {
		projects = nil
	}

	return c.JSON(fiber.Map{
		"account_id": accountID,
		"email":      c.Locals("account_email"),
		"name":       c.Locals("account_name"),
		"projects":   projects,
	})
}
