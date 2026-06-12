package handlers

import (
	"net/mail"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/gofiber/fiber/v2"
)

// validEmail valida o formato do endereço de email (RFC 5322 básico).
func validEmail(email string) bool {
	if len(email) > 254 {
		return false
	}
	addr, err := mail.ParseAddress(email)
	return err == nil && addr.Address == email
}

// POST /v1/auth/forgot-password
// Sempre responde 200 (anti-enumeration). Envia email se a conta existir.
func (h *AuthHandler) ForgotPassword(c *fiber.Ctx) error {
	var body struct {
		Email string `json:"email"`
	}
	if err := c.BodyParser(&body); err != nil || body.Email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "email obrigatório"})
	}
	if err := h.svc.RequestPasswordReset(c.Context(), body.Email); err != nil {
		// loga internamente mas não revela ao cliente
		return c.JSON(fiber.Map{"ok": true})
	}
	return c.JSON(fiber.Map{"ok": true, "message": "Se o email existir, enviamos um link de redefinição."})
}

// POST /v1/auth/reset-password
func (h *AuthHandler) ResetPassword(c *fiber.Ctx) error {
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&body); err != nil || body.Token == "" || body.Password == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "token e senha obrigatórios"})
	}
	if err := h.svc.ResetPassword(c.Context(), body.Token, body.Password); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true, "message": "Senha redefinida com sucesso."})
}

// POST /v1/auth/verify-email
func (h *AuthHandler) VerifyEmail(c *fiber.Ctx) error {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.BodyParser(&body); err != nil || body.Token == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "token obrigatório"})
	}
	if err := h.svc.VerifyEmail(c.Context(), body.Token); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true, "message": "Email confirmado com sucesso."})
}

// POST /v1/me/resend-verification — reenvia o email de verificação (autenticado).
func (h *AuthHandler) ResendVerification(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	email := middleware.GetAccountEmail(c)
	if email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "email não encontrado"})
	}
	if err := h.svc.SendVerificationEmail(c.Context(), accountID, email); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true, "message": "Email de verificação reenviado."})
}
