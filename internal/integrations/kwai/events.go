// Package kwai implementa o cliente para a Kwai for Business Conversion API.
// Docs: https://developers.kwai.com/rest/n/mapi/event/track
package kwai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const eventsURL = "https://developers.kwai.com/rest/n/mapi/event/track"

// Tipos de evento suportados pela Kwai Conversion API
const (
	EventPurchase   = "PURCHASE"
	EventLead       = "LEAD"
	EventAddToCart  = "ADD_TO_CART"
	EventViewContent = "VIEW_CONTENT"
	EventSearch     = "SEARCH"
)

type Client struct {
	pixelID     string
	accessToken string
	httpClient  *http.Client
}

func NewClient(pixelID, accessToken string) *Client {
	return &Client{
		pixelID:     pixelID,
		accessToken: accessToken,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// UserData contém dados do usuário hasheados (SHA-256)
type UserData struct {
	// Emails hasheados em SHA-256 (pode enviar múltiplos)
	Emails []string `json:"em,omitempty"`
	// Telefones hasheados em SHA-256
	Phones []string `json:"ph,omitempty"`
	// Kwai Click ID capturado da URL (?kwclid=...)
	KWClid string `json:"kwclid,omitempty"`
	// IP do visitante
	IP string `json:"client_ip_address,omitempty"`
	// User-agent do visitante
	UA string `json:"client_user_agent,omitempty"`
}

type CustomData struct {
	Value    float64 `json:"value,omitempty"`
	Currency string  `json:"currency,omitempty"`
	OrderID  string  `json:"order_id,omitempty"`
}

type Event struct {
	EventType  string     `json:"event_type"`
	EventTime  int64      `json:"event_time"`
	EventID    string     `json:"event_id,omitempty"`
	UserData   UserData   `json:"user_data"`
	CustomData CustomData `json:"custom_data,omitempty"`
	PageURL    string     `json:"page_url,omitempty"`
}

type payload struct {
	PixelID string  `json:"pixel_id"`
	Events  []Event `json:"events"`
}

type apiResponse struct {
	Result  int    `json:"result"`
	Message string `json:"msg"`
}

func (c *Client) Send(ctx context.Context, events []Event) error {
	p := payload{
		PixelID: c.pixelID,
		Events:  events,
	}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("kwai: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, eventsURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("kwai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Access-Token", c.accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kwai: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("kwai: status %d — %s", resp.StatusCode, respBody)
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("kwai: parse response: %w", err)
	}
	// result=1 significa sucesso na API do Kwai
	if apiResp.Result != 1 {
		return fmt.Errorf("kwai: api error — %s", apiResp.Message)
	}

	return nil
}

// SendPurchase dispara o evento PURCHASE (conversão de venda)
func (c *Client) SendPurchase(
	ctx context.Context,
	eventID string,
	ud UserData,
	value float64,
	currency, orderID, pageURL string,
) error {
	return c.Send(ctx, []Event{{
		EventType: EventPurchase,
		EventTime: time.Now().Unix(),
		EventID:   eventID,
		UserData:  ud,
		CustomData: CustomData{
			Value:    value,
			Currency: currency,
			OrderID:  orderID,
		},
		PageURL: pageURL,
	}})
}

// SendLead dispara o evento LEAD (formulário de captação)
func (c *Client) SendLead(
	ctx context.Context,
	eventID string,
	ud UserData,
	pageURL string,
) error {
	return c.Send(ctx, []Event{{
		EventType: EventLead,
		EventTime: time.Now().Unix(),
		EventID:   eventID,
		UserData:  ud,
		PageURL:   pageURL,
	}})
}
