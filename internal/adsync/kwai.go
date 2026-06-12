package adsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const kwaiReportURL = "https://developers.kwai.com/rest/n/mapi/report/dspCampaignEffectQuery"

type kwaiReportResp struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Details []struct {
			CampaignID   int64   `json:"campaignId"`
			CampaignName string  `json:"campaignName"`
			Cost         float64 `json:"cost"`   // gasto no dia
			StatDate     string  `json:"statDate"` // formato yyyyMMdd ou yyyy-MM-dd
		} `json:"details"`
	} `json:"data"`
}

// SyncKwai busca o gasto por campanha na Kwai Ads API e faz upsert em ad_costs.
// Config esperada: access_token, advertiser_id.
func SyncKwai(
	ctx context.Context,
	db *pgxpool.Pool,
	projectID uuid.UUID,
	cfg map[string]string,
	start, end time.Time,
) (int, error) {
	accessToken := cfg["access_token"]
	advertiserID := cfg["advertiser_id"]
	if accessToken == "" || advertiserID == "" {
		return 0, fmt.Errorf("kwai: access_token e advertiser_id são obrigatórios")
	}

	payload := map[string]interface{}{
		"advertiserId": advertiserID,
		"startDate":    start.Format("2006-01-02"),
		"endDate":      end.Format("2006-01-02"),
		"granularity":  2, // diário
		"timeZoneIana": "America/Sao_Paulo",
	}
	reqBody, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kwaiReportURL, bytes.NewReader(reqBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Access-Token", accessToken)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("kwai request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result kwaiReportResp
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("kwai parse: %w", err)
	}
	if result.Status != 200 && result.Status != 0 {
		return 0, fmt.Errorf("kwai api: %s", result.Message)
	}

	upserted := 0
	for _, row := range result.Data.Details {
		if row.Cost <= 0 {
			continue
		}
		date, err := parseKwaiDate(row.StatDate)
		if err != nil {
			continue
		}
		name := row.CampaignName
		if name == "" {
			name = fmt.Sprintf("%d", row.CampaignID)
		}
		_, eerr := db.Exec(ctx, `
			INSERT INTO ad_costs (project_id, date, platform, utm_source, utm_campaign, ad_account_id, amount, currency)
			VALUES ($1, $2, 'kwai', 'kwai', $3, $4, $5, 'BRL')
			ON CONFLICT (project_id, date, platform, utm_source, utm_campaign)
			DO UPDATE SET amount = EXCLUDED.amount, ad_account_id = EXCLUDED.ad_account_id`,
			projectID, date, name, advertiserID, row.Cost,
		)
		if eerr == nil {
			upserted++
		}
	}
	return upserted, nil
}

func parseKwaiDate(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("kwai: invalid date %q", s)
}
