package adsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	googleTokenURL  = "https://oauth2.googleapis.com/token"
	googleAdsAPIVer = "v17"
)

// googleAccessToken troca o refresh_token por um access_token de curta duração.
func googleAccessToken(ctx context.Context, clientID, clientSecret, refreshToken string) (string, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("google oauth: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("google oauth parse: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("google oauth: %s — %s", out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("google oauth: empty access_token")
	}
	return out.AccessToken, nil
}

type googleAdsRow struct {
	Campaign struct {
		Name string `json:"name"`
	} `json:"campaign"`
	Metrics struct {
		CostMicros string `json:"costMicros"`
	} `json:"metrics"`
	Segments struct {
		Date string `json:"date"`
	} `json:"segments"`
}

type googleAdsStreamResp []struct {
	Results []googleAdsRow `json:"results"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// SyncGoogle busca o gasto por campanha no Google Ads e faz upsert em ad_costs.
// Config esperada: developer_token, client_id, client_secret, refresh_token,
// customer_id (sem traços) e opcional login_customer_id.
func SyncGoogle(
	ctx context.Context,
	db *pgxpool.Pool,
	projectID uuid.UUID,
	cfg map[string]string,
	start, end time.Time,
) (int, error) {
	devToken := cfg["developer_token"]
	clientID := cfg["client_id"]
	clientSecret := cfg["client_secret"]
	refreshToken := cfg["refresh_token"]
	customerID := strings.ReplaceAll(cfg["customer_id"], "-", "")
	loginCustomerID := strings.ReplaceAll(cfg["login_customer_id"], "-", "")

	if devToken == "" || clientID == "" || clientSecret == "" || refreshToken == "" || customerID == "" {
		return 0, fmt.Errorf("google ads: developer_token, client_id, client_secret, refresh_token e customer_id são obrigatórios")
	}

	accessToken, err := googleAccessToken(ctx, clientID, clientSecret, refreshToken)
	if err != nil {
		return 0, err
	}

	gaql := fmt.Sprintf(
		`SELECT campaign.name, metrics.cost_micros, segments.date FROM campaign `+
			`WHERE segments.date BETWEEN '%s' AND '%s'`,
		start.Format("2006-01-02"), end.Format("2006-01-02"),
	)
	reqBody, _ := json.Marshal(map[string]string{"query": gaql})

	apiURL := fmt.Sprintf("https://googleads.googleapis.com/%s/customers/%s/googleAds:searchStream",
		googleAdsAPIVer, customerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("developer-token", devToken)
	req.Header.Set("Content-Type", "application/json")
	if loginCustomerID != "" {
		req.Header.Set("login-customer-id", loginCustomerID)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("google ads request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("google ads api (%d): %s", resp.StatusCode, truncate(string(body), 300))
	}

	var stream googleAdsStreamResp
	if err := json.Unmarshal(body, &stream); err != nil {
		return 0, fmt.Errorf("google ads parse: %w", err)
	}

	upserted := 0
	for _, chunk := range stream {
		if chunk.Error != nil {
			return upserted, fmt.Errorf("google ads error: %s", chunk.Error.Message)
		}
		for _, row := range chunk.Results {
			micros := parseInt64(row.Metrics.CostMicros)
			if micros <= 0 {
				continue
			}
			spend := float64(micros) / 1_000_000.0
			date, derr := time.Parse("2006-01-02", row.Segments.Date)
			if derr != nil {
				continue
			}
			_, eerr := db.Exec(ctx, `
				INSERT INTO ad_costs (project_id, date, platform, utm_source, utm_campaign, ad_account_id, amount, currency)
				VALUES ($1, $2, 'google', 'google', $3, $4, $5, 'BRL')
				ON CONFLICT (project_id, date, platform, utm_source, utm_campaign)
				DO UPDATE SET amount = EXCLUDED.amount, ad_account_id = EXCLUDED.ad_account_id`,
				projectID, date, row.Campaign.Name, customerID, spend,
			)
			if eerr == nil {
				upserted++
			}
		}
	}
	return upserted, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func parseInt64(s string) int64 {
	var v int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		v = v*10 + int64(r-'0')
	}
	return v
}
