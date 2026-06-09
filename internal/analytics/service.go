// Package analytics provides comprehensive funnel and financial metrics.
package analytics

import (
	"context"
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
