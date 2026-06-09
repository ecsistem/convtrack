package handlers

import (
	"encoding/json"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/replay"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ReplayHandler struct {
	svc *replay.Service
}

func NewReplay(svc *replay.Service) *ReplayHandler {
	return &ReplayHandler{svc: svc}
}

// POST /v1/replay/events
// Body: { "session_id": "uuid", "trigger": "checkout", "events": [ ...rrweb events... ] }
func (h *ReplayHandler) Collect(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		SessionID string            `json:"session_id"`
		Trigger   string            `json:"trigger"`   // ex: "checkout", "purchase", "lead"
		Events    []json.RawMessage `json:"events"`
	}

	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	if len(body.Events) == 0 {
		return c.JSON(fiber.Map{"ok": true, "buffered": 0})
	}

	sessionID, err := uuid.Parse(body.SessionID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session_id"})
	}

	if err := h.svc.AppendEvents(c.Context(), project.ID, sessionID, body.Events, body.Trigger); err != nil {
		// Não quebra o cliente se o replay falhar — apenas loga
		c.Context().Logger().Printf("replay append error: %v", err)
	}

	return c.JSON(fiber.Map{"ok": true, "buffered": len(body.Events)})
}

// POST /v1/replay/flush
// Chamado pelo tracker.js no evento beforeunload para garantir que os eventos sejam salvos
func (h *ReplayHandler) Flush(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		SessionID string `json:"session_id"`
		Trigger   string `json:"trigger"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	sessionID, err := uuid.Parse(body.SessionID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session_id"})
	}

	if err := h.svc.FlushToS3(c.Context(), project.ID, sessionID, body.Trigger); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"ok": true})
}

// GET /v1/replay/:sessionId
// Retorna presigned URL do S3 para o rrweb-player reproduzir
func (h *ReplayHandler) GetURL(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	sessionID, err := uuid.Parse(c.Params("sessionId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session_id"})
	}

	url, err := h.svc.GetPresignedURL(c.Context(), sessionID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "replay not found"})
	}

	return c.JSON(fiber.Map{
		"url":        url,
		"session_id": sessionID,
		"expires_in": 3600, // segundos
	})
}
