package handlers

import (
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/heatmap"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type HeatmapHandler struct {
	svc *heatmap.Service
}

func NewHeatmap(svc *heatmap.Service) *HeatmapHandler {
	return &HeatmapHandler{svc: svc}
}

// POST /v1/collect/clicks — público (apiKeyAuth). Recebe lote de cliques do tracker.
func (h *HeatmapHandler) Collect(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		SessionID string          `json:"session_id"`
		Clicks    []heatmap.Click `json:"clicks"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if len(body.Clicks) == 0 {
		return c.JSON(fiber.Map{"ok": true})
	}
	// Limite defensivo de tamanho de lote.
	if len(body.Clicks) > 200 {
		body.Clicks = body.Clicks[:200]
	}

	sessionID, _ := uuid.Parse(body.SessionID)
	if err := h.svc.InsertClicks(c.Context(), project.ID, sessionID, body.Clicks); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// GET /v1/dashboard/heatmap?url=/&days=7 — agregado de cliques de uma URL.
func (h *HeatmapHandler) Get(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	days := c.QueryInt("days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	since := time.Now().AddDate(0, 0, -days)

	urlPath := c.Query("url")
	if urlPath == "" {
		// sem URL: retorna apenas a lista de URLs disponíveis.
		urls, err := h.svc.ListURLs(c.Context(), project.ID, since)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"urls": urls})
	}

	points, elements, total, err := h.svc.Aggregate(c.Context(), project.ID, urlPath, since)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	urls, _ := h.svc.ListURLs(c.Context(), project.ID, since)

	return c.JSON(fiber.Map{
		"url":      urlPath,
		"total":    total,
		"points":   points,
		"elements": elements,
		"urls":     urls,
	})
}
