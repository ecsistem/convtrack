package handlers

import (
	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/conversion"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/rules"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type RulesHandler struct {
	svc     *rules.Service
	convSvc *conversion.Service
}

func NewRules(svc *rules.Service, convSvc *conversion.Service) *RulesHandler {
	return &RulesHandler{svc: svc, convSvc: convSvc}
}

// GET /v1/rules
// Endpoint público chamado pelo tracker.js na inicialização.
// Retorna apenas regras habilitadas — sem dados sensíveis.
func (h *RulesHandler) Public(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	list, err := h.svc.List(c.Context(), project.ID, true)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if list == nil {
		list = []models.TriggerRule{}
	}
	return c.JSON(fiber.Map{"rules": list})
}

// GET /v1/dashboard/rules
func (h *RulesHandler) List(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.List(c.Context(), project.ID, false)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if list == nil {
		list = []models.TriggerRule{}
	}
	return c.JSON(fiber.Map{"rules": list})
}

// POST /v1/dashboard/rules
func (h *RulesHandler) Create(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Name           string                 `json:"name"`
		Type           string                 `json:"type"`
		EventName      string                 `json:"event_name"`
		URLPattern     string                 `json:"url_pattern"`
		Selector       string                 `json:"selector"`
		ScrollDepth    int                    `json:"scroll_depth"`
		Properties     map[string]interface{} `json:"properties"`
		FireConversion bool                   `json:"fire_conversion"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Name == "" {
		body.Name = body.EventName + " (" + body.Type + ")"
	}

	rule, err := h.svc.Create(c.Context(), rules.CreateInput{
		ProjectID:      project.ID,
		Name:           body.Name,
		Type:           body.Type,
		EventName:      body.EventName,
		URLPattern:     body.URLPattern,
		Selector:       body.Selector,
		ScrollDepth:    body.ScrollDepth,
		Properties:     body.Properties,
		FireConversion: body.FireConversion,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(rule)
}

// PUT /v1/dashboard/rules/:id
func (h *RulesHandler) Update(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	var body struct {
		Name           *string                `json:"name"`
		Enabled        *bool                  `json:"enabled"`
		EventName      *string                `json:"event_name"`
		URLPattern     *string                `json:"url_pattern"`
		Selector       *string                `json:"selector"`
		ScrollDepth    *int                   `json:"scroll_depth"`
		Properties     map[string]interface{} `json:"properties"`
		FireConversion *bool                  `json:"fire_conversion"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	rule, err := h.svc.Update(c.Context(), id, project.ID, rules.UpdateInput{
		Name:           body.Name,
		Enabled:        body.Enabled,
		EventName:      body.EventName,
		URLPattern:     body.URLPattern,
		Selector:       body.Selector,
		ScrollDepth:    body.ScrollDepth,
		Properties:     body.Properties,
		FireConversion: body.FireConversion,
	})
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(rule)
}

// PATCH /v1/dashboard/rules/:id/toggle
func (h *RulesHandler) Toggle(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if err := h.svc.ToggleEnabled(c.Context(), id, project.ID, body.Enabled); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true, "enabled": body.Enabled})
}

// DELETE /v1/dashboard/rules/:id
func (h *RulesHandler) Delete(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	if err := h.svc.Delete(c.Context(), id, project.ID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// POST /v1/collect/conversion
// Chamado pelo tracker.js quando uma regra com fire_conversion=true é ativada.
func (h *RulesHandler) ClientConversion(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		SessionID  string  `json:"session_id"`
		RuleID     string  `json:"rule_id"`
		EventName  string  `json:"event_name"`
		Value      float64 `json:"value"`
		Currency   string  `json:"currency"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	conv, err := h.svc.FireConversionFromRule(
		c.Context(), project.ID,
		body.RuleID, body.SessionID,
		body.EventName, body.Value, body.Currency,
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Enfileira disparo de integrações (Meta CAPI, TikTok, Kwai, Google).
	// Usa context.Background() pois a goroutine pode sobreviver ao request.
	if h.convSvc != nil {
		convCopy := conv
		go h.convSvc.EnqueueIntegrations(c.Context(), convCopy)
	}

	return c.JSON(fiber.Map{"ok": true})
}
