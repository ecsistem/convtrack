package handlers

import (
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
	offset := c.QueryInt("offset", 0)
	if limit > 200 {
		limit = 200
	}

	list, err := h.sessions.ListSessions(c.Context(), project.ID, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"data": list, "limit": limit, "offset": offset})
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
