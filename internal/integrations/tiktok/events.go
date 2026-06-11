package tiktok

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

const eventsURL = "https://business-api.tiktok.com/open_api/v1.3/event/track/"

type Client struct {
	accessToken string
	pixelCode   string
	httpClient  *http.Client
}

func NewClient(accessToken, pixelCode string) *Client {
	return &Client{
		accessToken: accessToken,
		pixelCode:   pixelCode,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

type Properties struct {
	Price    float64 `json:"price,omitempty"`
	Quantity int     `json:"quantity,omitempty"`
	Value    float64 `json:"value,omitempty"`
	Currency string  `json:"currency,omitempty"`
	OrderID  string  `json:"order_id,omitempty"`
}

type Context struct {
	UserAgent string   `json:"user_agent,omitempty"`
	IP        string   `json:"ip,omitempty"`
	Page      PageCtx  `json:"page,omitempty"`
	User      UserCtx  `json:"user,omitempty"`
}

type PageCtx struct {
	URL string `json:"url,omitempty"`
}

type UserCtx struct {
	EmailSHA256 string `json:"email,omitempty"` // SHA-256 hashed
	PhoneSHA256 string `json:"phone_number,omitempty"`
	TTClid      string `json:"ttclid,omitempty"`
}

type Event struct {
	EventName  string     `json:"event"`
	EventTime  int64      `json:"event_time"`
	EventID    string     `json:"event_id,omitempty"`
	Properties Properties `json:"properties,omitempty"`
	Context    Context    `json:"context"`
}

type payload struct {
	PixelCode string  `json:"pixel_code"`
	Events    []Event `json:"event"`
}

func (c *Client) Send(ctx context.Context, events []Event) error {
	p := payload{
		PixelCode: c.pixelCode,
		Events:    events,
	}

	body, err := json.Marshal(p)
	if err != nil {
		return err
	}

	// DEV MODE
	targetURL := eventsURL
	devHook := os.Getenv("DEV_WEBHOOK_URL")
	if devHook != "" {
		targetURL = devHook
		fmt.Printf("[DEV] tiktok → webhook: %s\n", devHook)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Access-Token", c.accessToken)
	if devHook != "" {
		req.Header.Set("X-ConvTrack-Platform", "tiktok")
		req.Header.Set("X-ConvTrack-Would-Send-To", eventsURL)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("tiktok events request: %w", err)
	}
	defer resp.Body.Close()

	if devHook != "" {
		return nil // webhook retorna 200 genérico — OK em dev
	}

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tiktok error %d: %s", resp.StatusCode, b)
	}
	return nil
}

func (c *Client) SendCompletePayment(ctx context.Context, eventID string, user UserCtx, value float64, currency, orderID, pageURL, ip, ua string) error {
	return c.Send(ctx, []Event{{
		EventName: "CompletePayment",
		EventTime: time.Now().Unix(),
		EventID:   eventID,
		Properties: Properties{
			Value:    value,
			Currency: currency,
			OrderID:  orderID,
		},
		Context: Context{
			UserAgent: ua,
			IP:        ip,
			Page:      PageCtx{URL: pageURL},
			User:      user,
		},
	}})
}

func (c *Client) SendSubmitForm(ctx context.Context, eventID string, user UserCtx, pageURL, ip, ua string) error {
	return c.Send(ctx, []Event{{
		EventName: "SubmitForm",
		EventTime: time.Now().Unix(),
		EventID:   eventID,
		Context: Context{
			UserAgent: ua,
			IP:        ip,
			Page:      PageCtx{URL: pageURL},
			User:      user,
		},
	}})
}
