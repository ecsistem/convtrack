package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const capiURL = "https://graph.facebook.com/v19.0/%s/events"

type Client struct {
	pixelID     string
	accessToken string
	testCode    string // FBTESTEVT123 for Meta test events
	httpClient  *http.Client
}

func NewClient(pixelID, accessToken, testCode string) *Client {
	return &Client{
		pixelID:     pixelID,
		accessToken: accessToken,
		testCode:    testCode,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

type UserData struct {
	Email    string `json:"em,omitempty"` // SHA-256 hashed
	Phone    string `json:"ph,omitempty"` // SHA-256 hashed
	FBP      string `json:"fbp,omitempty"`
	FBC      string `json:"fbc,omitempty"`
	ClientIP string `json:"client_ip_address,omitempty"`
	UA       string `json:"client_user_agent,omitempty"`
}

type CustomData struct {
	Value    float64 `json:"value,omitempty"`
	Currency string  `json:"currency,omitempty"`
	OrderID  string  `json:"order_id,omitempty"`
}

type Event struct {
	Name       string     `json:"event_name"`
	Time       int64      `json:"event_time"`
	SourceURL  string     `json:"event_source_url,omitempty"`
	ActionSource string   `json:"action_source"`
	UserData   UserData   `json:"user_data"`
	CustomData CustomData `json:"custom_data,omitempty"`
	EventID    string     `json:"event_id,omitempty"`
}

type payload struct {
	Data      []Event `json:"data"`
	TestCode  string  `json:"test_event_code,omitempty"`
}

type Response struct {
	EventsReceived int    `json:"events_received"`
	FbTraceID      string `json:"fbtrace_id"`
}

func (c *Client) Send(ctx context.Context, events []Event) (*Response, error) {
	p := payload{Data: events}
	if c.testCode != "" {
		p.TestCode = c.testCode
	}

	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf(capiURL+"?access_token=%s", c.pixelID, c.accessToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta capi request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("meta capi error %d: %s", resp.StatusCode, respBody)
	}

	var result Response
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) SendPurchase(ctx context.Context, eventID string, ud UserData, value float64, currency, orderID, sourceURL string) (*Response, error) {
	return c.Send(ctx, []Event{{
		Name:         "Purchase",
		Time:         time.Now().Unix(),
		SourceURL:    sourceURL,
		ActionSource: "website",
		EventID:      eventID,
		UserData:     ud,
		CustomData: CustomData{
			Value:    value,
			Currency: currency,
			OrderID:  orderID,
		},
	}})
}

func (c *Client) SendLead(ctx context.Context, eventID string, ud UserData, sourceURL string) (*Response, error) {
	return c.Send(ctx, []Event{{
		Name:         "Lead",
		Time:         time.Now().Unix(),
		SourceURL:    sourceURL,
		ActionSource: "website",
		EventID:      eventID,
		UserData:     ud,
	}})
}
