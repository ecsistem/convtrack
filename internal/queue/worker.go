package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/crypto"
	"github.com/ecsistem/convtrack/internal/integrations/google"
	"github.com/ecsistem/convtrack/internal/integrations/kwai"
	"github.com/ecsistem/convtrack/internal/integrations/meta"
	"github.com/ecsistem/convtrack/internal/integrations/tiktok"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// TestEventPayload é publicado no canal Redis "test_events:{projectId}"
// para que o SSE stream entregue ao dashboard em tempo real.
type TestEventPayload struct {
	JobID      string `json:"job_id"`
	Platform   string `json:"platform"`
	EventName  string `json:"event_name"`
	Success    bool   `json:"success"`
	ErrorMsg   string `json:"error_msg,omitempty"`
	Attempt    int    `json:"attempt"`
	NextRetry  string `json:"next_retry,omitempty"` // ISO8601
	Timestamp  string `json:"timestamp"`
}

type Worker struct {
	queue *Queue
	db    *pgxpool.Pool
	rdb   *redis.Client
}

func NewWorker(q *Queue, db *pgxpool.Pool, rdb *redis.Client) *Worker {
	return &Worker{queue: q, db: db, rdb: rdb}
}

// Start lança o worker em background. Chame com `go worker.Start(ctx)`.
func (w *Worker) Start(ctx context.Context) {
	// Loop de reaping de jobs atrasados
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := w.queue.ReapDelayed(ctx); err != nil {
					fmt.Printf("worker: reap error: %v\n", err)
				} else if n > 0 {
					fmt.Printf("worker: reaped %d delayed jobs\n", n)
				}
			}
		}
	}()

	// Loop principal de processamento
	for {
		select {
		case <-ctx.Done():
			fmt.Println("worker: shutting down")
			return
		default:
			job, err := w.queue.Dequeue(ctx)
			if err != nil {
				fmt.Printf("worker: dequeue error: %v\n", err)
				time.Sleep(time.Second)
				continue
			}
			if job == nil {
				continue // timeout sem jobs — volta a esperar
			}
			w.process(ctx, job)
		}
	}
}

func (w *Worker) process(ctx context.Context, job *Job) {
	switch job.Type {
	case JobFireIntegration:
		w.processFireIntegration(ctx, job)
	default:
		fmt.Printf("worker: unknown job type %q\n", job.Type)
	}
}

func (w *Worker) processFireIntegration(ctx context.Context, job *Job) {
	var p FireIntegrationPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		fmt.Printf("worker: unmarshal payload: %v\n", err)
		return
	}

	// Carrega tudo que precisa do banco
	conv, attr, sess, settings, err := w.loadContext(ctx, p.ConversionID, p.ProjectID, p.Platform)
	if err != nil {
		fmt.Printf("worker: load context: %v\n", err)
		w.handleFailure(ctx, job, err)
		return
	}

	// Dispara a integração
	fireErr := w.fire(ctx, settings, conv, attr, sess, p.Platform, conv.ID.String())

	// Registra log no banco
	w.writeLog(ctx, conv.ProjectID, conv.ID, p.Platform, conv.EventName, fireErr)

	// Publica no canal SSE
	w.publishTestEvent(ctx, conv.ProjectID.String(), job, p.Platform, conv.EventName, fireErr)

	if fireErr != nil {
		w.handleFailure(ctx, job, fireErr)
	} else {
		fmt.Printf("worker: job %s OK [%s → %s]\n", job.ID, p.Platform, conv.EventName)
	}
}

func (w *Worker) handleFailure(ctx context.Context, job *Job, err error) {
	if job.Attempts >= job.MaxAttempts-1 {
		// Tentativas esgotadas — move para failed_jobs
		fmt.Printf("worker: job %s exhausted after %d attempts: %v\n", job.ID, job.MaxAttempts, err)
		w.saveFailedJob(ctx, job, err)
		return
	}

	delay := RetryDelay(job.Attempts)
	nextRun := time.Now().Add(delay)
	fmt.Printf("worker: job %s attempt %d/%d failed, retrying in %s\n",
		job.ID, job.Attempts+1, job.MaxAttempts, delay)

	if schedErr := w.queue.Schedule(ctx, job, delay); schedErr != nil {
		fmt.Printf("worker: schedule retry error: %v\n", schedErr)
	}
	_ = nextRun
}

// ── Helpers de integração ──────────────────────────────────────────────────

func (w *Worker) fire(ctx context.Context, settings models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, platform, eventID string) error {
	switch platform {
	case "meta":
		return w.fireMeta(ctx, settings, conv, attr, sess, eventID)
	case "google":
		return w.fireGoogle(ctx, settings, conv, attr, sess, eventID)
	case "tiktok":
		return w.fireTikTok(ctx, settings, conv, attr, sess, eventID)
	case "kwai":
		return w.fireKwai(ctx, settings, conv, attr, sess, eventID)
	}
	return fmt.Errorf("unknown platform: %s", platform)
}

func (w *Worker) fireMeta(ctx context.Context, s models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	pixelID, _ := s.Config["pixel_id"].(string)
	token, _ := s.Config["access_token"].(string)
	testCode, _ := s.Config["test_code"].(string)
	if pixelID == "" || token == "" {
		return fmt.Errorf("meta: missing pixel_id or access_token")
	}
	client := meta.NewClient(pixelID, token, testCode)
	ud := meta.UserData{Email: conv.EmailHash, Phone: conv.PhoneHash}
	if attr != nil {
		ud.FBP = attr.FBP
		ud.FBC = attr.FBC
		if ud.FBC == "" && attr.FBClid != "" {
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

func (w *Worker) fireGoogle(ctx context.Context, s models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	measurementID, _ := s.Config["measurement_id"].(string)
	apiSecret, _ := s.Config["api_secret"].(string)
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

func (w *Worker) fireTikTok(ctx context.Context, s models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	accessToken, _ := s.Config["access_token"].(string)
	pixelCode, _ := s.Config["pixel_code"].(string)
	if accessToken == "" || pixelCode == "" {
		return fmt.Errorf("tiktok: missing access_token or pixel_code")
	}
	client := tiktok.NewClient(accessToken, pixelCode)
	user := tiktok.UserCtx{EmailSHA256: conv.EmailHash, PhoneSHA256: conv.PhoneHash}
	if attr != nil {
		user.TTClid = attr.TTClid
	}
	ip, ua, pageURL := "", "", ""
	if sess != nil {
		ip, ua, pageURL = sess.IP, sess.UserAgent, sess.LandingPage
	}
	switch conv.EventName {
	case "Purchase":
		return client.SendCompletePayment(ctx, eventID, user, conv.Value, conv.Currency, conv.ExternalID, pageURL, ip, ua)
	case "Lead":
		return client.SendSubmitForm(ctx, eventID, user, pageURL, ip, ua)
	}
	return nil
}

func (w *Worker) fireKwai(ctx context.Context, s models.IntegrationSettings, conv *models.Conversion, attr *models.Attribution, sess *models.Session, eventID string) error {
	pixelID, _ := s.Config["pixel_id"].(string)
	accessToken, _ := s.Config["access_token"].(string)
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
		ud.IP, ud.UA = sess.IP, sess.UserAgent
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

// ── Queries de contexto ────────────────────────────────────────────────────

func (w *Worker) loadContext(ctx context.Context, convIDStr, projectIDStr, platform string) (*models.Conversion, *models.Attribution, *models.Session, models.IntegrationSettings, error) {
	convID, err := uuid.Parse(convIDStr)
	if err != nil {
		return nil, nil, nil, models.IntegrationSettings{}, fmt.Errorf("invalid conversion_id: %w", err)
	}

	// Conversão
	var conv models.Conversion
	err = w.db.QueryRow(ctx, `
		SELECT id, project_id, session_id, external_id, event_name, value, currency,
		       COALESCE(email_hash,''), COALESCE(phone_hash,''), COALESCE(platform,''), attributed
		FROM conversions WHERE id = $1`, convID,
	).Scan(&conv.ID, &conv.ProjectID, &conv.SessionID, &conv.ExternalID,
		&conv.EventName, &conv.Value, &conv.Currency,
		&conv.EmailHash, &conv.PhoneHash, &conv.Platform, &conv.Attributed)
	if err != nil {
		return nil, nil, nil, models.IntegrationSettings{}, fmt.Errorf("load conversion: %w", err)
	}

	// Attribution (opcional)
	var attr *models.Attribution
	if conv.SessionID != nil {
		var a models.Attribution
		err = w.db.QueryRow(ctx, `
			SELECT session_id, project_id, utm_source, utm_medium, utm_campaign, utm_content, utm_term,
			       fbclid, gclid, ttclid, COALESCE(kwclid,''), fbp, fbc
			FROM attributions WHERE session_id = $1`, *conv.SessionID,
		).Scan(&a.SessionID, &a.ProjectID,
			&a.UTMSource, &a.UTMMedium, &a.UTMCampaign, &a.UTMContent, &a.UTMTerm,
			&a.FBClid, &a.GClid, &a.TTClid, &a.KWClid, &a.FBP, &a.FBC)
		if err == nil {
			attr = &a
		}
	}

	// Session (opcional)
	var sess *models.Session
	if conv.SessionID != nil {
		var s models.Session
		err = w.db.QueryRow(ctx, `
			SELECT id, visitor_id, project_id, started_at, last_activity,
			       landing_page, referrer, user_agent, ip, country, city, device, browser, os
			FROM sessions WHERE id = $1`, *conv.SessionID,
		).Scan(&s.ID, &s.VisitorID, &s.ProjectID, &s.StartedAt, &s.LastActivity,
			&s.LandingPage, &s.Referrer, &s.UserAgent, &s.IP, &s.Country, &s.City,
			&s.Device, &s.Browser, &s.OS)
		if err == nil {
			sess = &s
		}
	}

	// Integration settings (com decrypt de credenciais)
	settings, err := w.loadIntegrationSettings(ctx, conv.ProjectID, platform)
	if err != nil {
		return nil, nil, nil, models.IntegrationSettings{}, fmt.Errorf("load settings for %s: %w", platform, err)
	}

	return &conv, attr, sess, settings, nil
}

func (w *Worker) loadIntegrationSettings(ctx context.Context, projectID uuid.UUID, platform string) (models.IntegrationSettings, error) {
	var is models.IntegrationSettings
	var configEncrypted string
	err := w.db.QueryRow(ctx, `
		SELECT id, project_id, platform, enabled, config
		FROM integration_settings
		WHERE project_id = $1 AND platform = $2 AND enabled = TRUE`,
		projectID, platform,
	).Scan(&is.ID, &is.ProjectID, &is.Platform, &is.Enabled, &configEncrypted)
	if err != nil {
		return models.IntegrationSettings{}, err
	}

	// Tenta descriptografar — se falhar, tenta parsear como JSON direto (retrocompatível)
	configJSON, decErr := decryptConfig(configEncrypted)
	if decErr != nil {
		configJSON = configEncrypted
	}

	if err := json.Unmarshal([]byte(configJSON), &is.Config); err != nil {
		return models.IntegrationSettings{}, fmt.Errorf("unmarshal config: %w", err)
	}
	return is, nil
}

// decryptConfig tenta descriptografar com AES-256-GCM.
// Se falhar (ex: config ainda em plaintext JSON — migração), retorna o valor original.
func decryptConfig(s string) (string, error) {
	plain, err := crypto.DecryptString(s)
	if err != nil {
		// Retrocompatibilidade: configs antigas podem estar em JSON plaintext
		return s, nil
	}
	return plain, nil
}

// ── Banco: logs e failed jobs ──────────────────────────────────────────────

func (w *Worker) writeLog(ctx context.Context, projectID, convID uuid.UUID, platform, eventName string, fireErr error) {
	success := fireErr == nil
	errMsg := ""
	if fireErr != nil {
		errMsg = fireErr.Error()
	}
	_, _ = w.db.Exec(ctx, `
		INSERT INTO integration_logs
			(project_id, conversion_id, platform, event_name, success, error_msg)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		projectID, convID, platform, eventName, success, errMsg,
	)
}

func (w *Worker) saveFailedJob(ctx context.Context, job *Job, finalErr error) {
	data, _ := json.Marshal(job)
	_, _ = w.db.Exec(ctx, `
		INSERT INTO failed_jobs (job_id, job_type, payload, platform, project_id, error_msg, attempts)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		job.ID, string(job.Type), data, job.Platform, job.ProjectID,
		finalErr.Error(), job.Attempts,
	)
}

// ── SSE publish ────────────────────────────────────────────────────────────

func (w *Worker) publishTestEvent(ctx context.Context, projectID string, job *Job, platform, eventName string, fireErr error) {
	evt := TestEventPayload{
		JobID:     job.ID,
		Platform:  platform,
		EventName: eventName,
		Success:   fireErr == nil,
		Attempt:   job.Attempts + 1,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if fireErr != nil {
		evt.ErrorMsg = fireErr.Error()
		if job.Attempts < job.MaxAttempts-1 {
			delay := RetryDelay(job.Attempts)
			evt.NextRetry = time.Now().Add(delay).UTC().Format(time.RFC3339)
		}
	}
	data, _ := json.Marshal(evt)
	channel := fmt.Sprintf("test_events:%s", projectID)
	_ = w.rdb.Publish(ctx, channel, string(data))
}
