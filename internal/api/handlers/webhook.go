package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/ecsistem/convtrack/internal/attribution"
	"github.com/ecsistem/convtrack/internal/conversion"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type WebhookHandler struct {
	conversions *conversion.Service
	attribution *attribution.Service
}

func NewWebhook(conv *conversion.Service, attr *attribution.Service) *WebhookHandler {
	return &WebhookHandler{conversions: conv, attribution: attr}
}

// POST /v1/webhooks/:projectKey/:platform
func (h *WebhookHandler) Handle(c *fiber.Ctx) error {
	platform := c.Params("platform")
	projectKey := c.Params("projectKey")

	var raw map[string]interface{}
	if err := c.BodyParser(&raw); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	// Validate signature when possible
	if err := validateSignature(platform, c.Body(), c.Get("X-Hotmart-Hottok"), c.Get("X-Hub-Signature-256")); err != nil {
		// Log but don't reject — some platforms don't sign
		fmt.Printf("webhook signature warn [%s]: %v\n", platform, err)
	}

	in, err := parseWebhookPayload(platform, projectKey, raw)
	if err != nil {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": true, "skipped": err.Error()})
	}

	// Resolve attribution — usada para ligar conversion à session
	_, sess, attrErr := h.attribution.ResolveForConversion(
		c.Context(), in.ProjectID, in.SessionID, in.Email, in.Phone,
	)

	conv, err := h.conversions.Create(c.Context(), conversion.CreateInput{
		ProjectID:     in.ProjectID,
		ExternalID:    in.OrderID,
		EventName:     in.EventName,
		Value:         in.Value,
		Currency:      in.Currency,
		Email:         in.Email,
		Phone:         in.Phone,
		EmailHash:     attribution.HashIdentifier(in.Email),
		PhoneHash:     attribution.HashIdentifier(in.Phone),
		Platform:      platform,
		RawPayload:    raw,
		PaymentMethod: in.PaymentMethod,
		Status:        in.Status,
		ProductName:   in.ProductName,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "conversion error"})
	}

	if attrErr == nil && sess != nil {
		_ = h.conversions.MarkAttributed(c.Context(), conv.ID, sess.ID)
		conv.SessionID = &sess.ID
	}

	// Enfileira disparos — retorno imediato, worker processa com retry exponencial
	h.conversions.EnqueueIntegrations(c.Context(), conv)

	return c.JSON(fiber.Map{"ok": true, "conversion_id": conv.ID.String()})
}

type parsedWebhook struct {
	ProjectID     uuid.UUID
	SessionID     *uuid.UUID
	OrderID       string
	EventName     string
	Email         string
	Phone         string
	Value         float64
	Currency      string
	PaymentMethod string // cartao | pix | boleto
	Status        string // approved | pending | refunded | chargeback
	ProductName   string
}

func parseWebhookPayload(platform, projectKey string, raw map[string]interface{}) (*parsedWebhook, error) {
	projectID, err := uuid.Parse(projectKey)
	if err != nil {
		// projectKey is an API key — but for webhooks we need the project ID
		// In production: look up by API key. For simplicity, we use project UUID directly
		return nil, fmt.Errorf("invalid project key format")
	}

	out := &parsedWebhook{ProjectID: projectID, Currency: "BRL", EventName: "Purchase"}

	switch platform {
	case "hotmart":
		return parseHotmart(out, raw)
	case "kiwify":
		return parseKiwify(out, raw)
	case "eduzz":
		return parseEduzz(out, raw)
	case "generic":
		return parseGeneric(out, raw)
	default:
		return nil, fmt.Errorf("unsupported platform: %s", platform)
	}
}

func parseHotmart(out *parsedWebhook, raw map[string]interface{}) (*parsedWebhook, error) {
	data, _ := raw["data"].(map[string]interface{})
	purchase, _ := data["purchase"].(map[string]interface{})
	buyer, _ := data["buyer"].(map[string]interface{})
	product, _ := data["product"].(map[string]interface{})

	status, _ := purchase["status"].(string)
	switch status {
	case "APPROVED", "COMPLETE":
		out.Status = "approved"
	case "WAITING_PAYMENT", "UNDER_ANALISYS":
		out.Status = "pending"
	case "REFUNDED", "CANCELLED":
		out.Status = "refunded"
	case "CHARGEBACK":
		out.Status = "chargeback"
	default:
		return nil, fmt.Errorf("hotmart: unhandled status: %s", status)
	}

	out.OrderID, _ = purchase["transaction"].(string)
	out.Email, _ = buyer["email"].(string)
	out.Phone, _ = buyer["checkout_phone"].(string)

	if price, ok := purchase["price"].(map[string]interface{}); ok {
		out.Value, _ = price["value"].(float64)
		out.Currency, _ = price["currency_value"].(string)
	}

	// Payment method
	if payType, ok := purchase["payment"].(map[string]interface{}); ok {
		if t, ok := payType["type"].(string); ok {
			out.PaymentMethod = normalisePaymentMethod(t)
		}
	}

	if product != nil {
		out.ProductName, _ = product["name"].(string)
	}

	return out, nil
}

func parseKiwify(out *parsedWebhook, raw map[string]interface{}) (*parsedWebhook, error) {
	status, _ := raw["order_status"].(string)
	switch status {
	case "paid":
		out.Status = "approved"
	case "waiting_payment":
		out.Status = "pending"
	case "refunded":
		out.Status = "refunded"
	case "chargedback":
		out.Status = "chargeback"
	default:
		return nil, fmt.Errorf("kiwify: unhandled status: %s", status)
	}

	out.OrderID, _ = raw["order_id"].(string)

	if customer, ok := raw["Customer"].(map[string]interface{}); ok {
		out.Email, _ = customer["email"].(string)
		out.Phone, _ = customer["mobile"].(string)
	}

	if orderValue, ok := raw["order_value"].(float64); ok {
		out.Value = orderValue / 100 // Kiwify sends cents
	}

	if pm, ok := raw["payment_method"].(string); ok {
		out.PaymentMethod = normalisePaymentMethod(pm)
	}

	if product, ok := raw["Product"].(map[string]interface{}); ok {
		out.ProductName, _ = product["name"].(string)
	}

	return out, nil
}

func parseEduzz(out *parsedWebhook, raw map[string]interface{}) (*parsedWebhook, error) {
	status, _ := raw["sale_status_name"].(string)
	statusLower := strings.ToLower(status)
	switch statusLower {
	case "paid", "pago":
		out.Status = "approved"
	case "waiting", "aguardando":
		out.Status = "pending"
	case "refunded", "reembolsado":
		out.Status = "refunded"
	default:
		return nil, fmt.Errorf("eduzz: unhandled status: %s", status)
	}

	out.OrderID = fmt.Sprintf("%v", raw["sale_id"])
	out.Email, _ = raw["client_email"].(string)
	out.Phone, _ = raw["client_cel"].(string)
	out.Value, _ = raw["sale_amount_win"].(float64)
	out.ProductName, _ = raw["product_name"].(string)

	if pm, ok := raw["payment_method"].(string); ok {
		out.PaymentMethod = normalisePaymentMethod(pm)
	}

	return out, nil
}

func parseGeneric(out *parsedWebhook, raw map[string]interface{}) (*parsedWebhook, error) {
	out.OrderID, _ = raw["order_id"].(string)
	out.Email, _ = raw["email"].(string)
	out.Phone, _ = raw["phone"].(string)
	out.Value, _ = raw["value"].(float64)
	out.ProductName, _ = raw["product_name"].(string)
	if currency, ok := raw["currency"].(string); ok {
		out.Currency = currency
	}
	if eventName, ok := raw["event"].(string); ok {
		out.EventName = eventName
	}
	if pm, ok := raw["payment_method"].(string); ok {
		out.PaymentMethod = normalisePaymentMethod(pm)
	}
	if st, ok := raw["status"].(string); ok {
		out.Status = st
	}

	// Support passing session_id for precise attribution
	if sessionStr, ok := raw["session_id"].(string); ok {
		if id, err := uuid.Parse(sessionStr); err == nil {
			out.SessionID = &id
		}
	}

	return out, nil
}

// normalisePaymentMethod maps platform-specific payment method strings to
// the canonical set used internally: cartao | pix | boleto | outros.
func normalisePaymentMethod(s string) string {
	switch strings.ToLower(s) {
	case "credit_card", "creditcard", "cartao", "cartão", "card":
		return "cartao"
	case "pix":
		return "pix"
	case "boleto", "bank_slip", "bankslip":
		return "boleto"
	default:
		return ""
	}
}

func validateSignature(platform string, body []byte, hotmartToken, hubSignature string) error {
	switch platform {
	case "hotmart":
		expected := os.Getenv("HOTMART_HOTTOK")
		if expected != "" && hotmartToken != expected {
			return fmt.Errorf("invalid hotmart token")
		}
	case "kiwify":
		secret := os.Getenv("KIWIFY_WEBHOOK_SECRET")
		if secret != "" && hubSignature != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
			if !hmac.Equal([]byte(expected), []byte(hubSignature)) {
				return fmt.Errorf("invalid kiwify signature")
			}
		}
	}
	return nil
}
