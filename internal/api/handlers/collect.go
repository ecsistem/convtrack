package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/attribution"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/session"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type CollectHandler struct {
	sessions    *session.Service
	attribution *attribution.Service
}

func NewCollect(sessions *session.Service, attr *attribution.Service) *CollectHandler {
	return &CollectHandler{sessions: sessions, attribution: attr}
}

type sessionPayload struct {
	VisitorID   string `json:"visitor_id"`
	SessionID   string `json:"session_id"`
	LandingPage string `json:"landing_page"`
	Referrer    string `json:"referrer"`
	UserAgent   string `json:"user_agent"`
	// Device / browser info
	ScreenWidth  int    `json:"screen_width"`
	ScreenHeight int    `json:"screen_height"`
	Timezone     string `json:"timezone"`
	Language     string `json:"language"`
	// UTM params
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMContent  string `json:"utm_content"`
	UTMTerm     string `json:"utm_term"`
	// Click IDs
	FBClid string `json:"fbclid"`
	GClid  string `json:"gclid"`
	TTClid string `json:"ttclid"`
	KWClid string `json:"kwclid"`
	// Cookies
	FBP string `json:"fbp"`
	FBC string `json:"fbc"`
}

// POST /v1/collect/session
func (h *CollectHandler) Session(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var p sessionPayload
	if err := c.BodyParser(&p); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	visitorID, err := parseOrNewUUID(p.VisitorID)
	if err != nil {
		visitorID = uuid.New()
	}
	sessionID, err := parseOrNewUUID(p.SessionID)
	if err != nil {
		sessionID = uuid.New()
	}

	// Upsert visitor
	_, err = h.sessions.GetOrCreateVisitor(c.Context(), project.ID, visitorID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "visitor error"})
	}

	// Upsert session
	ip := c.IP()
	ua := p.UserAgent
	if ua == "" {
		ua = c.Get("User-Agent")
	}

	sess, err := h.sessions.UpsertSession(c.Context(), session.UpsertSessionInput{
		SessionID:    sessionID,
		VisitorID:    visitorID,
		ProjectID:    project.ID,
		LandingPage:  p.LandingPage,
		Referrer:     p.Referrer,
		UserAgent:    ua,
		IP:           ip,
		ScreenWidth:  p.ScreenWidth,
		ScreenHeight: p.ScreenHeight,
		Timezone:     p.Timezone,
		Language:     p.Language,
	})
	if err == nil {
		// Geo lookup assíncrono — não bloqueia a resposta
		go lookupGeo(sess.ID, ip, h.sessions)
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "session error"})
	}

	// Upsert attribution (only if there's any UTM or click ID)
	hasAttribution := p.UTMSource != "" || p.FBClid != "" || p.GClid != "" || p.TTClid != "" || p.KWClid != ""
	if hasAttribution {
		attr := &models.Attribution{
			SessionID:   sess.ID,
			ProjectID:   project.ID,
			UTMSource:   p.UTMSource,
			UTMMedium:   p.UTMMedium,
			UTMCampaign: p.UTMCampaign,
			UTMContent:  p.UTMContent,
			UTMTerm:     p.UTMTerm,
			FBClid:      p.FBClid,
			GClid:       p.GClid,
			TTClid:      p.TTClid,
			KWClid:      p.KWClid,
			FBP:         p.FBP,
			FBC:         p.FBC,
		}
		_ = h.sessions.UpsertAttribution(c.Context(), attr)
	}

	return c.JSON(fiber.Map{
		"visitor_id": visitorID.String(),
		"session_id": sess.ID.String(),
		"ok":         true,
	})
}

type eventPayload struct {
	SessionID  string                 `json:"session_id"`
	Name       string                 `json:"name"`
	Properties map[string]interface{} `json:"properties"`
}

type identifyPayload struct {
	SessionID  string `json:"session_id"`
	VisitorID  string `json:"visitor_id"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
}

// POST /v1/collect/event
func (h *CollectHandler) Event(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var p eventPayload
	if err := c.BodyParser(&p); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	sessionID, err := uuid.Parse(p.SessionID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid session_id"})
	}

	if err := h.sessions.RecordEvent(c.Context(), sessionID, project.ID, p.Name, p.Properties); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "event error"})
	}

	return c.JSON(fiber.Map{"ok": true})
}

// POST /v1/collect/identify — links email/phone to visitor for attribution
func (h *CollectHandler) Identify(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var p identifyPayload
	if err := c.BodyParser(&p); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	visitorID, err := uuid.Parse(p.VisitorID)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid visitor_id"})
	}

	if p.Email != "" {
		hash := attribution.HashIdentifier(p.Email)
		_ = h.sessions.RecordIdentifier(c.Context(), project.ID, visitorID, "email", hash)
	}
	if p.Phone != "" {
		hash := attribution.HashIdentifier(p.Phone)
		_ = h.sessions.RecordIdentifier(c.Context(), project.ID, visitorID, "phone", hash)
	}

	return c.JSON(fiber.Map{"ok": true})
}

// POST /v1/collect/heartbeat
// Chamado pelo tracker.js a cada 30s e no beforeunload para manter métricas atualizadas.
func (h *CollectHandler) Heartbeat(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		SessionID       string `json:"session_id"`
		DurationSeconds int    `json:"duration_seconds"`
		PageCount       int    `json:"page_count"`
		CurrentPage     string `json:"current_page"`
		ClickCount      int    `json:"click_count"`
		InputCount      int    `json:"input_count"`
		ScrollDepthPct  int    `json:"scroll_depth_pct"`
		RageClicks      int    `json:"rage_clicks"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	_ = h.sessions.Heartbeat(c.Context(), session.HeartbeatInput{
		SessionID:       body.SessionID,
		DurationSeconds: body.DurationSeconds,
		PageCount:       body.PageCount,
		CurrentPage:     body.CurrentPage,
		ClickCount:      body.ClickCount,
		InputCount:      body.InputCount,
		ScrollDepthPct:  body.ScrollDepthPct,
		RageClicks:      body.RageClicks,
	})
	return c.JSON(fiber.Map{"ok": true})
}

// lookupGeo faz lookup de IP via ip-api.com e preenche country/city da sessão.
// Executado em goroutine — falhas são silenciosas.
func lookupGeo(sessionID uuid.UUID, ip string, svc *session.Service) {
	// Não faz lookup de IPs privados/loopback
	if ip == "" || ip == "127.0.0.1" || strings.HasPrefix(ip, "192.168.") ||
		strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "172.") || ip == "::1" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=country,city,status", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return
	}
	defer resp.Body.Close()

	var result struct {
		Status  string `json:"status"`
		Country string `json:"country"`
		City    string `json:"city"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Status != "success" {
		return
	}

	_ = svc.UpdateGeo(context.Background(), sessionID, result.Country, result.City)
}

func parseOrNewUUID(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.UUID{}, nil
	}
	return uuid.Parse(s)
}
