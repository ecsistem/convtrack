package handlers

import (
	"strconv"
	"strings"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── Slug cloaker (público — sem autenticação) ─────────────────────────────

// GET /:slug — entrada principal do cloaker por slug.
// Bots/revisores → safe_url  |  Humanos → money_url.
// O slug é só um ponto de entrada: redireciona para a raiz da URL destino
// (não proxia o caminho completo — o domínio próprio faz proxy completo).
func (h *ShieldHandler) SlugCloak(c *fiber.Ctx) error {
	slug := c.Params("slug")
	campaign, err := h.svc.ResolveCampaignBySlug(c.Context(), slug)
	if err != nil || campaign == nil {
		return c.Next() // slug não existe — deixa passar para outras rotas
	}

	ip := c.IP()
	ua := c.Get("User-Agent")

	result, _ := h.svc.Check(c.Context(), campaign.ProjectID, shield.CheckRequest{
		IP:        ip,
		UserAgent: ua,
	})

	var targetURL string
	if result == nil || result.Allowed {
		targetURL, _ = shield.ChooseURL(campaign, ip)
	} else {
		// bloqueado: manda para safe_url (ou redirect_url se configurado)
		if result.RedirectURL != "" {
			targetURL = result.RedirectURL
		} else {
			targetURL = campaign.SafeURL
		}
	}

	if targetURL == "" {
		return c.Status(fiber.StatusNotFound).SendString("campaign not configured")
	}

	// Repassa query strings originais (UTMs etc.) para a URL destino
	qs := string(c.Request().URI().QueryString())
	if qs != "" {
		if strings.Contains(targetURL, "?") {
			targetURL += "&" + qs
		} else {
			targetURL += "?" + qs
		}
	}

	return c.Redirect(targetURL, fiber.StatusFound)
}

// ── Campanhas ─────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/campaigns
func (h *ShieldHandler) ListCampaigns(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.ListCampaigns(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/shield/campaigns
func (h *ShieldHandler) CreateCampaign(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var cam shield.Campaign
	if err := c.BodyParser(&cam); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	cam.ProjectID = p.ID
	created, err := h.svc.CreateCampaign(c.Context(), &cam)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// PUT /v1/dashboard/shield/campaigns/:id
func (h *ShieldHandler) UpdateCampaign(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	var cam shield.Campaign
	if err := c.BodyParser(&cam); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	cam.ID = id
	cam.ProjectID = p.ID
	if err := h.svc.UpdateCampaign(c.Context(), &cam); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	h.svc.InvalidateSlugCache(c.Context(), cam.Slug)
	return c.JSON(fiber.Map{"ok": true})
}

// DELETE /v1/dashboard/shield/campaigns/:id
func (h *ShieldHandler) DeleteCampaign(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	// busca o slug antes de deletar para invalidar o cache
	campaigns, _ := h.svc.ListCampaigns(c.Context(), p.ID)
	for _, cam := range campaigns {
		if cam.ID == id {
			h.svc.InvalidateSlugCache(c.Context(), cam.Slug)
			break
		}
	}
	if err := h.svc.DeleteCampaign(c.Context(), id, p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ── Domínios ──────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/domains
func (h *ShieldHandler) ListDomains(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.ListDomains(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/shield/domains
func (h *ShieldHandler) CreateDomain(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var d shield.Domain
	if err := c.BodyParser(&d); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	d.ProjectID = p.ID
	created, err := h.svc.CreateDomain(c.Context(), &d)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// DELETE /v1/dashboard/shield/domains/:id
func (h *ShieldHandler) DeleteDomain(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.DeleteDomain(c.Context(), id, p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ── Webhooks ──────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/webhooks
func (h *ShieldHandler) ListWebhooks(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.ListWebhooks(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/shield/webhooks
func (h *ShieldHandler) CreateWebhook(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var w shield.WebhookConfig
	if err := c.BodyParser(&w); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	w.ProjectID = p.ID
	created, err := h.svc.CreateWebhook(c.Context(), &w)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// DELETE /v1/dashboard/shield/webhooks/:id
func (h *ShieldHandler) DeleteWebhook(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.DeleteWebhook(c.Context(), id, p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ── Fingerprint (público — chamado pelo shield-fp.js) ─────────────────────

// POST /v1/shield/fingerprint
func (h *ShieldHandler) Fingerprint(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var fp shield.FingerprintData
	if err := c.BodyParser(&fp); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	ip := c.IP()
	ua := c.Get("User-Agent")

	result, err := h.svc.ProcessFingerprint(c.Context(), p.ID, ip, ua, &fp)
	if err != nil {
		return c.JSON(&shield.FingerprintResult{Allowed: true, Action: "money"})
	}

	// Se existe uma campanha associada, escolhe URL com A/B split
	if result.Allowed && fp.CampaignID != "" {
		if camID, err := uuid.Parse(fp.CampaignID); err == nil {
			campaigns, _ := h.svc.ListCampaigns(c.Context(), p.ID)
			for _, cam := range campaigns {
				if cam.ID == camID {
					url, _ := shield.ChooseURL(&cam, ip)
					result.RedirectURL = url
					result.CampaignID = fp.CampaignID
					break
				}
			}
		}
	}

	return c.JSON(result)
}

// GET /shield-fp.js — serve o script de fingerprinting (sem autenticação)
func (h *ShieldHandler) ServeFPScript(c *fiber.Ctx) error {
	c.Set("Content-Type", "application/javascript; charset=utf-8")
	c.Set("Cache-Control", "public, max-age=3600")
	return c.SendFile("./public/shield-fp.js")
}

// ── Smart Redirect com fingerprinting ────────────────────────────────────

// GET /r/:projectKey — redirect tático (server-side, sem JS)
// Serve uma página de loading que fingerprinta e redireciona.
func (h *ShieldHandler) SmartRedirectAdvanced(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusNotFound).SendString("not found")
	}

	ua := c.Get("User-Agent")
	ip := c.IP()

	// Verificação rápida server-side para bots óbvios
	result, _ := h.svc.Check(c.Context(), p.ID, shield.CheckRequest{
		IP:        ip,
		UserAgent: ua,
	})

	if result != nil && !result.Allowed {
		if result.RedirectURL != "" {
			return c.Redirect(result.RedirectURL, fiber.StatusFound)
		}
		return c.Status(200).SendString("")
	}

	// Humano (ou incerto) → serve página de fingerprinting
	cfg, _ := h.svc.GetConfig(c.Context(), p.ID)
	primaryURL := ""
	if cfg != nil {
		primaryURL = cfg.PrimaryURL
	}

	apiBase := h.svc.APIBase
	apiKey := c.Params("projectKey")

	// Página HTML que coleta fingerprint e redireciona
	html := buildFPRedirectPage(apiBase, apiKey, primaryURL)
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
}

// buildFPRedirectPage gera a página HTML de loading + fingerprinting.
func buildFPRedirectPage(apiBase, apiKey, fallbackURL string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Redirecionando...</title>
<style>
body{margin:0;background:#0a0a0b;display:flex;align-items:center;justify-content:center;min-height:100vh;}
.spinner{width:32px;height:32px;border:3px solid #1e1b4b;border-top-color:#4f46e5;border-radius:50%;animation:spin .8s linear infinite;}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head><body>
<div class="spinner"></div>
<script>
(function(){
  var API_BASE = '` + apiBase + `';
  var API_KEY  = '` + apiKey + `';
  var FALLBACK = '` + fallbackURL + `';

  function go(url){ if(url) window.location.replace(url); }

  fetch(API_BASE + '/v1/shield/fingerprint', {
    method: 'POST',
    headers: {'Content-Type':'application/json','X-API-Key':API_KEY},
    body: JSON.stringify({
      api_key: API_KEY,
      webdriver: !!navigator.webdriver,
      headless_hint: /HeadlessChrome|Puppeteer|Playwright|Selenium/i.test(navigator.userAgent),
      screen_width: screen.width, screen_height: screen.height,
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      language: navigator.language, platform: navigator.platform,
      cpu_cores: navigator.hardwareConcurrency||0,
      memory_gb: navigator.deviceMemory||0,
      touch_points: navigator.maxTouchPoints||0,
      plugins: navigator.plugins ? navigator.plugins.length : 0
    })
  })
  .then(function(r){ return r.json(); })
  .then(function(res){
    if(res.redirect_url){ go(res.redirect_url); }
    else if(FALLBACK){ go(FALLBACK); }
  })
  .catch(function(){ if(FALLBACK) go(FALLBACK); });

  // Timeout de segurança
  setTimeout(function(){ if(FALLBACK) go(FALLBACK); }, 5000);
})();
</script>
</body></html>`
}

// ── Visitas ───────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/visits
func (h *ShieldHandler) ListVisits(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	type VisitRow struct {
		ID         string   `json:"id"`
		IP         string   `json:"ip"`
		Country    string   `json:"country"`
		Device     string   `json:"device"`
		IsBot      bool     `json:"is_bot"`
		BotScore   float64  `json:"bot_score"`
		Signals    []string `json:"signals"`
		Action     string   `json:"action"`
		DestURL    string   `json:"dest_url"`
		CreatedAt  string   `json:"created_at"`
	}

	rows, err := h.svc.DB().Query(c.Context(), `
		SELECT id::text, ip, country, device, is_bot, bot_score, signals, action, dest_url, created_at::text
		FROM shield_visits
		WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, p.ID, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	var visits []VisitRow
	for rows.Next() {
		var v VisitRow
		if err := rows.Scan(&v.ID, &v.IP, &v.Country, &v.Device, &v.IsBot,
			&v.BotScore, &v.Signals, &v.Action, &v.DestURL, &v.CreatedAt); err != nil {
			continue
		}
		visits = append(visits, v)
	}
	if visits == nil {
		visits = []VisitRow{}
	}

	var total int
	_ = h.svc.DB().QueryRow(c.Context(),
		`SELECT COUNT(*) FROM shield_visits WHERE project_id = $1`, p.ID,
	).Scan(&total)

	return c.JSON(fiber.Map{"data": visits, "total": total, "limit": limit, "offset": offset})
}
