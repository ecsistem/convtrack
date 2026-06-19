// Package billing handles subscription lifecycle and PixUp payment integration.
package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pixupBase = "https://api.pixup.com.br/v1"

// PlanProduct maps plan names to PixUp product/offer IDs configured via env.
// Set PIXUP_PRODUCT_STARTER, PIXUP_PRODUCT_PRO, PIXUP_PRODUCT_AGENCY in env.
func planProduct(plan string) string {
	env := map[string]string{
		"starter": os.Getenv("PIXUP_PRODUCT_STARTER"),
		"pro":     os.Getenv("PIXUP_PRODUCT_PRO"),
		"agency":  os.Getenv("PIXUP_PRODUCT_AGENCY"),
	}
	return env[plan]
}

type Service struct {
	db        *pgxpool.Pool
	apiKey    string
	apiSecret string
}

func New(db *pgxpool.Pool) *Service {
	return &Service{
		db:        db,
		apiKey:    os.Getenv("PIXUP_API_KEY"),
		apiSecret: os.Getenv("PIXUP_WEBHOOK_SECRET"),
	}
}

// CheckoutURL creates a PixUp checkout link for the given plan and returns the URL.
func (s *Service) CheckoutURL(ctx context.Context, accountID uuid.UUID, plan, email, name string) (string, error) {
	productID := planProduct(plan)
	if productID == "" {
		return "", fmt.Errorf("plano %q não configurado (defina PIXUP_PRODUCT_%s)", plan, strings.ToUpper(plan))
	}

	body, _ := json.Marshal(map[string]any{
		"product_id": productID,
		"customer": map[string]string{
			"email": email,
			"name":  name,
		},
		"metadata": map[string]string{
			"account_id": accountID.String(),
			"plan":       plan,
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pixupBase+"/checkouts", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("pixup request: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		CheckoutURL string `json:"checkout_url"`
		Error       string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("pixup error %d: %s", resp.StatusCode, out.Error)
	}
	return out.CheckoutURL, nil
}

// PixupWebhookPayload is the generic event payload from PixUp.
type PixupWebhookPayload struct {
	Event       string `json:"event"`
	ExternalID  string `json:"id"`
	Status      string `json:"status"`
	Plan        string `json:"plan"`
	CustomerEmail string `json:"customer_email"`
	Metadata    struct {
		AccountID string `json:"account_id"`
		Plan      string `json:"plan"`
	} `json:"metadata"`
	ExpiresAt string `json:"expires_at"`
}

// ValidateSignature checks the HMAC-SHA256 signature sent by PixUp.
func (s *Service) ValidateSignature(body []byte, sig string) bool {
	if s.apiSecret == "" {
		return true // dev mode: skip validation
	}
	mac := hmac.New(sha256.New, []byte(s.apiSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// HandleWebhook processes a validated PixUp webhook event and updates the account.
func (s *Service) HandleWebhook(ctx context.Context, raw []byte) error {
	var p PixupWebhookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode webhook: %w", err)
	}

	accountIDStr := p.Metadata.AccountID
	plan := p.Metadata.Plan
	if accountIDStr == "" || plan == "" {
		return fmt.Errorf("missing metadata in webhook")
	}
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		return fmt.Errorf("invalid account_id: %w", err)
	}

	switch p.Event {
	case "payment.approved", "subscription.active":
		var periodEnd *time.Time
		if p.ExpiresAt != "" {
			t, _ := time.Parse(time.RFC3339, p.ExpiresAt)
			if !t.IsZero() {
				periodEnd = &t
			}
		}
		if periodEnd == nil {
			t := time.Now().AddDate(0, 1, 0)
			periodEnd = &t
		}
		// Update account plan + approve
		_, _ = s.db.Exec(ctx,
			`UPDATE accounts SET plan=$1, status='approved' WHERE id=$2`, plan, accountID)
		// Upsert subscription
		_, _ = s.db.Exec(ctx, `
			INSERT INTO subscriptions (account_id, plan, status, provider, external_id, current_period_end)
			VALUES ($1,$2,'active','pixup',$3,$4)
			ON CONFLICT (account_id) DO UPDATE
			  SET plan=$2, status='active', external_id=$3, current_period_end=$4, updated_at=NOW()`,
			accountID, plan, p.ExternalID, periodEnd)

		// Credit affiliate commission if applicable
		go s.creditAffiliateCommission(ctx, accountID, plan)

	case "payment.cancelled", "subscription.cancelled", "subscription.expired":
		_, _ = s.db.Exec(ctx,
			`UPDATE accounts SET plan='free' WHERE id=$1`, accountID)
		_, _ = s.db.Exec(ctx, `
			UPDATE subscriptions SET status='cancelled', updated_at=NOW()
			WHERE account_id=$1`, accountID)

	case "payment.refunded", "payment.chargeback":
		_, _ = s.db.Exec(ctx,
			`UPDATE accounts SET plan='free', status='suspended' WHERE id=$1`, accountID)
		_, _ = s.db.Exec(ctx, `DELETE FROM auth_tokens WHERE account_id=$1`, accountID)
	}

	return nil
}

// ReadBody reads and returns the full request body for signature validation.
func ReadBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}

// creditAffiliateCommission records a referral commission (best-effort).
func (s *Service) creditAffiliateCommission(ctx context.Context, accountID uuid.UUID, plan string) {
	// Find affiliate code used at registration
	var affiliateRef string
	_ = s.db.QueryRow(ctx, `SELECT affiliate_ref FROM accounts WHERE id=$1`, accountID).Scan(&affiliateRef)
	if affiliateRef == "" {
		return
	}

	// Plan prices for commission calculation
	prices := map[string]float64{"starter": 97, "pro": 197, "agency": 397}
	amount, ok := prices[plan]
	if !ok {
		return
	}

	// Find affiliate by code
	var affiliateID uuid.UUID
	var commissionPct int
	err := s.db.QueryRow(ctx,
		`SELECT id, commission_pct FROM affiliates WHERE code=$1 AND status='active'`, affiliateRef,
	).Scan(&affiliateID, &commissionPct)
	if err != nil {
		return
	}

	commission := amount * float64(commissionPct) / 100

	// Insert referral (ignore duplicate)
	_, _ = s.db.Exec(ctx, `
		INSERT INTO affiliate_referrals (affiliate_id, referred_account_id, plan, amount, commission)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT DO NOTHING`,
		affiliateID, accountID, plan, amount, commission)

	// Update affiliate total_earned
	_, _ = s.db.Exec(ctx,
		`UPDATE affiliates SET total_earned = total_earned + $1 WHERE id = $2`,
		commission, affiliateID)
}

// GetSubscription returns the active subscription for an account.
func (s *Service) GetSubscription(ctx context.Context, accountID uuid.UUID) (map[string]any, error) {
	var sub struct {
		ID          uuid.UUID  `db:"id"`
		Plan        string     `db:"plan"`
		Status      string     `db:"status"`
		Provider    string     `db:"provider"`
		PeriodEnd   *time.Time `db:"current_period_end"`
		CreatedAt   time.Time  `db:"created_at"`
	}
	err := s.db.QueryRow(ctx, `
		SELECT id, plan, status, provider, current_period_end, created_at
		FROM subscriptions WHERE account_id=$1
		ORDER BY created_at DESC LIMIT 1`, accountID,
	).Scan(&sub.ID, &sub.Plan, &sub.Status, &sub.Provider, &sub.PeriodEnd, &sub.CreatedAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":                  sub.ID,
		"plan":                sub.Plan,
		"status":              sub.Status,
		"provider":            sub.Provider,
		"current_period_end":  sub.PeriodEnd,
		"created_at":          sub.CreatedAt,
	}, nil
}
