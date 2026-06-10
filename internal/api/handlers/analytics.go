package handlers

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/adsync"
	"github.com/ecsistem/convtrack/internal/analytics"
	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/conversion"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/session"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AnalyticsHandler struct {
	svc      *analytics.Service
	sessions *session.Service
	convSvc  *conversion.Service
	db       *pgxpool.Pool
}

func NewAnalytics(svc *analytics.Service, sessions *session.Service, convSvc *conversion.Service, db *pgxpool.Pool) *AnalyticsHandler {
	return &AnalyticsHandler{svc: svc, sessions: sessions, convSvc: convSvc, db: db}
}

// GET /v1/dashboard/analytics
// Query params:
//   period=today|yesterday|7d|30d|90d|custom  (default: 30d)
//   start=2024-01-01  (used when period=custom)
//   end=2024-01-31
//   source=facebook
//   platform=meta|google|tiktok|kwai
//   product=<product name>
func (h *AnalyticsHandler) Get(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	f := analytics.Filters{ProjectID: project.ID}

	period := c.Query("period", "30d")
	now := time.Now()
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	switch period {
	case "today":
		f.Start = today
		f.End = today.Add(24 * time.Hour)
	case "yesterday":
		f.Start = today.AddDate(0, 0, -1)
		f.End = today
	case "7d":
		f.Start = today.AddDate(0, 0, -7)
		f.End = today.Add(24 * time.Hour)
	case "30d":
		f.Start = today.AddDate(0, 0, -30)
		f.End = today.Add(24 * time.Hour)
	case "90d":
		f.Start = today.AddDate(0, 0, -90)
		f.End = today.Add(24 * time.Hour)
	case "custom":
		var parseErr error
		f.Start, parseErr = time.ParseInLocation("2006-01-02", c.Query("start"), loc)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid start date"})
		}
		end, parseErr := time.ParseInLocation("2006-01-02", c.Query("end"), loc)
		if parseErr != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid end date"})
		}
		f.End = end.Add(24 * time.Hour)
	default:
		f.Start = today.AddDate(0, 0, -30)
		f.End = today.Add(24 * time.Hour)
	}

	f.Source = c.Query("source")
	f.Platform = c.Query("platform")
	f.Product = c.Query("product")

	result, err := h.svc.Get(c.Context(), f)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(result)
}

// GET /v1/dashboard/settings
func (h *AnalyticsHandler) GetSettings(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	taxRate, monthlyExp, productCostDefault := h.svc.GetSettings(c.Context(), project.ID)
	return c.JSON(fiber.Map{
		"tax_rate":                    taxRate,
		"additional_expenses_monthly": monthlyExp,
		"product_cost_default":        productCostDefault,
	})
}

// PUT /v1/dashboard/settings
func (h *AnalyticsHandler) PutSettings(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var body struct {
		TaxRate                   float64 `json:"tax_rate"`
		AdditionalExpensesMonthly float64 `json:"additional_expenses_monthly"`
		ProductCostDefault        float64 `json:"product_cost_default"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if err := h.svc.UpsertSettings(c.Context(), project.ID, body.TaxRate, body.AdditionalExpensesMonthly, body.ProductCostDefault); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// GET /v1/dashboard/ad-costs?start=2024-01-01&end=2024-01-31
func (h *AnalyticsHandler) ListAdCosts(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	startStr := c.Query("start", today.AddDate(0, 0, -30).Format("2006-01-02"))
	endStr := c.Query("end", today.Format("2006-01-02"))

	start, err := time.ParseInLocation("2006-01-02", startStr, loc)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid start"})
	}
	end, err := time.ParseInLocation("2006-01-02", endStr, loc)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid end"})
	}

	list, err := h.svc.ListAdCosts(c.Context(), project.ID, start, end)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if list == nil {
		list = []analytics.AdCostRow{}
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/ad-costs
func (h *AnalyticsHandler) AddAdCost(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var body struct {
		Date        string  `json:"date"`
		Platform    string  `json:"platform"`
		UTMSource   string  `json:"utm_source"`
		UTMCampaign string  `json:"utm_campaign"`
		AdAccountID string  `json:"ad_account_id"`
		Amount      float64 `json:"amount"`
		Currency    string  `json:"currency"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.Amount <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "amount must be > 0"})
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	dateVal, err := time.ParseInLocation("2006-01-02", body.Date, loc)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid date"})
	}
	cur := body.Currency
	if cur == "" {
		cur = "BRL"
	}

	id, err := h.svc.AddAdCost(c.Context(), project.ID, analytics.AdCostRow{
		Date: dateVal, Platform: body.Platform,
		UTMSource: body.UTMSource, UTMCampaign: body.UTMCampaign,
		AdAccountID: body.AdAccountID, Amount: body.Amount, Currency: cur,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": id})
}

// DELETE /v1/dashboard/ad-costs/:id
func (h *AnalyticsHandler) DeleteAdCost(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.DeleteAdCost(c.Context(), project.ID, id); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// GET /v1/dashboard/campaigns?period=7d&start=...&end=...
// Returns per-UTM-campaign metrics: sessions, leads, sales, revenue, ad_spend, ROAS, CPA, ROI.
func (h *AnalyticsHandler) GetCampaigns(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var start, end time.Time
	period := c.Query("period", "30d")
	switch period {
	case "today":
		start, end = today, today.Add(24*time.Hour)
	case "yesterday":
		start, end = today.AddDate(0, 0, -1), today
	case "7d":
		start, end = today.AddDate(0, 0, -7), today.Add(24*time.Hour)
	case "30d":
		start, end = today.AddDate(0, 0, -30), today.Add(24*time.Hour)
	case "90d":
		start, end = today.AddDate(0, 0, -90), today.Add(24*time.Hour)
	case "custom":
		var err error
		start, err = time.ParseInLocation("2006-01-02", c.Query("start"), loc)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid start"})
		}
		e, err := time.ParseInLocation("2006-01-02", c.Query("end"), loc)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid end"})
		}
		end = e.Add(24 * time.Hour)
	default:
		start, end = today.AddDate(0, 0, -30), today.Add(24*time.Hour)
	}

	data, err := h.svc.GetCampaigns(c.Context(), project.ID, start, end)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": data, "period": fiber.Map{"start": start, "end": end}})
}

// GET /v1/dashboard/online
// Returns count of sessions active in the last 5 minutes.
func (h *AnalyticsHandler) GetOnline(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	count, err := h.sessions.OnlineCount(c.Context(), project.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"online": count})
}

// POST /v1/dashboard/sync-costs
// Automatically pulls campaign spend from Meta Marketing API and upserts into ad_costs.
// Requires the Meta integration to have access_token + ad_account_id configured.
// Body (optional): { "days": 30 } — how many past days to sync (default 30).
func (h *AnalyticsHandler) SyncAdCosts(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Days int `json:"days"`
	}
	_ = c.BodyParser(&body)
	if body.Days <= 0 || body.Days > 90 {
		body.Days = 30
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Now().In(loc)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := end.AddDate(0, 0, -body.Days)

	// ── Meta ──────────────────────────────────────────────────────────────────
	accessToken, adAccountID, metaErr := adsync.GetMetaConfig(c.Context(), h.db, project.ID)
	metaSynced := 0
	var metaErrMsg string
	if metaErr == nil {
		n, err := adsync.SyncMeta(c.Context(), h.db, project.ID, adAccountID, accessToken, start, end)
		if err != nil {
			metaErrMsg = err.Error()
		} else {
			metaSynced = n
		}
	} else {
		metaErrMsg = metaErr.Error()
	}

	return c.JSON(fiber.Map{
		"ok": true,
		"synced": fiber.Map{
			"meta": fiber.Map{"rows": metaSynced, "error": metaErrMsg},
		},
		"period": fiber.Map{"start": start.Format("2006-01-02"), "end": end.Format("2006-01-02")},
	})
}

// GET /v1/dashboard/events?period=30d&name=Purchase&limit=100&offset=0
// Retorna stats agrupadas por nome + lista paginada de eventos recentes.
func (h *AnalyticsHandler) GetEvents(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var since time.Time
	switch c.Query("period", "30d") {
	case "today":
		since = today
	case "7d":
		since = today.AddDate(0, 0, -7)
	case "90d":
		since = today.AddDate(0, 0, -90)
	default: // 30d
		since = today.AddDate(0, 0, -30)
	}

	limit := c.QueryInt("limit", 100)
	if limit > 500 {
		limit = 500
	}
	offset := c.QueryInt("offset", 0)
	name   := c.Query("name")

	stats, err := h.svc.GetEventStats(c.Context(), project.ID, since)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if stats == nil {
		stats = []analytics.EventStat{}
	}

	events, err := h.svc.GetEvents(c.Context(), project.ID, since, name, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if events == nil {
		events = []analytics.EventRow{}
	}

	return c.JSON(fiber.Map{
		"stats":  stats,
		"data":   events,
		"period": c.Query("period", "30d"),
	})
}

// GET /v1/dashboard/leads?limit=50&offset=0
func (h *AnalyticsHandler) GetLeads(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	limit := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)
	if limit > 200 {
		limit = 200
	}

	leads, err := h.svc.GetLeads(c.Context(), project.ID, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if leads == nil {
		leads = []analytics.LeadRow{}
	}

	stats, err := h.svc.GetLeadStats(c.Context(), project.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.JSON(fiber.Map{"data": leads, "stats": stats, "limit": limit, "offset": offset})
}

// POST /v1/dashboard/test-conversion
// Cria uma conversão de teste para verificar o pipeline completo (CAPI, TikTok, etc.).
// Body: { event_name, value, currency, email, phone, product_name, payment_method, platform }
func (h *AnalyticsHandler) TestConversion(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		EventName     string  `json:"event_name"`
		Value         float64 `json:"value"`
		Currency      string  `json:"currency"`
		Email         string  `json:"email"`
		Phone         string  `json:"phone"`
		ProductName   string  `json:"product_name"`
		PaymentMethod string  `json:"payment_method"`
		Platform      string  `json:"platform"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if body.EventName == "" {
		body.EventName = "Purchase"
	}
	if body.Currency == "" {
		body.Currency = "BRL"
	}
	if body.Platform == "" {
		body.Platform = "test"
	}

	emailHash := ""
	if body.Email != "" {
		h256 := sha256.Sum256([]byte(body.Email))
		emailHash = fmt.Sprintf("%x", h256[:])
	}
	phoneHash := ""
	if body.Phone != "" {
		h256 := sha256.Sum256([]byte(body.Phone))
		phoneHash = fmt.Sprintf("%x", h256[:])
	}

	extID := fmt.Sprintf("test_%d", time.Now().UnixMilli())
	var convID uuid.UUID
	err := h.db.QueryRow(c.Context(), `
		INSERT INTO conversions
			(project_id, external_id, event_name, value, currency,
			 email_hash, phone_hash, platform, product_name, payment_method, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,''),'approved')
		RETURNING id`,
		project.ID, extID, body.EventName, body.Value, body.Currency,
		emailHash, phoneHash, body.Platform,
		body.ProductName, body.PaymentMethod,
	).Scan(&convID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	if h.convSvc != nil {
		conv := &models.Conversion{
			ID:            convID,
			ProjectID:     project.ID,
			ExternalID:    extID,
			EventName:     body.EventName,
			Value:         body.Value,
			Currency:      body.Currency,
			EmailHash:     emailHash,
			PhoneHash:     phoneHash,
			Platform:      body.Platform,
			ProductName:   body.ProductName,
			PaymentMethod: body.PaymentMethod,
			Status:        "approved",
			CreatedAt:     time.Now(),
		}
		go h.convSvc.EnqueueIntegrations(c.Context(), conv)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"ok":         true,
		"id":         convID,
		"event_name": body.EventName,
		"value":      body.Value,
		"platform":   body.Platform,
	})
}

// GET /v1/dashboard/clone-violations?limit=50
// Retorna violações de domínio registradas (tentativas de clone detectadas).
func (h *AnalyticsHandler) GetCloneViolations(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	limit := c.QueryInt("limit", 50)
	if limit > 200 {
		limit = 200
	}

	type ViolationRow struct {
		ID            string `json:"id"`
		RequestDomain string `json:"request_domain"`
		IP            string `json:"ip"`
		UserAgent     string `json:"user_agent"`
		CreatedAt     string `json:"created_at"`
	}

	rows, err := h.db.Query(c.Context(), `
		SELECT id::text, request_domain, COALESCE(ip,''), COALESCE(user_agent,''), created_at
		FROM domain_violations
		WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2`,
		project.ID.String(), limit,
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	var violations []ViolationRow
	for rows.Next() {
		var v ViolationRow
		var createdAt time.Time
		if err := rows.Scan(&v.ID, &v.RequestDomain, &v.IP, &v.UserAgent, &createdAt); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		v.CreatedAt = createdAt.Format(time.RFC3339)
		violations = append(violations, v)
	}
	if err := rows.Err(); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if violations == nil {
		violations = []ViolationRow{}
	}

	// Conta total
	var total int
	_ = h.db.QueryRow(c.Context(),
		`SELECT COUNT(*) FROM domain_violations WHERE project_id = $1`,
		project.ID.String(),
	).Scan(&total)

	return c.JSON(fiber.Map{
		"data":             violations,
		"total":            total,
		"clone_protection": project.CloneProtection,
		"domain":           project.Domain,
	})
}

