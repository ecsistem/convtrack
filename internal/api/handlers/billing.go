package handlers

import (
	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/billing"
	"github.com/gofiber/fiber/v2"
)

type BillingHandler struct {
	svc *billing.Service
}

func NewBilling(svc *billing.Service) *BillingHandler {
	return &BillingHandler{svc: svc}
}

// POST /v1/billing/checkout
func (h *BillingHandler) Checkout(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Plan string `json:"plan"`
	}
	if err := c.BodyParser(&body); err != nil || body.Plan == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "campo 'plan' obrigatório"})
	}

	email := middleware.GetAccountEmail(c)
	name := c.Locals("account_name").(string)

	url, err := h.svc.CheckoutURL(c.Context(), accountID, body.Plan, email, name)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"checkout_url": url})
}

// POST /v1/billing/webhook  (public — validated by HMAC)
func (h *BillingHandler) Webhook(c *fiber.Ctx) error {
	raw := c.Body()
	sig := c.Get("X-Pixup-Signature")
	if !h.svc.ValidateSignature(raw, sig) {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "invalid signature"})
	}
	if err := h.svc.HandleWebhook(c.Context(), raw); err != nil {
		// Return 200 to prevent PixUp retries on business logic errors
		return c.SendStatus(fiber.StatusOK)
	}
	return c.SendStatus(fiber.StatusOK)
}

// GET /v1/billing/subscription
func (h *BillingHandler) Subscription(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	sub, err := h.svc.GetSubscription(c.Context(), accountID)
	if err != nil {
		return c.JSON(fiber.Map{"subscription": nil})
	}
	return c.JSON(fiber.Map{"subscription": sub})
}
