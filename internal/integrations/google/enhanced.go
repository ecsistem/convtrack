package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Google Ads Enhanced Conversions via the Conversion Tracking API
const conversionURL = "https://www.googleapis.com/upload/dfareporting/v4/userprofiles/%s/conversions/batchinsert"

// For simpler integration, we use the Measurement Protocol for GA4
const ga4URL = "https://www.google-analytics.com/mp/collect"

type GA4Client struct {
	measurementID string
	apiSecret     string
	httpClient    *http.Client
}

func NewGA4Client(measurementID, apiSecret string) *GA4Client {
	return &GA4Client{
		measurementID: measurementID,
		apiSecret:     apiSecret,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

type UserProperties struct {
	EmailSHA256 string `json:"email_sha256,omitempty"`
	PhoneSHA256 string `json:"phone_sha256,omitempty"`
}

type GA4Event struct {
	Name   string                 `json:"name"`
	Params map[string]interface{} `json:"params"`
}

type GA4Payload struct {
	ClientID       string     `json:"client_id"`
	UserID         string     `json:"user_id,omitempty"`
	TimestampMicros int64     `json:"timestamp_micros,omitempty"`
	Events         []GA4Event `json:"events"`
}

func (c *GA4Client) SendPurchase(ctx context.Context, clientID, transactionID string, value float64, currency string, emailHash string) error {
	payload := GA4Payload{
		ClientID: clientID,
		Events: []GA4Event{
			{
				Name: "purchase",
				Params: map[string]interface{}{
					"transaction_id": transactionID,
					"value":          value,
					"currency":       currency,
					"email_hash":     emailHash,
				},
			},
		},
	}
	return c.send(ctx, payload)
}

func (c *GA4Client) SendLead(ctx context.Context, clientID, formID string, emailHash string) error {
	payload := GA4Payload{
		ClientID: clientID,
		Events: []GA4Event{
			{
				Name: "generate_lead",
				Params: map[string]interface{}{
					"form_id":    formID,
					"email_hash": emailHash,
				},
			},
		},
	}
	return c.send(ctx, payload)
}

func (c *GA4Client) send(ctx context.Context, p GA4Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}

	// DEV MODE
	targetURL := fmt.Sprintf("%s?measurement_id=%s&api_secret=%s", ga4URL, c.measurementID, c.apiSecret)
	devHook := os.Getenv("DEV_WEBHOOK_URL")
	if devHook != "" {
		targetURL = devHook
		fmt.Printf("[DEV] google/ga4 → webhook: %s\n", devHook)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if devHook != "" {
		req.Header.Set("X-ConvTrack-Platform", "google")
		req.Header.Set("X-ConvTrack-Would-Send-To", ga4URL)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ga4 mp request: %w", err)
	}
	defer resp.Body.Close()

	if devHook != "" {
		return nil // webhook retorna 200 genérico — OK em dev
	}

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ga4 error %d: %s", resp.StatusCode, b)
	}
	return nil
}
