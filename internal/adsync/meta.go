// Package adsync pulls campaign-level ad spend from ad platforms and upserts
// into the ad_costs table, enabling automatic ROAS / CPA calculation.
package adsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const metaInsightsURL = "https://graph.facebook.com/v19.0/%s/insights"

// MetaInsight is one row from the Meta Marketing API insights response.
type MetaInsight struct {
	CampaignName string `json:"campaign_name"`
	Spend        string `json:"spend"` // string in Meta API
	DateStart    string `json:"date_start"`
}

type metaInsightsResp struct {
	Data  []MetaInsight `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// SyncMeta fetches campaign spend from Meta Marketing API for [start, end]
// and upserts into ad_costs. adAccountID must include the "act_" prefix.
// Returns count of rows upserted.
func SyncMeta(
	ctx context.Context,
	db *pgxpool.Pool,
	projectID uuid.UUID,
	adAccountID, accessToken string,
	start, end time.Time,
) (int, error) {
	if adAccountID == "" || accessToken == "" {
		return 0, fmt.Errorf("ad_account_id and access_token are required")
	}

	url := fmt.Sprintf(metaInsightsURL, adAccountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	q := req.URL.Query()
	q.Set("fields", "campaign_name,spend,date_start")
	q.Set("level", "campaign")
	q.Set("time_increment", "1") // daily breakdown
	q.Set("time_range", fmt.Sprintf(`{"since":"%s","until":"%s"}`,
		start.Format("2006-01-02"), end.Format("2006-01-02")))
	q.Set("access_token", accessToken)
	q.Set("limit", "500")
	req.URL.RawQuery = q.Encode()

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("meta api request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result metaInsightsResp
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("meta api parse: %w", err)
	}
	if result.Error != nil {
		return 0, fmt.Errorf("meta api error: %s", result.Error.Message)
	}

	upserted := 0
	for _, row := range result.Data {
		spend, err := strconv.ParseFloat(row.Spend, 64)
		if err != nil || spend <= 0 {
			continue
		}
		date, err := time.Parse("2006-01-02", row.DateStart)
		if err != nil {
			continue
		}

		_, err = db.Exec(ctx, `
			INSERT INTO ad_costs (project_id, date, platform, utm_source, utm_campaign, ad_account_id, amount, currency)
			VALUES ($1, $2, 'meta', 'facebook', $3, $4, $5, 'BRL')
			ON CONFLICT (project_id, date, platform, utm_source, utm_campaign)
			DO UPDATE SET amount = EXCLUDED.amount, ad_account_id = EXCLUDED.ad_account_id`,
			projectID, date, row.CampaignName, adAccountID, spend,
		)
		if err == nil {
			upserted++
		}
	}
	return upserted, nil
}

// GetMetaConfig reads the active Meta integration config for a project.
// Returns accessToken, adAccountID, or an error if not configured / disabled.
func GetMetaConfig(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID) (accessToken, adAccountID string, err error) {
	var configRaw []byte
	err = db.QueryRow(ctx,
		`SELECT config FROM integration_settings
		 WHERE project_id = $1 AND platform = 'meta' AND enabled = true`,
		projectID,
	).Scan(&configRaw)
	if err != nil {
		return "", "", fmt.Errorf("meta integration not configured or disabled")
	}
	var cfg map[string]string
	if err := json.Unmarshal(configRaw, &cfg); err != nil {
		return "", "", err
	}
	return cfg["access_token"], cfg["ad_account_id"], nil
}
