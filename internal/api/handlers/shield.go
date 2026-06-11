package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type ShieldHandler struct {
	svc *shield.Service
	rdb *redis.Client
}

func NewShield(svc *shield.Service, rdb *redis.Client) *ShieldHandler {
	return &ShieldHandler{svc: svc, rdb: rdb}
}

// ── Dashboard endpoints (JWT) ─────────────────────────────────────────────

// GET /v1/dashboard/shield/config
func (h *ShieldHandler) GetConfig(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	cfg, err := h.svc.GetConfig(c.Context(), project.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(cfg)
}

// PUT /v1/dashboard/shield/config
func (h *ShieldHandler) PutConfig(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var cfg models.ShieldConfig
	if err := c.BodyParser(&cfg); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	// Garantir slices não nulos
	if cfg.GeoCountries == nil {
		cfg.GeoCountries = []string{}
	}
	if cfg.FallbackURLs == nil {
		cfg.FallbackURLs = []string{}
	}
	if cfg.BlockedIPs == nil {
		cfg.BlockedIPs = []string{}
	}

	if err := h.svc.UpsertConfig(c.Context(), project.ID, &cfg); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// GET /v1/dashboard/shield/logs
func (h *ShieldHandler) ListLogs(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))

	logs, total, err := h.svc.ListLogs(c.Context(), project.ID, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": logs, "total": total, "limit": limit, "offset": offset})
}

// GET /v1/dashboard/shield/logs/stream  — SSE
func (h *ShieldHandler) StreamLogs(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	channel := fmt.Sprintf("shield:%s", project.ID)

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	heartbeat := time.NewTicker(30 * time.Second)

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer heartbeat.Stop()

		pubsub := h.rdb.Subscribe(c.Context(), channel)
		defer pubsub.Close()

		redisCh := pubsub.Channel()

		fmt.Fprintf(w, "data: {\"connected\":true,\"project_id\":%q}\n\n", project.ID)
		_ = w.Flush()

		for {
			select {
			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
				if err := w.Flush(); err != nil {
					return
				}
			case <-heartbeat.C:
				fmt.Fprintf(w, ": heartbeat\n\n")
				if err := w.Flush(); err != nil {
					return
				}
			case <-c.Context().Done():
				return
			}
		}
	})

	return nil
}

// ── Public endpoint (API key) ─────────────────────────────────────────────

// POST /v1/shield/check — chamado pelo tracker.js
func (h *ShieldHandler) Check(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		WebDriver    bool `json:"webdriver"`
		HeadlessHint bool `json:"headless_hint"`
		DevTools     bool `json:"devtools"`
	}
	_ = c.BodyParser(&body)

	ua := c.Get("User-Agent")
	ip := c.IP()

	result, err := h.svc.Check(c.Context(), project.ID, shield.CheckRequest{
		IP:           ip,
		UserAgent:    ua,
		WebDriver:    body.WebDriver,
		HeadlessHint: body.HeadlessHint,
		DevTools:     body.DevTools,
	})
	if err != nil {
		return c.JSON(&shield.CheckResult{Allowed: true})
	}
	return c.JSON(result)
}

// GET /r/:projectKey — smart redirect (server-side cloaking link)
// Qualquer tráfego (incluindo bots sem JS) bate nesse endpoint.
func (h *ShieldHandler) SmartRedirect(c *fiber.Ctx) error {
	projectKey := c.Params("projectKey")
	if projectKey == "" {
		return c.Status(fiber.StatusBadRequest).SendString("missing project key")
	}

	// Resolve project via header simulado (reusa APIKey middleware internamente)
	// Para simplificar: passa como query param api_key
	c.Request().Header.Set("X-API-Key", projectKey)

	// Resolve manualmente o projeto
	// Nota: o ShieldHandler recebe o *shield.Service mas não tem acesso ao DB diretamente.
	// Vamos usar o helper do middleware via context — o router deve passar apiKeyAuth antes deste handler.
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusNotFound).SendString("not found")
	}

	ua := c.Get("User-Agent")
	ip := c.IP()

	dest, blocked := h.svc.SmartRedirect(c.Context(), project.ID, shield.CheckRequest{
		IP:        ip,
		UserAgent: ua,
	})

	if dest == "" {
		if blocked {
			return c.Status(200).SendString("") // página em branco
		}
		return c.Status(404).SendString("primary URL not configured")
	}

	return c.Redirect(dest, fiber.StatusFound)
}

// ── DELETE logs ───────────────────────────────────────────────────────────

// DELETE /v1/dashboard/shield/logs — limpa todos os logs do projeto
func (h *ShieldHandler) ClearLogs(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projectID, err := uuid.Parse(project.ID.String())
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid project"})
	}
	_ = projectID

	// O handler não tem acesso direto ao DB — expor via service
	// Por ora deixamos o método público no service
	if err := h.svc.ClearLogs(c.Context(), project.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// ── stats ─────────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/stats
func (h *ShieldHandler) Stats(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	rows, err := h.svc.StatsLogs(c.Context(), project.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	_ = json.Marshal // suppress unused import warning if needed
	return c.JSON(fiber.Map{"data": rows})
}
