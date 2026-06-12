package conversion

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/attribution"
	"github.com/ecsistem/convtrack/internal/integrations/google"
	"github.com/ecsistem/convtrack/internal/integrations/kwai"
	"github.com/ecsistem/convtrack/internal/integrations/meta"
	"github.com/ecsistem/convtrack/internal/integrations/tiktok"
	"github.com/ecsistem/convtrack/internal/live"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/queue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Service struct {
	db          *pgxpool.Pool
	attribution *attribution.Service
	queue       *queue.Queue
	rdb         *redis.Client // opcional, para eventos live
}

func New(db *pgxpool.Pool, attr *attribution.Service) *Service {
	return &Service{db: db, attribution: attr}
}

func NewWithQueue(db *pgxpool.Pool, attr *attribution.Service, q *queue.Queue) *Service {
	return &Service{db: db, attribution: attr, queue: q}
}

// WithLive habilita publicação de eventos em tempo real.
func (s *Service) WithLive(rdb *redis.Client) *Service {
	s.rdb = rdb
	return s
}

type CreateInput struct {
	ProjectID     uuid.UUID
	SessionID     *uuid.UUID
	ExternalID    string
	EventName     string
	Value         float64
	Currency      string
	Email         string
	Phone         string
	Platform      string
	RawPayload    map[string]interface{}
	EmailHash     string
	PhoneHash     string
	// Analytics fields (migration 006)
	PaymentMethod string  // cartao | pix | boleto
	Status        string  // approved | pending | refunded | chargeback (default: approved)
	ProductName   string
	ProductCost   float64
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*models.Conversion, error) {
	id := uuid.New()

	rawJSON, _ := json.Marshal(in.RawPayload)

	status := in.Status
	if status == "" {
		status = "approved"
	}

	var conv models.Conversion
	err := s.db.QueryRow(ctx, `
		INSERT INTO conversions
			(id, project_id, session_id, external_id, event_name, value, currency,
			 email_hash, phone_hash, platform, raw_payload, attributed,
			 payment_method, status, product_name, product_cost)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, FALSE, $12, $13, $14, $15)
		RETURNING id, project_id, session_id, external_id, event_name, value, currency,
		          email_hash, phone_hash, platform, attributed,
		          COALESCE(payment_method,''), status, COALESCE(product_name,''), product_cost,
		          created_at`,
		id, in.ProjectID, in.SessionID, in.ExternalID, in.EventName, in.Value, in.Currency,
		in.EmailHash, in.PhoneHash, in.Platform, rawJSON,
		in.PaymentMethod, status, in.ProductName, in.ProductCost,
	).Scan(
		&conv.ID, &conv.ProjectID, &conv.SessionID, &conv.ExternalID,
		&conv.EventName, &conv.Value, &conv.Currency,
		&conv.EmailHash, &conv.PhoneHash, &conv.Platform,
		&conv.Attributed,
		&conv.PaymentMethod, &conv.Status, &conv.ProductName, &conv.ProductCost,
		&conv.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create conversion: %w", err)
	}

	// Evento de tempo real para o dashboard (best-effort).
	live.Publish(ctx, s.rdb, conv.ProjectID, live.Event{
		Type:  "conversion",
		Value: conv.Value,
		Label: conv.Platform,
	})

	return &conv, nil
}

func (s *Service) MarkAttributed(ctx context.Context, convID, sessionID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`UPDATE conversions SET session_id = $2, attributed = TRUE WHERE id = $1`,
		convID, sessionID,
	)
	return err
}

// EnqueueIntegrations enfileira um job por plataforma habilitada.
// O worker consome a fila com retry exponencial (30s, 1m, 2m, 4m, 8m).
// Retorna em <1ms — o caller nunca espera o disparo completar.
func (s *Service) EnqueueIntegrations(ctx context.Context, conv *models.Conversion) {
	if s.queue == nil {
		// Fallback síncrono se a fila não estiver configurada (dev sem Redis)
		s.FireIntegrations(ctx, conv, nil, nil)
		return
	}

	settings, err := s.loadIntegrationSettings(ctx, conv.ProjectID)
	if err != nil {
		fmt.Printf("enqueue: load settings: %v\n", err)
		return
	}

	for _, setting := range settings {
		if !setting.Enabled {
			continue
		}
		if err := s.queue.EnqueueFireIntegration(ctx, conv.ID.String(), conv.ProjectID.String(), setting.Platform); err != nil {
			fmt.Printf("enqueue: %s: %v\n", setting.Platform, err)
		}
	}
}

// FireIntegrations dispara todas as integrações de forma síncrona (usado como fallback).
func (s *Service) FireIntegrations(ctx context.Context, conv *models.Conversion, attr *models.Attribution, sess *models.Session) {
	settings, err := s.loadIntegrationSettings(ctx, conv.ProjectID)
	if err != nil {
		return
	}

	eventID := conv.ID.String()

	for _, setting := range settings {
		if !setting.Enabled {
			continue
		}

		var logErr error
		var logResp string

		switch setting.Platform {
		case "meta":
			logErr = s.fireMeta(ctx, setting, conv, attr, sess, eventID)
		case "google":
			logErr = s.fireGoogle(ctx, setting, conv, attr, sess, eventID)
		case "tiktok":
			logErr = s.fireTikTok(ctx, setting, conv, attr, sess, eventID)
		case "kwai":
			logErr = s.fireKwai(ctx, setting, conv, attr, sess, eventID)
		}

		success := logErr == nil
		if logErr != nil {
			logResp = logErr.Error()
		}
		_ = s.writeLog(ctx, conv.ProjectID, conv.ID, setting.Platform, conv.EventName, success, logResp)
	}
}

func (s *Service) fireMeta(ctx context.Context, setting models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	pixelID, _ := setting.Config["pixel_id"].(string)
	token, _ := setting.Config["access_token"].(string)
	testCode, _ := setting.Config["test_code"].(string)

	if pixelID == "" || token == "" {
		return fmt.Errorf("meta: missing pixel_id or access_token")
	}

	client := meta.NewClient(pixelID, token, testCode)

	ud := meta.UserData{
		Email: conv.EmailHash,
		Phone: conv.PhoneHash,
	}
	if attr != nil {
		ud.FBP = attr.FBP
		ud.FBC = attr.FBC
		if attr.FBC == "" && attr.FBClid != "" {
			ud.FBC = fmt.Sprintf("fb.1.%d.%s", time.Now().UnixMilli(), attr.FBClid)
		}
	}
	if sess != nil {
		ud.ClientIP = sess.IP
		ud.UA = sess.UserAgent
	}

	sourceURL := ""
	if sess != nil {
		sourceURL = sess.LandingPage
	}

	switch conv.EventName {
	case "Purchase":
		_, err := client.SendPurchase(ctx, eventID, ud, conv.Value, conv.Currency, conv.ExternalID, sourceURL)
		return err
	case "Lead":
		_, err := client.SendLead(ctx, eventID, ud, sourceURL)
		return err
	}
	return nil
}

func (s *Service) fireGoogle(ctx context.Context, setting models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	measurementID, _ := setting.Config["measurement_id"].(string)
	apiSecret, _ := setting.Config["api_secret"].(string)

	if measurementID == "" || apiSecret == "" {
		return fmt.Errorf("google: missing measurement_id or api_secret")
	}

	client := google.NewGA4Client(measurementID, apiSecret)
	clientID := eventID
	if attr != nil && attr.GClid != "" {
		clientID = attr.GClid
	}

	switch conv.EventName {
	case "Purchase":
		return client.SendPurchase(ctx, clientID, conv.ExternalID, conv.Value, conv.Currency, conv.EmailHash)
	case "Lead":
		return client.SendLead(ctx, clientID, "", conv.EmailHash)
	}
	return nil
}

func (s *Service) fireTikTok(ctx context.Context, setting models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	accessToken, _ := setting.Config["access_token"].(string)
	pixelCode, _ := setting.Config["pixel_code"].(string)

	if accessToken == "" || pixelCode == "" {
		return fmt.Errorf("tiktok: missing access_token or pixel_code")
	}

	client := tiktok.NewClient(accessToken, pixelCode)

	user := tiktok.UserCtx{
		EmailSHA256: conv.EmailHash,
		PhoneSHA256: conv.PhoneHash,
	}
	if attr != nil {
		user.TTClid = attr.TTClid
	}

	ip, ua, pageURL := "", "", ""
	if sess != nil {
		ip = sess.IP
		ua = sess.UserAgent
		pageURL = sess.LandingPage
	}

	switch conv.EventName {
	case "Purchase":
		return client.SendCompletePayment(ctx, eventID, user, conv.Value, conv.Currency, conv.ExternalID, pageURL, ip, ua)
	case "Lead":
		return client.SendSubmitForm(ctx, eventID, user, pageURL, ip, ua)
	}
	return nil
}

func (s *Service) fireKwai(ctx context.Context, setting models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	pixelID, _ := setting.Config["pixel_id"].(string)
	accessToken, _ := setting.Config["access_token"].(string)

	if pixelID == "" || accessToken == "" {
		return fmt.Errorf("kwai: missing pixel_id or access_token")
	}

	client := kwai.NewClient(pixelID, accessToken)

	ud := kwai.UserData{}
	if conv.EmailHash != "" {
		ud.Emails = []string{conv.EmailHash}
	}
	if conv.PhoneHash != "" {
		ud.Phones = []string{conv.PhoneHash}
	}
	if attr != nil {
		ud.KWClid = attr.KWClid
	}
	if sess != nil {
		ud.IP = sess.IP
		ud.UA = sess.UserAgent
	}

	pageURL := ""
	if sess != nil {
		pageURL = sess.LandingPage
	}

	switch conv.EventName {
	case "Purchase":
		return client.SendPurchase(ctx, eventID, ud, conv.Value, conv.Currency, conv.ExternalID, pageURL)
	case "Lead":
		return client.SendLead(ctx, eventID, ud, pageURL)
	}
	return nil
}

func (s *Service) loadIntegrationSettings(ctx context.Context, projectID uuid.UUID) ([]models.IntegrationSettings, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, project_id, platform, enabled, config FROM integration_settings WHERE project_id = $1`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var settings []models.IntegrationSettings
	for rows.Next() {
		var is models.IntegrationSettings
		var configJSON []byte
		if err := rows.Scan(&is.ID, &is.ProjectID, &is.Platform, &is.Enabled, &configJSON); err != nil {
			continue
		}
		_ = json.Unmarshal(configJSON, &is.Config)
		settings = append(settings, is)
	}
	return settings, nil
}

func (s *Service) writeLog(ctx context.Context, projectID, convID uuid.UUID, platform, eventName string, success bool, errMsg string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO integration_logs (project_id, conversion_id, platform, event_name, success, error_msg)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		projectID, convID, platform, eventName, success, errMsg,
	)
	return err
}

func (s *Service) Stats(ctx context.Context, projectID uuid.UUID, since time.Time) ([]models.CampaignStats, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
			COALESCE(a.utm_source,'(direct)') as utm_source,
			COALESCE(a.utm_medium,'(none)') as utm_medium,
			COALESCE(a.utm_campaign,'(not set)') as utm_campaign,
			COUNT(c.id) as conversions,
			COALESCE(SUM(c.value),0) as revenue
		FROM conversions c
		LEFT JOIN attributions a ON a.session_id = c.session_id
		WHERE c.project_id = $1 AND c.created_at >= $2
		GROUP BY utm_source, utm_medium, utm_campaign
		ORDER BY revenue DESC`,
		projectID, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []models.CampaignStats
	for rows.Next() {
		var st models.CampaignStats
		if err := rows.Scan(&st.UTMSource, &st.UTMMedium, &st.UTMCampaign, &st.Conversions, &st.Revenue); err != nil {
			continue
		}
		stats = append(stats, st)
	}
	return stats, nil
}

func (s *Service) List(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]models.ConversionWithAttribution, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.id, c.project_id, c.session_id, c.external_id, c.event_name,
		       c.value, c.currency, c.email_hash, c.phone_hash, c.platform, c.attributed, c.created_at,
		       COALESCE(a.utm_source,'') , COALESCE(a.utm_medium,''),
		       COALESCE(a.utm_campaign,''), COALESCE(a.utm_content,'')
		FROM conversions c
		LEFT JOIN attributions a ON a.session_id = c.session_id
		WHERE c.project_id = $1
		ORDER BY c.created_at DESC
		LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []models.ConversionWithAttribution
	for rows.Next() {
		var c models.ConversionWithAttribution
		if err := rows.Scan(
			&c.ID, &c.ProjectID, &c.SessionID, &c.ExternalID, &c.EventName,
			&c.Value, &c.Currency, &c.EmailHash, &c.PhoneHash, &c.Platform, &c.Attributed, &c.CreatedAt,
			&c.UTMSource, &c.UTMMedium, &c.UTMCampaign, &c.UTMContent,
		); err != nil {
			continue
		}
		list = append(list, c)
	}
	return list, nil
}
