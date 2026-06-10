package rules

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ecsistem/convtrack/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Service {
	return &Service{db: db}
}

// List retorna todas as regras habilitadas de um projeto ordenadas por sort_order.
// Usado pelo tracker.js via GET /v1/rules.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, onlyEnabled bool) ([]models.TriggerRule, error) {
	q := `SELECT id, project_id, name, enabled, type, event_name,
	             COALESCE(url_pattern,''), COALESCE(selector,''), COALESCE(scroll_depth,0),
	             properties, fire_conversion, sort_order, created_at, updated_at
	      FROM trigger_rules
	      WHERE project_id = $1`
	if onlyEnabled {
		q += " AND enabled = TRUE"
	}
	q += " ORDER BY sort_order ASC, created_at ASC"

	rows, err := s.db.Query(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []models.TriggerRule
	for rows.Next() {
		var r models.TriggerRule
		var propsJSON []byte
		if err := rows.Scan(
			&r.ID, &r.ProjectID, &r.Name, &r.Enabled, &r.Type, &r.EventName,
			&r.URLPattern, &r.Selector, &r.ScrollDepth,
			&propsJSON, &r.FireConversion, &r.SortOrder,
			&r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(propsJSON, &r.Properties)
		list = append(list, r)
	}
	return list, nil
}

type CreateInput struct {
	ProjectID      uuid.UUID
	Name           string
	Type           string
	EventName      string
	URLPattern     string
	Selector       string
	ScrollDepth    int
	Properties     map[string]interface{}
	FireConversion bool
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*models.TriggerRule, error) {
	if err := validateType(in.Type); err != nil {
		return nil, err
	}
	if in.EventName == "" {
		return nil, fmt.Errorf("event_name is required")
	}

	propsJSON, _ := json.Marshal(in.Properties)
	if propsJSON == nil {
		propsJSON = []byte("{}")
	}

	var r models.TriggerRule
	var propsBytes []byte
	err := s.db.QueryRow(ctx, `
		INSERT INTO trigger_rules
			(project_id, name, enabled, type, event_name,
			 url_pattern, selector, scroll_depth, properties, fire_conversion)
		VALUES ($1, $2, TRUE, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,0), $8, $9)
		RETURNING id, project_id, name, enabled, type, event_name,
		          COALESCE(url_pattern,''), COALESCE(selector,''), COALESCE(scroll_depth,0),
		          properties, fire_conversion, sort_order, created_at, updated_at`,
		in.ProjectID, in.Name, in.Type, in.EventName,
		in.URLPattern, in.Selector, in.ScrollDepth, propsJSON, in.FireConversion,
	).Scan(
		&r.ID, &r.ProjectID, &r.Name, &r.Enabled, &r.Type, &r.EventName,
		&r.URLPattern, &r.Selector, &r.ScrollDepth,
		&propsBytes, &r.FireConversion, &r.SortOrder,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create trigger rule: %w", err)
	}
	_ = json.Unmarshal(propsBytes, &r.Properties)
	return &r, nil
}

type UpdateInput struct {
	Name           *string
	Enabled        *bool
	EventName      *string
	URLPattern     *string
	Selector       *string
	ScrollDepth    *int
	Properties     map[string]interface{}
	FireConversion *bool
}

func (s *Service) Update(ctx context.Context, id, projectID uuid.UUID, in UpdateInput) (*models.TriggerRule, error) {
	existing, err := s.get(ctx, id, projectID)
	if err != nil {
		return nil, err
	}

	// Aplica campos fornecidos (patch semântico)
	if in.Name != nil {
		existing.Name = *in.Name
	}
	if in.Enabled != nil {
		existing.Enabled = *in.Enabled
	}
	if in.EventName != nil {
		existing.EventName = *in.EventName
	}
	if in.URLPattern != nil {
		existing.URLPattern = *in.URLPattern
	}
	if in.Selector != nil {
		existing.Selector = *in.Selector
	}
	if in.ScrollDepth != nil {
		existing.ScrollDepth = *in.ScrollDepth
	}
	if in.Properties != nil {
		existing.Properties = in.Properties
	}
	if in.FireConversion != nil {
		existing.FireConversion = *in.FireConversion
	}

	propsJSON, _ := json.Marshal(existing.Properties)
	var propsBytes []byte
	err = s.db.QueryRow(ctx, `
		UPDATE trigger_rules SET
			name            = $3,
			enabled         = $4,
			event_name      = $5,
			url_pattern     = NULLIF($6,''),
			selector        = NULLIF($7,''),
			scroll_depth    = NULLIF($8,0),
			properties      = $9,
			fire_conversion = $10,
			updated_at      = NOW()
		WHERE id = $1 AND project_id = $2
		RETURNING id, project_id, name, enabled, type, event_name,
		          COALESCE(url_pattern,''), COALESCE(selector,''), COALESCE(scroll_depth,0),
		          properties, fire_conversion, sort_order, created_at, updated_at`,
		id, projectID,
		existing.Name, existing.Enabled, existing.EventName,
		existing.URLPattern, existing.Selector, existing.ScrollDepth,
		propsJSON, existing.FireConversion,
	).Scan(
		&existing.ID, &existing.ProjectID, &existing.Name, &existing.Enabled,
		&existing.Type, &existing.EventName,
		&existing.URLPattern, &existing.Selector, &existing.ScrollDepth,
		&propsBytes, &existing.FireConversion, &existing.SortOrder,
		&existing.CreatedAt, &existing.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("update trigger rule: %w", err)
	}
	_ = json.Unmarshal(propsBytes, &existing.Properties)
	return existing, nil
}

func (s *Service) Delete(ctx context.Context, id, projectID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM trigger_rules WHERE id = $1 AND project_id = $2`, id, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("rule not found")
	}
	return nil
}

func (s *Service) ToggleEnabled(ctx context.Context, id, projectID uuid.UUID, enabled bool) error {
	_, err := s.db.Exec(ctx,
		`UPDATE trigger_rules SET enabled = $3, updated_at = NOW() WHERE id = $1 AND project_id = $2`,
		id, projectID, enabled)
	return err
}

func (s *Service) get(ctx context.Context, id, projectID uuid.UUID) (*models.TriggerRule, error) {
	var r models.TriggerRule
	var propsBytes []byte
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, name, enabled, type, event_name,
		       COALESCE(url_pattern,''), COALESCE(selector,''), COALESCE(scroll_depth,0),
		       properties, fire_conversion, sort_order, created_at, updated_at
		FROM trigger_rules WHERE id = $1 AND project_id = $2`,
		id, projectID,
	).Scan(
		&r.ID, &r.ProjectID, &r.Name, &r.Enabled, &r.Type, &r.EventName,
		&r.URLPattern, &r.Selector, &r.ScrollDepth,
		&propsBytes, &r.FireConversion, &r.SortOrder,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("rule not found: %w", err)
	}
	_ = json.Unmarshal(propsBytes, &r.Properties)
	return &r, nil
}

func validateType(t string) error {
	valid := map[string]bool{
		"pageload": true, "click": true,
		"visibility": true, "scroll": true, "submit": true,
	}
	if !valid[t] {
		return fmt.Errorf("invalid type %q: must be pageload, click, visibility, scroll or submit", t)
	}
	return nil
}

// FireConversionFromRule cria uma conversão server-side originada de uma regra client-side.
// Chamado quando o tracker.js detecta fire_conversion=true numa regra.
// Retorna a conversão criada para que o caller possa enfileirar integrações (CAPI, TikTok, etc.).
func (s *Service) FireConversionFromRule(ctx context.Context, projectID uuid.UUID, ruleID, sessionID, eventName string, value float64, currency string) (*models.Conversion, error) {
	if currency == "" {
		currency = "BRL"
	}
	var conv models.Conversion
	err := s.db.QueryRow(ctx, `
		INSERT INTO conversions
			(project_id, session_id, external_id, event_name, value, currency, platform)
		VALUES ($1,
			(SELECT id FROM sessions WHERE id = $2::uuid LIMIT 1),
			$3, $4, $5, $6, 'rule')
		RETURNING id, project_id, session_id, COALESCE(external_id,''), event_name, value, currency,
		          COALESCE(email_hash,''), COALESCE(phone_hash,''), COALESCE(platform,''),
		          attributed, created_at,
		          COALESCE(payment_method,''), status, COALESCE(product_name,''), product_cost`,
		projectID,
		sessionID,
		"rule:"+ruleID,
		eventName,
		value,
		currency,
	).Scan(
		&conv.ID, &conv.ProjectID, &conv.SessionID, &conv.ExternalID, &conv.EventName,
		&conv.Value, &conv.Currency, &conv.EmailHash, &conv.PhoneHash, &conv.Platform,
		&conv.Attributed, &conv.CreatedAt, &conv.PaymentMethod, &conv.Status,
		&conv.ProductName, &conv.ProductCost,
	)
	if err != nil {
		return nil, fmt.Errorf("fire conversion from rule: %w", err)
	}
	return &conv, nil
}
