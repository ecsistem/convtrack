package shield

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tipos de webhook suportados
const (
	WebhookTelegram = "telegram"
	WebhookDiscord  = "discord"
	WebhookCustom   = "custom"
)

// Eventos que podem disparar webhooks
const (
	EventBotDetected = "bot_detected"
	EventVisit       = "visit"
	EventConversion  = "conversion"
)

// WebhookConfig define um webhook configurado por projeto.
type WebhookConfig struct {
	ID        uuid.UUID `json:"id"         db:"id"`
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	Name      string    `json:"name"       db:"name"`
	Type      string    `json:"type"       db:"type"`
	URL       string    `json:"url"        db:"url"`
	Token     string    `json:"token"      db:"token"`   // Telegram: bot token
	ChatID    string    `json:"chat_id"    db:"chat_id"` // Telegram: chat_id
	Events    []string  `json:"events"     db:"events"`
	Enabled   bool      `json:"enabled"    db:"enabled"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// ── CRUD ──────────────────────────────────────────────────────────────────

func (s *Service) ListWebhooks(ctx context.Context, projectID uuid.UUID) ([]WebhookConfig, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, name, type, url, token, chat_id, events, enabled, created_at
		FROM shield_webhooks WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WebhookConfig
	for rows.Next() {
		var w WebhookConfig
		if err := rows.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Type, &w.URL,
			&w.Token, &w.ChatID, &w.Events, &w.Enabled, &w.CreatedAt); err != nil {
			continue
		}
		list = append(list, w)
	}
	if list == nil {
		list = []WebhookConfig{}
	}
	return list, nil
}

func (s *Service) CreateWebhook(ctx context.Context, w *WebhookConfig) (*WebhookConfig, error) {
	w.ID = uuid.New()
	w.CreatedAt = time.Now()
	if len(w.Events) == 0 {
		w.Events = []string{EventBotDetected}
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO shield_webhooks (id, project_id, name, type, url, token, chat_id, events, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		w.ID, w.ProjectID, w.Name, w.Type, w.URL, w.Token, w.ChatID, w.Events, w.Enabled,
	)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Service) UpdateWebhook(ctx context.Context, w *WebhookConfig) error {
	_, err := s.db.Exec(ctx, `
		UPDATE shield_webhooks
		SET name=$1, type=$2, url=$3, token=$4, chat_id=$5, events=$6, enabled=$7
		WHERE id=$8 AND project_id=$9`,
		w.Name, w.Type, w.URL, w.Token, w.ChatID, w.Events, w.Enabled, w.ID, w.ProjectID,
	)
	return err
}

func (s *Service) DeleteWebhook(ctx context.Context, id, projectID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM shield_webhooks WHERE id=$1 AND project_id=$2`, id, projectID)
	return err
}

// ── Dispatch ──────────────────────────────────────────────────────────────

// FireWebhooks dispara todos os webhooks habilitados que assinam o evento.
func (s *Service) FireWebhooks(ctx context.Context, projectID uuid.UUID, event string, payload map[string]interface{}) {
	whs, err := s.ListWebhooks(ctx, projectID)
	if err != nil {
		return
	}
	for _, w := range whs {
		if !w.Enabled {
			continue
		}
		if !sliceContains(w.Events, event) {
			continue
		}
		go s.dispatch(w, event, payload)
	}
}

func (s *Service) dispatch(w WebhookConfig, event string, payload map[string]interface{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch w.Type {
	case WebhookTelegram:
		s.sendTelegram(ctx, w, event, payload)
	case WebhookDiscord:
		s.sendDiscord(ctx, w, event, payload)
	default:
		s.sendCustom(ctx, w, event, payload)
	}
}

// FireSingleWebhook dispara para um único webhook e retorna erro se falhar.
// Usado pelo endpoint de teste — ignora enabled/events para permitir testar qualquer webhook.
func (s *Service) FireSingleWebhook(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch w.Type {
	case WebhookTelegram:
		return s.sendTelegramErr(ctx, w, event, payload)
	case WebhookDiscord:
		return s.sendDiscordErr(ctx, w, event, payload)
	default:
		return s.sendCustomErr(ctx, w, event, payload)
	}
}

// ── Telegram ──────────────────────────────────────────────────────────────

func (s *Service) sendTelegram(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) {
	_ = s.sendTelegramErr(ctx, w, event, payload)
}

func (s *Service) sendTelegramErr(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) error {
	if w.Token == "" || w.ChatID == "" {
		return fmt.Errorf("telegram: token ou chat_id não configurados")
	}
	text := buildText(event, payload)
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    w.ChatID,
		"text":       text,
		"parse_mode": "HTML",
	})
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", w.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── Discord ───────────────────────────────────────────────────────────────

func (s *Service) sendDiscord(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) {
	_ = s.sendDiscordErr(ctx, w, event, payload)
}

func (s *Service) sendDiscordErr(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) error {
	if w.URL == "" {
		return fmt.Errorf("discord: URL não configurada")
	}
	color := 0x4f46e5 // indigo — evento genérico
	if event == EventBotDetected {
		color = 0xef4444 // red
	} else if event == EventConversion {
		color = 0x22c55e // green
	} else if event == EventVisit {
		color = 0x7c3aed // violet
	}

	// Campos ordenados para exibição previsível no Discord
	orderedKeys := []string{"ip", "reason", "action", "device", "score", "signals", "user_agent", "note"}
	seen := map[string]bool{}
	var fields []map[string]interface{}
	for _, k := range orderedKeys {
		if v, ok := payload[k]; ok {
			fields = append(fields, map[string]interface{}{
				"name":   k,
				"value":  fmt.Sprintf("`%v`", v),
				"inline": true,
			})
			seen[k] = true
		}
	}
	for k, v := range payload {
		if !seen[k] {
			fields = append(fields, map[string]interface{}{
				"name":   k,
				"value":  fmt.Sprintf("`%v`", v),
				"inline": true,
			})
		}
	}

	body, _ := json.Marshal(map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":     "🛡️ ConvTrack Shield · " + eventLabel(event),
				"color":     color,
				"fields":    fields,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
				"footer":    map[string]string{"text": "ConvTrack Shield"},
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ── Custom HTTP ───────────────────────────────────────────────────────────

func (s *Service) sendCustom(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) {
	_ = s.sendCustomErr(ctx, w, event, payload)
}

func (s *Service) sendCustomErr(ctx context.Context, w WebhookConfig, event string, payload map[string]interface{}) error {
	if w.URL == "" {
		return fmt.Errorf("custom: URL não configurada")
	}
	envelope := map[string]interface{}{
		"event":   event,
		"payload": payload,
		"ts":      time.Now().Unix(),
		"source":  "convtrack-shield",
	}
	body, _ := json.Marshal(envelope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http: status %d", resp.StatusCode)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

func eventLabel(event string) string {
	switch event {
	case EventBotDetected:
		return "Bot Detectado"
	case EventVisit:
		return "Nova Visita"
	case EventConversion:
		return "Conversão"
	default:
		return event
	}
}

func buildText(event string, payload map[string]interface{}) string {
	var sb strings.Builder
	icon := "🛡️"
	switch event {
	case EventBotDetected:
		icon = "🚫"
	case EventConversion:
		icon = "✅"
	case EventVisit:
		icon = "👤"
	}
	sb.WriteString(fmt.Sprintf("%s <b>ConvTrack Shield · %s</b>\n\n", icon, eventLabel(event)))

	// Campos em ordem previsível
	orderedKeys := []string{"ip", "reason", "action", "device", "score", "signals", "user_agent", "note"}
	seen := map[string]bool{}
	for _, k := range orderedKeys {
		if v, ok := payload[k]; ok {
			sb.WriteString(fmt.Sprintf("<b>%s:</b> <code>%v</code>\n", k, v))
			seen[k] = true
		}
	}
	for k, v := range payload {
		if !seen[k] {
			sb.WriteString(fmt.Sprintf("<b>%s:</b> <code>%v</code>\n", k, v))
		}
	}
	return sb.String()
}

func sliceContains(slice []string, val string) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}
