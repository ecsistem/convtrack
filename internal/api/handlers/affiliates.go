package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AffiliateHandler struct {
	db *pgxpool.Pool
}

func NewAffiliate(db *pgxpool.Pool) *AffiliateHandler {
	return &AffiliateHandler{db: db}
}

func newAffiliateCode() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) // 8 hex chars
}

// ── Admin routes ─────────────────────────────────────────────────────────────

// GET /v1/admin/affiliates
func (h *AffiliateHandler) AdminList(c *fiber.Ctx) error {
	search := c.Query("search", "")
	limit  := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)

	where := "WHERE 1=1"
	args := []any{}
	i := 1
	if search != "" {
		where += " AND (lower(a.name) LIKE lower($1) OR lower(a.email) LIKE lower($1))"
		args = append(args, "%"+search+"%")
		i++
	}
	args = append(args, limit, offset)

	rows, err := h.db.Query(c.Context(), `
		SELECT af.id, af.code, af.commission_pct, af.status,
		       af.total_earned, af.paid_out, af.created_at,
		       a.id AS account_id, a.name, a.email,
		       (SELECT COUNT(*) FROM affiliate_referrals WHERE affiliate_id=af.id) AS referrals
		FROM affiliates af
		JOIN accounts a ON a.id = af.account_id
		`+where+`
		ORDER BY af.created_at DESC
		LIMIT $`+itoa(i)+` OFFSET $`+itoa(i+1),
		args...,
	)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	type row struct {
		ID            uuid.UUID `json:"id"`
		Code          string    `json:"code"`
		CommissionPct int       `json:"commission_pct"`
		Status        string    `json:"status"`
		TotalEarned   float64   `json:"total_earned"`
		PaidOut       float64   `json:"paid_out"`
		Pending       float64   `json:"pending"`
		CreatedAt     time.Time `json:"created_at"`
		AccountID     uuid.UUID `json:"account_id"`
		Name          string    `json:"name"`
		Email         string    `json:"email"`
		Referrals     int       `json:"referrals"`
	}

	list := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(
			&r.ID, &r.Code, &r.CommissionPct, &r.Status,
			&r.TotalEarned, &r.PaidOut, &r.CreatedAt,
			&r.AccountID, &r.Name, &r.Email, &r.Referrals,
		); err != nil {
			continue
		}
		r.Pending = r.TotalEarned - r.PaidOut
		list = append(list, r)
	}

	var total int
	_ = h.db.QueryRow(c.Context(), `SELECT COUNT(*) FROM affiliates`).Scan(&total)

	return c.JSON(fiber.Map{"data": list, "total": total})
}

// POST /v1/admin/affiliates  — body: { account_id, commission_pct? }
func (h *AffiliateHandler) AdminCreate(c *fiber.Ctx) error {
	var body struct {
		AccountID     string `json:"account_id"`
		CommissionPct *int   `json:"commission_pct"`
	}
	if err := c.BodyParser(&body); err != nil || body.AccountID == "" {
		return c.Status(400).JSON(fiber.Map{"error": "account_id obrigatório"})
	}
	accountID, err := uuid.Parse(body.AccountID)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "account_id inválido"})
	}
	pct := 30
	if body.CommissionPct != nil {
		pct = *body.CommissionPct
	}
	code := newAffiliateCode()
	// Mark account as affiliate
	_, _ = h.db.Exec(c.Context(), `UPDATE accounts SET is_affiliate=true WHERE id=$1`, accountID)
	// Insert affiliate record
	_, err = h.db.Exec(c.Context(), `
		INSERT INTO affiliates (account_id, code, commission_pct)
		VALUES ($1,$2,$3)
		ON CONFLICT (account_id) DO UPDATE SET commission_pct=$3, status='active'`,
		accountID, code, pct)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(201).JSON(fiber.Map{"ok": true, "code": code})
}

// PUT /v1/admin/affiliates/:id
func (h *AffiliateHandler) AdminUpdate(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}
	var body struct {
		CommissionPct *int    `json:"commission_pct"`
		Status        *string `json:"status"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "json inválido"})
	}
	if body.CommissionPct != nil {
		_, _ = h.db.Exec(c.Context(), `UPDATE affiliates SET commission_pct=$1 WHERE id=$2`, *body.CommissionPct, id)
	}
	if body.Status != nil {
		_, _ = h.db.Exec(c.Context(), `UPDATE affiliates SET status=$1 WHERE id=$2`, *body.Status, id)
	}
	return c.JSON(fiber.Map{"ok": true})
}

// DELETE /v1/admin/affiliates/:id
func (h *AffiliateHandler) AdminDelete(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}
	// Remove affiliate flag from account
	_, _ = h.db.Exec(c.Context(), `
		UPDATE accounts SET is_affiliate=false
		WHERE id=(SELECT account_id FROM affiliates WHERE id=$1)`, id)
	_, _ = h.db.Exec(c.Context(), `DELETE FROM affiliates WHERE id=$1`, id)
	return c.JSON(fiber.Map{"ok": true})
}

// POST /v1/admin/affiliates/:id/payout  — mark all unpaid referrals as paid
func (h *AffiliateHandler) AdminPayout(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}
	var pending float64
	_ = h.db.QueryRow(c.Context(),
		`SELECT COALESCE(SUM(commission),0) FROM affiliate_referrals WHERE affiliate_id=$1 AND paid=false`, id,
	).Scan(&pending)
	_, _ = h.db.Exec(c.Context(),
		`UPDATE affiliate_referrals SET paid=true WHERE affiliate_id=$1 AND paid=false`, id)
	_, _ = h.db.Exec(c.Context(),
		`UPDATE affiliates SET paid_out=paid_out+$1 WHERE id=$2`, pending, id)
	return c.JSON(fiber.Map{"ok": true, "amount_paid": pending})
}

// ── Affiliate user routes ─────────────────────────────────────────────────────

// GET /v1/affiliate/me
func (h *AffiliateHandler) Me(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "unauthorized"})
	}

	var aff struct {
		ID            uuid.UUID `json:"id"`
		Code          string    `json:"code"`
		CommissionPct int       `json:"commission_pct"`
		TotalEarned   float64   `json:"total_earned"`
		PaidOut       float64   `json:"paid_out"`
	}
	err := h.db.QueryRow(c.Context(), `
		SELECT id, code, commission_pct, total_earned, paid_out
		FROM affiliates WHERE account_id=$1 AND status='active'`, accountID,
	).Scan(&aff.ID, &aff.Code, &aff.CommissionPct, &aff.TotalEarned, &aff.PaidOut)
	if err != nil {
		return c.Status(403).JSON(fiber.Map{"error": "não é um afiliado ativo"})
	}

	var referrals int
	_ = h.db.QueryRow(c.Context(),
		`SELECT COUNT(*) FROM affiliate_referrals WHERE affiliate_id=$1`, aff.ID,
	).Scan(&referrals)

	return c.JSON(fiber.Map{
		"id":             aff.ID,
		"code":           aff.Code,
		"commission_pct": aff.CommissionPct,
		"total_earned":   aff.TotalEarned,
		"paid_out":       aff.PaidOut,
		"pending":        aff.TotalEarned - aff.PaidOut,
		"referrals":      referrals,
	})
}

// GET /v1/affiliate/referrals
func (h *AffiliateHandler) Referrals(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"error": "unauthorized"})
	}

	var affiliateID uuid.UUID
	err := h.db.QueryRow(c.Context(),
		`SELECT id FROM affiliates WHERE account_id=$1`, accountID,
	).Scan(&affiliateID)
	if err != nil {
		return c.Status(403).JSON(fiber.Map{"error": "não é um afiliado"})
	}

	rows, err := h.db.Query(c.Context(), `
		SELECT ar.plan, ar.amount, ar.commission, ar.paid, ar.created_at
		FROM affiliate_referrals ar
		WHERE ar.affiliate_id=$1
		ORDER BY ar.created_at DESC
		LIMIT 100`, affiliateID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	type ref struct {
		Plan       string    `json:"plan"`
		Amount     float64   `json:"amount"`
		Commission float64   `json:"commission"`
		Paid       bool      `json:"paid"`
		CreatedAt  time.Time `json:"created_at"`
	}
	list := []ref{}
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.Plan, &r.Amount, &r.Commission, &r.Paid, &r.CreatedAt); err == nil {
			list = append(list, r)
		}
	}
	return c.JSON(fiber.Map{"data": list})
}
