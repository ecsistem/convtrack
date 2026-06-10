// Package analytics provides comprehensive funnel and financial metrics.
package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Types ────────────────────────────────────────────────────────────────────

type PeriodInfo struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type ApprovalMethodStats struct {
	Approved int     `json:"approved"`
	Total    int     `json:"total"`
	Rate     float64 `json:"rate"` // 0-100
}

type ApprovalRates struct {
	Cartao ApprovalMethodStats `json:"cartao"`
	Pix    ApprovalMethodStats `json:"pix"`
	Boleto ApprovalMethodStats `json:"boleto"`
}

type KPIs struct {
	Revenue            float64  `json:"revenue"`
	RevenueNet         float64  `json:"revenue_net"`
	RevenuePending     float64  `json:"revenue_pending"`
	RevenueRefunded    float64  `json:"revenue_refunded"`
	ProductCosts       float64  `json:"product_costs"`
	AdditionalExpenses float64  `json:"additional_expenses"`
	Taxes              float64  `json:"taxes"`
	AdSpend            float64  `json:"ad_spend"`
	Profit             float64  `json:"profit"`
	ROAS               *float64 `json:"roas"`
	ROI                *float64 `json:"roi"`
	Margin             *float64 `json:"margin"`
	ARPU               *float64 `json:"arpu"`
	CPA                *float64 `json:"cpa"`
	SalesApproved      int      `json:"sales_approved"`
	SalesPending       int      `json:"sales_pending"`
	SalesRefunded      int      `json:"sales_refunded"`
	SalesChargeback    int      `json:"sales_chargeback"`
	Leads              int      `json:"leads"`
	InitiateCheckouts  int      `json:"initiate_checkouts"`
	Pageviews          int      `json:"pageviews"`
	Clicks             int      `json:"clicks"`
	UniqueVisitors     int      `json:"unique_visitors"`
	ChargebackRate     float64  `json:"chargeback_rate"` // 0-100
}

type FunnelStep struct {
	Step         string   `json:"step"`
	Label        string   `json:"label"`
	Value        int      `json:"value"`
	Revenue      float64  `json:"revenue,omitempty"`
	RateFromPrev *float64 `json:"rate_from_prev"` // % do passo anterior
}

type HourlyStats struct {
	Hour    int     `json:"hour"`
	Sales   int     `json:"sales"`
	Revenue float64 `json:"revenue"`
	Profit  float64 `json:"profit"`
}

type PaymentStats struct {
	Method  string  `json:"method"`
	Label   string  `json:"label"`
	Count   int     `json:"count"`
	Revenue float64 `json:"revenue"`
	Pct     float64 `json:"pct"` // % do total
}

type ProductStats struct {
	Product     string  `json:"product"`
	Sales       int     `json:"sales"`
	Revenue     float64 `json:"revenue"`
	ProductCost float64 `json:"product_cost"`
	Profit      float64 `json:"profit"`
}

type SourceStats struct {
	Source   string  `json:"source"`
	Medium   string  `json:"medium"`
	Campaign string  `json:"campaign"`
	Sales    int     `json:"sales"`
	Revenue  float64 `json:"revenue"`
	Leads    int     `json:"leads"`
}

// CampaignStat is the full UTMify-style per-campaign breakdown.
type CampaignStat struct {
	Source   string   `json:"source"`
	Medium   string   `json:"medium"`
	Campaign string   `json:"campaign"`
	Sessions int      `json:"sessions"`
	Leads    int      `json:"leads"`
	Sales    int      `json:"sales"`
	Revenue  float64  `json:"revenue"`
	AdSpend  float64  `json:"ad_spend"`
	ROAS     *float64 `json:"roas"`
	CPA      *float64 `json:"cpa"`
	ROI      *float64 `json:"roi"`
}

type Analytics struct {
	Period        PeriodInfo     `json:"period"`
	KPIs          KPIs           `json:"kpis"`
	ApprovalRates ApprovalRates  `json:"approval_rates"`
	Funnel        []FunnelStep   `json:"funnel"`
	Hourly        []HourlyStats  `json:"hourly"`
	ByPayment     []PaymentStats `json:"by_payment"`
	ByProduct     []ProductStats `json:"by_product"`
	BySource      []SourceStats  `json:"by_source"`
}

// ─── Filters ─────────────────────────────────────────────────────────────────

type Filters struct {
	ProjectID uuid.UUID
	Start     time.Time
	End       time.Time
	Source    string // utm_source, "" = todos
	Platform  string // platform, "" = todos
	Product   string // product_name, "" = todos
}

// ─── Service ─────────────────────────────────────────────────────────────────

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// Get returns a fully computed Analytics snapshot for the given filters.
func (s *Service) Get(ctx context.Context, f Filters) (*Analytics, error) {
	a := &Analytics{
		Period: PeriodInfo{Start: f.Start, End: f.End},
	}

	if err := s.loadKPIs(ctx, f, a); err != nil {
		return nil, fmt.Errorf("loadKPIs: %w", err)
	}
	if err := s.loadHourly(ctx, f, a); err != nil {
		return nil, fmt.Errorf("loadHourly: %w", err)
	}
	if err := s.loadByPayment(ctx, f, a); err != nil {
		return nil, fmt.Errorf("loadByPayment: %w", err)
	}
	if err := s.loadByProduct(ctx, f, a); err != nil {
		return nil, fmt.Errorf("loadByProduct: %w", err)
	}
	if err := s.loadBySource(ctx, f, a); err != nil {
		return nil, fmt.Errorf("loadBySource: %w", err)
	}

	a.ApprovalRates = computeApprovalRates(a)
	a.Funnel = buildFunnel(a.KPIs)
	return a, nil
}

// ─── Settings helpers ─────────────────────────────────────────────────────────

func (s *Service) GetSettings(ctx context.Context, projectID uuid.UUID) (taxRate, monthlyExp, productCostDefault float64) {
	_ = s.db.QueryRow(ctx,
		`SELECT tax_rate, additional_expenses_monthly, product_cost_default
		 FROM project_settings WHERE project_id = $1`, projectID,
	).Scan(&taxRate, &monthlyExp, &productCostDefault)
	return
}

func (s *Service) GetAdSpend(ctx context.Context, f Filters) float64 {
	q := `SELECT COALESCE(SUM(amount),0) FROM ad_costs
	      WHERE project_id = $1 AND date >= $2::date AND date < $3::date`
	args := []interface{}{f.ProjectID, f.Start, f.End}
	idx := 3

	if f.Source != "" {
		idx++
		q += fmt.Sprintf(" AND utm_source = $%d", idx)
		args = append(args, f.Source)
	}
	if f.Platform != "" {
		idx++
		q += fmt.Sprintf(" AND platform = $%d", idx)
		args = append(args, f.Platform)
	}

	var total float64
	_ = s.db.QueryRow(ctx, q, args...).Scan(&total)
	return total
}

// ─── KPIs ─────────────────────────────────────────────────────────────────────

func (s *Service) loadKPIs(ctx context.Context, f Filters, a *Analytics) error {
	// Build the JOIN needed only when source filter is active
	joinClause, whereClause, args := buildConversionFilters(f, true)

	q := fmt.Sprintf(`
		SELECT
		  COALESCE(SUM(c.value) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'), 0),
		  COALESCE(SUM(c.value) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'pending'), 0),
		  COALESCE(SUM(c.value) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'refunded'), 0),
		  COUNT(*)       FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'),
		  COUNT(*)       FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'pending'),
		  COUNT(*)       FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'refunded'),
		  COUNT(*)       FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'chargeback'),
		  COUNT(*)       FILTER (WHERE c.event_name = 'Lead'),
		  COUNT(*)       FILTER (WHERE c.event_name = 'InitiateCheckout'),
		  COALESCE(SUM(COALESCE(c.product_cost,0)) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'), 0),
		  COUNT(DISTINCT c.session_id) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'),
		  COUNT(*)       FILTER (WHERE c.payment_method = 'cartao' AND c.event_name = 'Purchase' AND c.status = 'approved'),
		  COUNT(*)       FILTER (WHERE c.payment_method = 'cartao' AND c.event_name = 'Purchase'),
		  COUNT(*)       FILTER (WHERE c.payment_method = 'pix'    AND c.event_name = 'Purchase' AND c.status = 'approved'),
		  COUNT(*)       FILTER (WHERE c.payment_method = 'pix'    AND c.event_name = 'Purchase'),
		  COUNT(*)       FILTER (WHERE c.payment_method = 'boleto' AND c.event_name = 'Purchase' AND c.status = 'approved'),
		  COUNT(*)       FILTER (WHERE c.payment_method = 'boleto' AND c.event_name = 'Purchase')
		FROM conversions c
		%s
		WHERE %s`, joinClause, whereClause)

	row := s.db.QueryRow(ctx, q, args...)
	k := &a.KPIs

	var approvedCard, totalCard, approvedPix, totalPix, approvedBoleto, totalBoleto int

	if err := row.Scan(
		&k.Revenue, &k.RevenuePending, &k.RevenueRefunded,
		&k.SalesApproved, &k.SalesPending, &k.SalesRefunded, &k.SalesChargeback,
		&k.Leads, &k.InitiateCheckouts,
		&k.ProductCosts,
		&k.UniqueVisitors,
		&approvedCard, &totalCard,
		&approvedPix, &totalPix,
		&approvedBoleto, &totalBoleto,
	); err != nil {
		return err
	}

	// Event counts (pageviews, clicks)
	evQ, evArgs := buildEventFilters(f)
	_ = s.db.QueryRow(ctx, evQ, evArgs...).Scan(&k.Pageviews, &k.Clicks)

	// Financial settings + ad spend
	taxRate, monthlyExp, _ := s.GetSettings(ctx, f.ProjectID)
	k.AdSpend = s.GetAdSpend(ctx, f)

	// Compute derived metrics
	k.Taxes = k.Revenue * taxRate
	k.AdditionalExpenses = prorateDailyExpenses(monthlyExp, f.Start, f.End)
	k.RevenueNet = k.Revenue - k.Taxes - k.RevenueRefunded
	totalCosts := k.ProductCosts + k.AdditionalExpenses + k.Taxes + k.AdSpend
	k.Profit = k.Revenue - totalCosts

	if k.AdSpend > 0 {
		v := k.Revenue / k.AdSpend
		k.ROAS = &v
		if totalCosts > 0 {
			roi := (k.Revenue - totalCosts) / totalCosts * 100
			k.ROI = &roi
		}
		cpa := k.AdSpend / float64(max(k.SalesApproved, 1))
		k.CPA = &cpa
	}
	if k.Revenue > 0 {
		margin := k.Profit / k.Revenue * 100
		k.Margin = &margin
	}
	if k.UniqueVisitors > 0 {
		arpu := k.Revenue / float64(k.UniqueVisitors)
		k.ARPU = &arpu
	}

	salesTotal := k.SalesApproved + k.SalesPending + k.SalesRefunded + k.SalesChargeback
	if salesTotal > 0 {
		k.ChargebackRate = float64(k.SalesChargeback) / float64(salesTotal) * 100
	}

	// Approval rates (stored temporarily for use in computeApprovalRates)
	a.ApprovalRates = ApprovalRates{
		Cartao: newApproval(approvedCard, totalCard),
		Pix:    newApproval(approvedPix, totalPix),
		Boleto: newApproval(approvedBoleto, totalBoleto),
	}
	return nil
}

// ─── Hourly ───────────────────────────────────────────────────────────────────

func (s *Service) loadHourly(ctx context.Context, f Filters, a *Analytics) error {
	joinClause, whereClause, args := buildConversionFilters(f, false)
	whereClause += " AND c.event_name = 'Purchase' AND c.status = 'approved'"

	q := fmt.Sprintf(`
		SELECT EXTRACT(HOUR FROM c.created_at AT TIME ZONE 'America/Sao_Paulo')::int AS h,
		       COUNT(*),
		       COALESCE(SUM(c.value),0),
		       COALESCE(SUM(c.value - COALESCE(c.product_cost,0)),0)
		FROM conversions c
		%s
		WHERE %s
		GROUP BY h ORDER BY h`, joinClause, whereClause)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Initialise all 24 hours
	hourMap := make(map[int]*HourlyStats)
	for i := 0; i < 24; i++ {
		hourMap[i] = &HourlyStats{Hour: i}
	}

	for rows.Next() {
		var h int
		var hs HourlyStats
		if err := rows.Scan(&h, &hs.Sales, &hs.Revenue, &hs.Profit); err != nil {
			return err
		}
		hs.Hour = h
		hourMap[h] = &hs
	}

	a.Hourly = make([]HourlyStats, 24)
	for i := 0; i < 24; i++ {
		a.Hourly[i] = *hourMap[i]
	}
	return nil
}

// ─── By Payment ───────────────────────────────────────────────────────────────

func (s *Service) loadByPayment(ctx context.Context, f Filters, a *Analytics) error {
	joinClause, whereClause, args := buildConversionFilters(f, false)
	whereClause += " AND c.event_name = 'Purchase' AND c.status = 'approved'"

	q := fmt.Sprintf(`
		SELECT COALESCE(c.payment_method, 'outros') AS m,
		       COUNT(*),
		       COALESCE(SUM(c.value),0)
		FROM conversions c
		%s
		WHERE %s
		GROUP BY m ORDER BY COUNT(*) DESC`, joinClause, whereClause)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var items []PaymentStats
	var total int
	for rows.Next() {
		var ps PaymentStats
		if err := rows.Scan(&ps.Method, &ps.Count, &ps.Revenue); err != nil {
			return err
		}
		ps.Label = paymentLabel(ps.Method)
		items = append(items, ps)
		total += ps.Count
	}

	for i := range items {
		if total > 0 {
			items[i].Pct = float64(items[i].Count) / float64(total) * 100
		}
	}
	a.ByPayment = items
	return nil
}

// ─── By Product ───────────────────────────────────────────────────────────────

func (s *Service) loadByProduct(ctx context.Context, f Filters, a *Analytics) error {
	joinClause, whereClause, args := buildConversionFilters(f, false)
	whereClause += " AND c.event_name = 'Purchase' AND c.status = 'approved'"

	q := fmt.Sprintf(`
		SELECT COALESCE(NULLIF(c.product_name,''), '(sem produto)') AS p,
		       COUNT(*),
		       COALESCE(SUM(c.value),0),
		       COALESCE(SUM(COALESCE(c.product_cost,0)),0)
		FROM conversions c
		%s
		WHERE %s
		GROUP BY p ORDER BY SUM(c.value) DESC
		LIMIT 20`, joinClause, whereClause)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ps ProductStats
		if err := rows.Scan(&ps.Product, &ps.Sales, &ps.Revenue, &ps.ProductCost); err != nil {
			return err
		}
		ps.Profit = ps.Revenue - ps.ProductCost
		a.ByProduct = append(a.ByProduct, ps)
	}
	return nil
}

// ─── By UTM Source ────────────────────────────────────────────────────────────

func (s *Service) loadBySource(ctx context.Context, f Filters, a *Analytics) error {
	// Always join attributions for source breakdown
	_, whereClause, args := buildConversionFilters(f, false)

	q := fmt.Sprintf(`
		SELECT
		  COALESCE(NULLIF(a.utm_source,''),  '(direto)')   AS src,
		  COALESCE(NULLIF(a.utm_medium,''),  '(none)')     AS med,
		  COALESCE(NULLIF(a.utm_campaign,''),'(sem campanha)') AS camp,
		  COUNT(*)      FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'),
		  COALESCE(SUM(c.value) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'), 0),
		  COUNT(*)      FILTER (WHERE c.event_name = 'Lead')
		FROM conversions c
		LEFT JOIN attributions a ON a.session_id = c.session_id AND a.project_id = c.project_id
		WHERE %s
		GROUP BY src, med, camp
		ORDER BY SUM(c.value) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved') DESC NULLS LAST
		LIMIT 20`, whereClause)

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ss SourceStats
		if err := rows.Scan(&ss.Source, &ss.Medium, &ss.Campaign, &ss.Sales, &ss.Revenue, &ss.Leads); err != nil {
			return err
		}
		a.BySource = append(a.BySource, ss)
	}
	return nil
}

// ─── Settings CRUD ────────────────────────────────────────────────────────────

func (s *Service) UpsertSettings(ctx context.Context, projectID uuid.UUID, taxRate, monthlyExp, productCostDefault float64) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO project_settings (project_id, tax_rate, additional_expenses_monthly, product_cost_default, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (project_id) DO UPDATE SET
		  tax_rate                    = EXCLUDED.tax_rate,
		  additional_expenses_monthly = EXCLUDED.additional_expenses_monthly,
		  product_cost_default        = EXCLUDED.product_cost_default,
		  updated_at                  = NOW()`,
		projectID, taxRate, monthlyExp, productCostDefault,
	)
	return err
}

func (s *Service) ListAdCosts(ctx context.Context, projectID uuid.UUID, start, end time.Time) ([]AdCostRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, date, COALESCE(platform,''), COALESCE(utm_source,''), COALESCE(utm_campaign,''),
		       COALESCE(ad_account_id,''), amount, currency
		FROM ad_costs
		WHERE project_id = $1 AND date >= $2::date AND date <= $3::date
		ORDER BY date DESC`, projectID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdCostRow
	for rows.Next() {
		var r AdCostRow
		if err := rows.Scan(&r.ID, &r.Date, &r.Platform, &r.UTMSource, &r.UTMCampaign, &r.AdAccountID, &r.Amount, &r.Currency); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *Service) AddAdCost(ctx context.Context, projectID uuid.UUID, r AdCostRow) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO ad_costs (project_id, date, platform, utm_source, utm_campaign, ad_account_id, amount, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		projectID, r.Date, r.Platform, r.UTMSource, r.UTMCampaign, r.AdAccountID, r.Amount, r.Currency,
	).Scan(&id)
	return id, err
}

func (s *Service) DeleteAdCost(ctx context.Context, projectID, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ad_costs WHERE id=$1 AND project_id=$2`, id, projectID)
	return err
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// AdCostRow is the data shape for ad cost entries (list + create).
type AdCostRow struct {
	ID          uuid.UUID `json:"id"`
	Date        time.Time `json:"date"`
	Platform    string    `json:"platform"`
	UTMSource   string    `json:"utm_source"`
	UTMCampaign string    `json:"utm_campaign"`
	AdAccountID string    `json:"ad_account_id"`
	Amount      float64   `json:"amount"`
	Currency    string    `json:"currency"`
}

// GetCampaigns returns per-UTM-campaign metrics: sessions, leads, sales, revenue,
// ad spend (joined from ad_costs), and computed ROAS / CPA / ROI.
func (s *Service) GetCampaigns(ctx context.Context, projectID uuid.UUID, start, end time.Time) ([]CampaignStat, error) {
	rows, err := s.db.Query(ctx, `
		WITH session_base AS (
			SELECT
				COALESCE(NULLIF(a.utm_source,''),   '(direto)')        AS source,
				COALESCE(NULLIF(a.utm_medium,''),   '(none)')          AS medium,
				COALESCE(NULLIF(a.utm_campaign,''), '(sem campanha)')  AS campaign,
				COUNT(DISTINCT s.id)                                    AS sessions,
				COUNT(DISTINCT c.id) FILTER (WHERE c.event_name = 'Lead')                              AS leads,
				COUNT(DISTINCT c.id) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved') AS sales,
				COALESCE(SUM(c.value) FILTER (WHERE c.event_name = 'Purchase' AND c.status = 'approved'), 0) AS revenue
			FROM sessions s
			LEFT JOIN attributions a  ON a.session_id  = s.id AND a.project_id = s.project_id
			LEFT JOIN conversions  c  ON c.session_id  = s.id AND c.project_id = s.project_id
			WHERE s.project_id = $1 AND s.started_at >= $2 AND s.started_at < $3
			GROUP BY source, medium, campaign
		),
		spend_base AS (
			SELECT
				COALESCE(NULLIF(utm_source,''),   '(direto)')        AS source,
				COALESCE(NULLIF(utm_campaign,''), '(sem campanha)')  AS campaign,
				SUM(amount) AS ad_spend
			FROM ad_costs
			WHERE project_id = $1 AND date >= $2::date AND date < $3::date
			GROUP BY source, campaign
		)
		SELECT
			sb.source, sb.medium, sb.campaign,
			sb.sessions, sb.leads, sb.sales, sb.revenue,
			COALESCE(sp.ad_spend, 0) AS ad_spend
		FROM session_base sb
		LEFT JOIN spend_base sp ON sp.source = sb.source AND sp.campaign = sb.campaign
		ORDER BY sb.revenue DESC, sb.sessions DESC
		LIMIT 200`,
		projectID, start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CampaignStat
	for rows.Next() {
		var cs CampaignStat
		if err := rows.Scan(
			&cs.Source, &cs.Medium, &cs.Campaign,
			&cs.Sessions, &cs.Leads, &cs.Sales, &cs.Revenue, &cs.AdSpend,
		); err != nil {
			return nil, err
		}
		// Computed metrics
		if cs.AdSpend > 0 {
			roas := cs.Revenue / cs.AdSpend
			cs.ROAS = &roas
			roi := (cs.Revenue - cs.AdSpend) / cs.AdSpend * 100
			cs.ROI = &roi
		}
		if cs.Sales > 0 && cs.AdSpend > 0 {
			cpa := cs.AdSpend / float64(cs.Sales)
			cs.CPA = &cpa
		}
		out = append(out, cs)
	}
	if out == nil {
		out = []CampaignStat{}
	}
	return out, nil
}

// buildConversionFilters returns (joinClause, whereClause, args).
// withAttrJoin=true forces LEFT JOIN attributions (needed when filtering by source).
func buildConversionFilters(f Filters, withAttrJoin bool) (string, string, []interface{}) {
	args := []interface{}{f.ProjectID, f.Start, f.End}
	where := []string{"c.project_id = $1", "c.created_at >= $2", "c.created_at < $3"}
	idx := 3

	needsJoin := withAttrJoin
	if f.Source != "" {
		idx++
		where = append(where, fmt.Sprintf("a.utm_source = $%d", idx))
		args = append(args, f.Source)
		needsJoin = true
	}
	if f.Platform != "" {
		idx++
		where = append(where, fmt.Sprintf("c.platform = $%d", idx))
		args = append(args, f.Platform)
	}
	if f.Product != "" {
		idx++
		where = append(where, fmt.Sprintf("c.product_name = $%d", idx))
		args = append(args, f.Product)
	}

	join := ""
	if needsJoin {
		join = "LEFT JOIN attributions a ON a.session_id = c.session_id AND a.project_id = c.project_id"
	}
	return join, strings.Join(where, " AND "), args
}

func buildEventFilters(f Filters) (string, []interface{}) {
	q := `SELECT
	        COUNT(*) FILTER (WHERE name = 'pageview'),
	        COUNT(*) FILTER (WHERE name = 'click')
	      FROM events
	      WHERE project_id = $1 AND created_at >= $2 AND created_at < $3`
	args := []interface{}{f.ProjectID, f.Start, f.End}
	return q, args
}

func buildFunnel(k KPIs) []FunnelStep {
	steps := []struct {
		step  string
		label string
		val   int
	}{
		{"clicks", "Cliques", k.Clicks},
		{"pageviews", "Vis. Página", k.Pageviews},
		{"initiate_checkouts", "ICs", k.InitiateCheckouts},
		{"sales_pending", "Vendas Inic.", k.SalesPending + k.SalesApproved},
		{"sales_approved", "Vendas Apr.", k.SalesApproved},
	}

	funnel := make([]FunnelStep, len(steps))
	for i, s := range steps {
		fs := FunnelStep{Step: s.step, Label: s.label, Value: s.val}
		if i > 0 && steps[i-1].val > 0 {
			rate := float64(s.val) / float64(steps[i-1].val) * 100
			fs.RateFromPrev = &rate
		}
		funnel[i] = fs
	}
	return funnel
}

func computeApprovalRates(a *Analytics) ApprovalRates {
	// Already computed during loadKPIs, just return
	return a.ApprovalRates
}

func newApproval(approved, total int) ApprovalMethodStats {
	s := ApprovalMethodStats{Approved: approved, Total: total}
	if total > 0 {
		s.Rate = float64(approved) / float64(total) * 100
	}
	return s
}

func paymentLabel(method string) string {
	switch method {
	case "cartao":
		return "Cartão"
	case "pix":
		return "PIX"
	case "boleto":
		return "Boleto"
	default:
		return "Outros"
	}
}

// ─── Leads ───────────────────────────────────────────────────────────────────

type LeadRow struct {
	ID            string  `json:"id"`
	CreatedAt     string  `json:"created_at"`
	EventName     string  `json:"event_name"`
	Value         float64 `json:"value"`
	Currency      string  `json:"currency"`
	Platform      string  `json:"platform"`
	Status        string  `json:"status"`
	EmailHash     string  `json:"email_hash"`
	PhoneHash     string  `json:"phone_hash"`
	Attributed    bool    `json:"attributed"`
	UTMSource     string  `json:"utm_source"`
	UTMMedium     string  `json:"utm_medium"`
	UTMCampaign   string  `json:"utm_campaign"`
	UTMContent    string  `json:"utm_content"`
	LandingPage   string  `json:"landing_page"`
	Device        string  `json:"device"`
	Browser       string  `json:"browser"`
	Country       string  `json:"country"`
	City          string  `json:"city"`
	SessionID     string  `json:"session_id"`
}

type LeadStats struct {
	Total         int     `json:"total"`
	Today         int     `json:"today"`
	Week          int     `json:"week"`
	Month         int     `json:"month"`
	ConvRate      float64 `json:"conv_rate"`      // leads / sessions (30d)
	AvgPerDay     float64 `json:"avg_per_day"`    // month / 30
}

// GetLeads retorna todas as conversões do projeto com info de sessão e UTM (CRM view).
func (s *Service) GetLeads(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]LeadRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
			c.id, c.created_at, c.event_name, COALESCE(c.value,0),
			c.currency, COALESCE(c.platform,''), c.status,
			COALESCE(c.email_hash,''), COALESCE(c.phone_hash,''),
			c.attributed,
			COALESCE(a.utm_source,''), COALESCE(a.utm_medium,''),
			COALESCE(a.utm_campaign,''), COALESCE(a.utm_content,''),
			COALESCE(s.landing_page,''), COALESCE(s.device,''),
			COALESCE(s.browser,''), COALESCE(s.country,''), COALESCE(s.city,''),
			COALESCE(c.session_id::text,'')
		FROM conversions c
		LEFT JOIN attributions a ON a.session_id = c.session_id
		LEFT JOIN sessions s ON s.id = c.session_id
		WHERE c.project_id = $1
		ORDER BY c.created_at DESC
		LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []LeadRow
	for rows.Next() {
		var r LeadRow
		var createdAt time.Time
		if err := rows.Scan(
			&r.ID, &createdAt, &r.EventName, &r.Value,
			&r.Currency, &r.Platform, &r.Status,
			&r.EmailHash, &r.PhoneHash, &r.Attributed,
			&r.UTMSource, &r.UTMMedium, &r.UTMCampaign, &r.UTMContent,
			&r.LandingPage, &r.Device, &r.Browser, &r.Country, &r.City,
			&r.SessionID,
		); err != nil {
			return nil, fmt.Errorf("GetLeads scan: %w", err)
		}
		r.CreatedAt = createdAt.Format(time.RFC3339)
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetLeads rows: %w", err)
	}
	return list, nil
}

// GetLeadStats retorna métricas resumidas de conversões (visão CRM).
func (s *Service) GetLeadStats(ctx context.Context, projectID uuid.UUID) (LeadStats, error) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var st LeadStats
	err := s.db.QueryRow(ctx, `
		WITH leads AS (
			SELECT created_at FROM conversions
			WHERE project_id = $1
		),
		sessions_30d AS (
			SELECT COUNT(*) AS cnt FROM sessions
			WHERE project_id = $1 AND started_at >= $2
		)
		SELECT
			(SELECT COUNT(*) FROM leads),
			(SELECT COUNT(*) FROM leads WHERE created_at >= $3),
			(SELECT COUNT(*) FROM leads WHERE created_at >= $4),
			(SELECT COUNT(*) FROM leads WHERE created_at >= $2),
			(SELECT cnt FROM sessions_30d)`,
		projectID,
		today.AddDate(0, 0, -30),     // $2 month ago
		today,                         // $3 today
		today.AddDate(0, 0, -7),       // $4 week ago
	).Scan(&st.Total, &st.Today, &st.Week, &st.Month, new(int))
	if err != nil {
		return st, err
	}

	var sessions30d int
	_ = s.db.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE project_id=$1 AND started_at >= $2`,
		projectID, today.AddDate(0, 0, -30),
	).Scan(&sessions30d)

	if sessions30d > 0 {
		st.ConvRate = float64(st.Month) / float64(sessions30d) * 100
	}
	if st.Month > 0 {
		st.AvgPerDay = float64(st.Month) / 30
	}
	return st, nil
}

// prorateDailyExpenses converts monthly expenses to the fraction covering the period.
func prorateDailyExpenses(monthly float64, start, end time.Time) float64 {
	if monthly == 0 {
		return 0
	}
	days := end.Sub(start).Hours() / 24
	return monthly / 30 * days
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─── Events ───────────────────────────────────────────────────────────────────

type EventStat struct {
	Name           string  `json:"name"`
	Count          int     `json:"count"`
	UniqueSessions int     `json:"unique_sessions"`
	LastSeen       string  `json:"last_seen"`
	PctOfTotal     float64 `json:"pct_of_total"` // relative to the most frequent event
}

type EventRow struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Properties map[string]interface{} `json:"properties"`
	CreatedAt  string                 `json:"created_at"`
	SessionID  string                 `json:"session_id"`
	Device     string                 `json:"device"`
	Browser    string                 `json:"browser"`
	Country    string                 `json:"country"`
	UTMSource  string                 `json:"utm_source"`
	UTMCampaign string               `json:"utm_campaign"`
}

// GetEventStats retorna contagem de eventos agrupada por nome, com % relativa ao maior.
func (s *Service) GetEventStats(ctx context.Context, projectID uuid.UUID, since time.Time) ([]EventStat, error) {
	rows, err := s.db.Query(ctx, `
		SELECT e.name,
		       COUNT(*) AS cnt,
		       COUNT(DISTINCT e.session_id) AS uniq,
		       MAX(e.created_at)::text AS last_seen
		FROM events e
		WHERE e.project_id = $1 AND e.created_at >= $2
		GROUP BY e.name
		ORDER BY cnt DESC`,
		projectID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []EventStat
	for rows.Next() {
		var ev EventStat
		if err := rows.Scan(&ev.Name, &ev.Count, &ev.UniqueSessions, &ev.LastSeen); err != nil {
			return nil, fmt.Errorf("GetEventStats scan: %w", err)
		}
		list = append(list, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetEventStats rows: %w", err)
	}

	// Calcular % relativa ao maior
	if len(list) > 0 {
		maxCount := list[0].Count
		for i := range list {
			if maxCount > 0 {
				list[i].PctOfTotal = float64(list[i].Count) / float64(maxCount) * 100
			}
		}
	}
	return list, nil
}

// GetEvents retorna eventos recentes com info de sessão e UTM.
func (s *Service) GetEvents(ctx context.Context, projectID uuid.UUID, since time.Time, name string, limit, offset int) ([]EventRow, error) {
	q := `
		SELECT e.id, e.name, COALESCE(e.properties,'{}'),
		       e.created_at::text, e.session_id::text,
		       COALESCE(s.device,''), COALESCE(s.browser,''), COALESCE(s.country,''),
		       COALESCE(a.utm_source,''), COALESCE(a.utm_campaign,'')
		FROM events e
		LEFT JOIN sessions s ON s.id = e.session_id
		LEFT JOIN attributions a ON a.session_id = e.session_id
		WHERE e.project_id = $1 AND e.created_at >= $2`
	args := []interface{}{projectID, since}
	if name != "" {
		q += ` AND e.name = $3 ORDER BY e.created_at DESC LIMIT $4 OFFSET $5`
		args = append(args, name, limit, offset)
	} else {
		q += ` ORDER BY e.created_at DESC LIMIT $3 OFFSET $4`
		args = append(args, limit, offset)
	}

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []EventRow
	for rows.Next() {
		var ev EventRow
		var propsJSON []byte
		if err := rows.Scan(
			&ev.ID, &ev.Name, &propsJSON, &ev.CreatedAt, &ev.SessionID,
			&ev.Device, &ev.Browser, &ev.Country, &ev.UTMSource, &ev.UTMCampaign,
		); err != nil {
			return nil, fmt.Errorf("GetEvents scan: %w", err)
		}
		_ = json.Unmarshal(propsJSON, &ev.Properties)
		if ev.Properties == nil {
			ev.Properties = map[string]interface{}{}
		}
		list = append(list, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetEvents rows: %w", err)
	}
	return list, nil
}
