package handlers

import (
	"time"

	"github.com/ecsistem/convtrack/internal/analytics"
	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type AnalyticsHandler struct {
	svc *analytics.Service
}

func NewAnalytics(svc *analytics.Service) *AnalyticsHandler {
	return &AnalyticsHandler{svc: svc}
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

