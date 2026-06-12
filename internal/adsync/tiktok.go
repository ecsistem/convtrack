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

const tiktokReportURL = "https://business-api.tiktok.com/open_api/v1.3/report/integrated/get/"

type tiktokReportResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		List []struct {
			Dimensions struct {
				CampaignID  string `json:"campaign_id"`
				StatTimeDay string `json:"stat_time_day"`
			} `json:"dimensions"`
			Metrics struct {
				Spend        string `json:"spend"`
				CampaignName string `json:"campaign_name"`
			} `json:"metrics"`
		} `json:"list"`
	} `json:"data"`
}

// SyncTikTok busca o gasto por campanha na TikTok Marketing API e faz upsert
// em ad_costs. Config esperada: access_token, advertiser_id.
func SyncTikTok(
	ctx context.Context,
	db *pgxpool.Pool,
	projectID uuid.UUID,
	cfg map[string]string,
	start, end time.Time,
) (int, error) {
	accessToken := cfg["access_token"]
	advertiserID := cfg["advertiser_id"]
	if accessToken == "" || advertiserID == "" {
		return 0, fmt.Errorf("tiktok: access_token e advertiser_id são obrigatórios")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tiktokReportURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Access-Token", accessToken)

	dims, _ := json.Marshal([]string{"campaign_id", "stat_time_day"})
	metrics, _ := json.Marshal([]string{"spend", "campaign_name"})
	q := req.URL.Query()
	q.Set("advertiser_id", advertiserID)
	q.Set("report_type", "BASIC")
	q.Set("data_level", "AUCTION_CAMPAIGN")
	q.Set("dimensions", string(dims))
	q.Set("metrics", string(metrics))
	q.Set("start_date", start.Format("2006-01-02"))
	q.Set("end_date", end.Format("2006-01-02"))
	q.Set("page_size", "1000")
	req.URL.RawQuery = q.Encode()

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("tiktok request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result tiktokReportResp
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("tiktok parse: %w", err)
	}
	if result.Code != 0 {
		return 0, fmt.Errorf("tiktok api: %s", result.Message)
	}

	upserted := 0
	for _, row := range result.Data.List {
		spend, err := strconv.ParseFloat(row.Metrics.Spend, 64)
		if err != nil || spend <= 0 {
			continue
		}
		date, err := time.Parse("2006-01-02", row.Dimensions.StatTimeDay)
		if err != nil {
			// TikTok às vezes retorna "2024-01-02 00:00:00"
			date, err = time.Parse("2006-01-02 15:04:05", row.Dimensions.StatTimeDay)
			if err != nil {
				continue
			}
		}
		campaignName := row.Metrics.CampaignName
		if campaignName == "" {
			campaignName = row.Dimensions.CampaignID
		}
		_, eerr := db.Exec(ctx, `
			INSERT INTO ad_costs (project_id, date, platform, utm_source, utm_campaign, ad_account_id, amount, currency)
			VALUES ($1, $2, 'tiktok', 'tiktok', $3, $4, $5, 'BRL')
			ON CONFLICT (project_id, date, platform, utm_source, utm_campaign)
			DO UPDATE SET amount = EXCLUDED.amount, ad_account_id = EXCLUDED.ad_account_id`,
			projectID, date, campaignName, advertiserID, spend,
		)
		if eerr == nil {
			upserted++
		}
	}
	return upserted, nil
}
