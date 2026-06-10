package models

import (
	"time"

	"github.com/google/uuid"
)

type Account struct {
	ID             uuid.UUID `json:"id" db:"id"`
	Name           string    `json:"name" db:"name"`
	Email          string    `json:"email" db:"email"`
	Plan           string    `json:"plan" db:"plan"`
	SessionsQuota  int       `json:"sessions_quota" db:"sessions_quota"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	// Auth fields (migration 007) — never included in JSON API responses
	PasswordHash   string    `json:"-" db:"password_hash"`
}

type Project struct {
	ID              uuid.UUID `json:"id" db:"id"`
	AccountID       uuid.UUID `json:"account_id" db:"account_id"`
	Name            string    `json:"name" db:"name"`
	Domain          string    `json:"domain" db:"domain"`
	APIKey          string    `json:"api_key" db:"api_key"`
	CloneProtection bool      `json:"clone_protection" db:"clone_protection"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
}

type Visitor struct {
	ID          uuid.UUID `json:"id" db:"id"`
	ProjectID   uuid.UUID `json:"project_id" db:"project_id"`
	Fingerprint string    `json:"fingerprint" db:"fingerprint"`
	FirstSeen   time.Time `json:"first_seen" db:"first_seen"`
	LastSeen    time.Time `json:"last_seen" db:"last_seen"`
}

type Session struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	VisitorID       uuid.UUID  `json:"visitor_id" db:"visitor_id"`
	ProjectID       uuid.UUID  `json:"project_id" db:"project_id"`
	StartedAt       time.Time  `json:"started_at" db:"started_at"`
	LastActivity    time.Time  `json:"last_activity" db:"last_activity"`
	LandingPage     string     `json:"landing_page" db:"landing_page"`
	Referrer        string     `json:"referrer" db:"referrer"`
	UserAgent       string     `json:"user_agent" db:"user_agent"`
	IP              string     `json:"ip" db:"ip"`
	Country         string     `json:"country" db:"country"`
	City            string     `json:"city" db:"city"`
	Device          string     `json:"device" db:"device"`
	Browser         string     `json:"browser" db:"browser"`
	OS              string     `json:"os" db:"os"`
	// Metrics (migration 009)
	ScreenWidth     int        `json:"screen_width" db:"screen_width"`
	ScreenHeight    int        `json:"screen_height" db:"screen_height"`
	Timezone        string     `json:"timezone" db:"timezone"`
	Language        string     `json:"language" db:"language"`
	DurationSeconds int        `json:"duration_seconds" db:"duration_seconds"`
	PageCount       int        `json:"page_count" db:"page_count"`
	ExitPage        string     `json:"exit_page" db:"exit_page"`
	// Interaction metrics (migration 010)
	ClickCount      int        `json:"click_count" db:"click_count"`
	InputCount      int        `json:"input_count" db:"input_count"`
	ScrollDepthPct  int        `json:"scroll_depth_pct" db:"scroll_depth_pct"`
	RageClicks      int        `json:"rage_clicks" db:"rage_clicks"`
	// Computed — joined in dashboard queries
	TimeToPurchase  *int       `json:"time_to_purchase,omitempty"`
	UTMSource       string     `json:"utm_source"`
	UTMMedium       string     `json:"utm_medium"`
	UTMCampaign     string     `json:"utm_campaign"`
	EventCount      int        `json:"event_count"`
	HasReplay       bool       `json:"has_replay"`
}

type Attribution struct {
	ID          uuid.UUID `json:"id" db:"id"`
	SessionID   uuid.UUID `json:"session_id" db:"session_id"`
	ProjectID   uuid.UUID `json:"project_id" db:"project_id"`
	UTMSource   string    `json:"utm_source" db:"utm_source"`
	UTMMedium   string    `json:"utm_medium" db:"utm_medium"`
	UTMCampaign string    `json:"utm_campaign" db:"utm_campaign"`
	UTMContent  string    `json:"utm_content" db:"utm_content"`
	UTMTerm     string    `json:"utm_term" db:"utm_term"`
	FBClid      string    `json:"fbclid" db:"fbclid"`
	GClid       string    `json:"gclid" db:"gclid"`
	TTClid      string    `json:"ttclid" db:"ttclid"`
	KWClid      string    `json:"kwclid" db:"kwclid"`
	FBP         string    `json:"fbp" db:"fbp"`
	FBC         string    `json:"fbc" db:"fbc"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

type Event struct {
	ID         uuid.UUID              `json:"id" db:"id"`
	SessionID  uuid.UUID              `json:"session_id" db:"session_id"`
	ProjectID  uuid.UUID              `json:"project_id" db:"project_id"`
	Name       string                 `json:"name" db:"name"`
	Properties map[string]interface{} `json:"properties" db:"properties"`
	CreatedAt  time.Time              `json:"created_at" db:"created_at"`
}

type Conversion struct {
	ID            uuid.UUID              `json:"id" db:"id"`
	ProjectID     uuid.UUID              `json:"project_id" db:"project_id"`
	SessionID     *uuid.UUID             `json:"session_id" db:"session_id"`
	ExternalID    string                 `json:"external_id" db:"external_id"`
	EventName     string                 `json:"event_name" db:"event_name"`
	Value         float64                `json:"value" db:"value"`
	Currency      string                 `json:"currency" db:"currency"`
	EmailHash     string                 `json:"email_hash" db:"email_hash"`
	PhoneHash     string                 `json:"phone_hash" db:"phone_hash"`
	Platform      string                 `json:"platform" db:"platform"`
	RawPayload    map[string]interface{} `json:"raw_payload" db:"raw_payload"`
	Attributed    bool                   `json:"attributed" db:"attributed"`
	// Analytics fields (added in migration 006)
	PaymentMethod string                 `json:"payment_method" db:"payment_method"` // cartao | pix | boleto
	Status        string                 `json:"status" db:"status"`                 // approved | pending | refunded | chargeback
	ProductName   string                 `json:"product_name" db:"product_name"`
	ProductCost   float64                `json:"product_cost" db:"product_cost"`
	CreatedAt     time.Time              `json:"created_at" db:"created_at"`
}

// ProjectSettings stores financial configuration per project.
type ProjectSettings struct {
	ProjectID                 uuid.UUID `json:"project_id" db:"project_id"`
	TaxRate                   float64   `json:"tax_rate" db:"tax_rate"`
	AdditionalExpensesMonthly float64   `json:"additional_expenses_monthly" db:"additional_expenses_monthly"`
	ProductCostDefault        float64   `json:"product_cost_default" db:"product_cost_default"`
	UpdatedAt                 time.Time `json:"updated_at" db:"updated_at"`
}

// AdCost represents a manual ad spend entry for a given day.
type AdCost struct {
	ID           uuid.UUID `json:"id" db:"id"`
	ProjectID    uuid.UUID `json:"project_id" db:"project_id"`
	Date         time.Time `json:"date" db:"date"`
	AdAccountID  string    `json:"ad_account_id" db:"ad_account_id"`
	Platform     string    `json:"platform" db:"platform"`
	UTMSource    string    `json:"utm_source" db:"utm_source"`
	UTMCampaign  string    `json:"utm_campaign" db:"utm_campaign"`
	Amount       float64   `json:"amount" db:"amount"`
	Currency     string    `json:"currency" db:"currency"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
}

type IntegrationSettings struct {
	ID        uuid.UUID              `json:"id" db:"id"`
	ProjectID uuid.UUID              `json:"project_id" db:"project_id"`
	Platform  string                 `json:"platform" db:"platform"`
	Enabled   bool                   `json:"enabled" db:"enabled"`
	Config    map[string]interface{} `json:"config" db:"config"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

// TriggerRule define quando e qual evento o SDK dispara automaticamente.
type TriggerRule struct {
	ID             uuid.UUID              `json:"id"`
	ProjectID      uuid.UUID              `json:"project_id"`
	Name           string                 `json:"name"`
	Enabled        bool                   `json:"enabled"`
	Type           string                 `json:"type"`        // pageload | click | visibility | scroll | submit
	EventName      string                 `json:"event_name"`
	URLPattern     string                 `json:"url_pattern"` // glob ou "contains:texto"
	Selector       string                 `json:"selector"`    // seletor CSS
	ScrollDepth    int                    `json:"scroll_depth"`
	Properties     map[string]interface{} `json:"properties"`
	FireConversion bool                   `json:"fire_conversion"`
	SortOrder      int                    `json:"sort_order"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// ConversionWithAttribution is used for dashboard queries
type ConversionWithAttribution struct {
	Conversion
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMContent  string `json:"utm_content"`
}

// CampaignStats aggregates conversion data by campaign
type CampaignStats struct {
	UTMSource   string  `json:"utm_source"`
	UTMMedium   string  `json:"utm_medium"`
	UTMCampaign string  `json:"utm_campaign"`
	Conversions int     `json:"conversions"`
	Revenue     float64 `json:"revenue"`
	Sessions    int     `json:"sessions"`
}
