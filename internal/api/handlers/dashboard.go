package handlers

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/conversion"
	"github.com/ecsistem/convtrack/internal/session"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type DashboardHandler struct {
	conversions *conversion.Service
	sessions    *session.Service
}

func NewDashboard(conv *conversion.Service, sess *session.Service) *DashboardHandler {
	return &DashboardHandler{conversions: conv, sessions: sess}
}

// GET /v1/dashboard/overview?days=30
func (h *DashboardHandler) Overview(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	days := c.QueryInt("days", 30)
	since := time.Now().AddDate(0, 0, -days)

	stats, err := h.conversions.Stats(c.Context(), project.ID, since)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	sessionCount, _ := h.sessions.CountSessions(c.Context(), project.ID, since)

	var totalRevenue float64
	var totalConversions int
	for _, s := range stats {
		totalRevenue += s.Revenue
		totalConversions += s.Conversions
	}

	cpa := 0.0
	if totalConversions > 0 {
		cpa = totalRevenue / float64(totalConversions)
	}

	return c.JSON(fiber.Map{
		"revenue":     totalRevenue,
		"conversions": totalConversions,
		"sessions":    sessionCount,
		"cpa":         cpa,
		"campaigns":   stats,
	})
}

// GET /v1/dashboard/conversions?limit=50&offset=0
func (h *DashboardHandler) Conversions(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	limit := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)
	if limit > 200 {
		limit = 200
	}

	list, err := h.conversions.List(c.Context(), project.ID, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"data": list, "limit": limit, "offset": offset})
}

// GET /v1/dashboard/sessions?limit=50&offset=0
func (h *DashboardHandler) Sessions(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	limit := c.QueryInt("limit", 50)
	if limit > 200 {
		limit = 200
	}
	if limit < 1 {
		limit = 50
	}

	// Modo legado por offset (mantido p/ compatibilidade): só usado se ?offset estiver presente.
	if c.Query("cursor") == "" && c.Query("offset") != "" {
		offset := c.QueryInt("offset", 0)
		list, err := h.sessions.ListSessions(c.Context(), project.ID, limit, offset)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"data": list, "limit": limit, "offset": offset})
	}

	// Paginação por cursor (keyset) — estável sob inserções em tempo real.
	beforeTime, beforeID, err := decodeCursor(c.Query("cursor"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cursor inválido"})
	}

	list, err := h.sessions.ListSessionsCursor(c.Context(), project.ID, beforeTime, beforeID, limit)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	var nextCursor string
	if len(list) == limit {
		last := list[len(list)-1]
		nextCursor = encodeCursor(last.StartedAt, last.ID)
	}

	return c.JSON(fiber.Map{"data": list, "limit": limit, "next_cursor": nextCursor})
}

// encodeCursor serializa (started_at, id) em um token opaco base64.
func encodeCursor(t time.Time, id uuid.UUID) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor faz o parse de um token de cursor. Token vazio = primeira página.
func decodeCursor(cursor string) (*time.Time, uuid.UUID, error) {
	if cursor == "" {
		return nil, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, uuid.Nil, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, uuid.Nil, fmt.Errorf("formato inválido")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, uuid.Nil, err
	}
	return &t, id, nil
}

// GET /v1/dashboard/sessions/:id/events
// Retorna todos os eventos rastreados para uma sessão específica (sidebar do replay player).
func (h *DashboardHandler) SessionEvents(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	sessionID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session id"})
	}

	events, err := h.sessions.ListSessionEvents(c.Context(), project.ID, sessionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"data": events})
}
