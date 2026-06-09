package session

import (
	"context"
	"fmt"
	"time"

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

type UpsertSessionInput struct {
	SessionID   uuid.UUID
	VisitorID   uuid.UUID
	ProjectID   uuid.UUID
	LandingPage string
	Referrer    string
	UserAgent   string
	IP          string
	Country     string
	City        string
	Device      string
	Browser     string
	OS          string
}

type UpsertVisitorInput struct {
	ProjectID   uuid.UUID
	Fingerprint string
}

func (s *Service) UpsertVisitor(ctx context.Context, in UpsertVisitorInput) (*models.Visitor, error) {
	var v models.Visitor
	err := s.db.QueryRow(ctx, `
		INSERT INTO visitors (id, project_id, fingerprint, first_seen, last_seen)
		VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
		ON CONFLICT (project_id, fingerprint) DO UPDATE
			SET last_seen = NOW()
		RETURNING id, project_id, fingerprint, first_seen, last_seen`,
		in.ProjectID, in.Fingerprint,
	).Scan(&v.ID, &v.ProjectID, &v.Fingerprint, &v.FirstSeen, &v.LastSeen)
	if err != nil {
		return nil, fmt.Errorf("upsert visitor: %w", err)
	}
	return &v, nil
}

func (s *Service) GetOrCreateVisitor(ctx context.Context, projectID, visitorID uuid.UUID) (*models.Visitor, error) {
	var v models.Visitor
	err := s.db.QueryRow(ctx,
		`SELECT id, project_id, fingerprint, first_seen, last_seen FROM visitors WHERE id = $1 AND project_id = $2`,
		visitorID, projectID,
	).Scan(&v.ID, &v.ProjectID, &v.Fingerprint, &v.FirstSeen, &v.LastSeen)
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE visitors SET last_seen = NOW() WHERE id = $1`, v.ID)
		return &v, nil
	}

	// Visitor ID not found (first time from this browser) — create new
	err = s.db.QueryRow(ctx, `
		INSERT INTO visitors (id, project_id, first_seen, last_seen)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET last_seen = NOW()
		RETURNING id, project_id, fingerprint, first_seen, last_seen`,
		visitorID, projectID,
	).Scan(&v.ID, &v.ProjectID, &v.Fingerprint, &v.FirstSeen, &v.LastSeen)
	return &v, err
}

func (s *Service) UpsertSession(ctx context.Context, in UpsertSessionInput) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(ctx, `
		INSERT INTO sessions (id, visitor_id, project_id, started_at, last_activity,
			landing_page, referrer, user_agent, ip, country, city, device, browser, os)
		VALUES ($1, $2, $3, NOW(), NOW(), $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (id) DO UPDATE
			SET last_activity = NOW()
		RETURNING id, visitor_id, project_id, started_at, last_activity,
			landing_page, referrer, user_agent, ip, country, city, device, browser, os`,
		in.SessionID, in.VisitorID, in.ProjectID,
		in.LandingPage, in.Referrer, in.UserAgent,
		in.IP, in.Country, in.City,
		in.Device, in.Browser, in.OS,
	).Scan(
		&sess.ID, &sess.VisitorID, &sess.ProjectID, &sess.StartedAt, &sess.LastActivity,
		&sess.LandingPage, &sess.Referrer, &sess.UserAgent,
		&sess.IP, &sess.Country, &sess.City,
		&sess.Device, &sess.Browser, &sess.OS,
	)
	return &sess, err
}

func (s *Service) UpsertAttribution(ctx context.Context, a *models.Attribution) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO attributions
			(session_id, project_id, utm_source, utm_medium, utm_campaign, utm_content, utm_term,
			 fbclid, gclid, ttclid, kwclid, fbp, fbc)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (session_id) DO UPDATE SET
			utm_source   = COALESCE(NULLIF(EXCLUDED.utm_source,''),   attributions.utm_source),
			utm_medium   = COALESCE(NULLIF(EXCLUDED.utm_medium,''),   attributions.utm_medium),
			utm_campaign = COALESCE(NULLIF(EXCLUDED.utm_campaign,''), attributions.utm_campaign),
			utm_content  = COALESCE(NULLIF(EXCLUDED.utm_content,''),  attributions.utm_content),
			utm_term     = COALESCE(NULLIF(EXCLUDED.utm_term,''),     attributions.utm_term),
			fbclid       = COALESCE(NULLIF(EXCLUDED.fbclid,''),       attributions.fbclid),
			gclid        = COALESCE(NULLIF(EXCLUDED.gclid,''),        attributions.gclid),
			ttclid       = COALESCE(NULLIF(EXCLUDED.ttclid,''),       attributions.ttclid),
			kwclid       = COALESCE(NULLIF(EXCLUDED.kwclid,''),       attributions.kwclid),
			fbp          = COALESCE(NULLIF(EXCLUDED.fbp,''),          attributions.fbp),
			fbc          = COALESCE(NULLIF(EXCLUDED.fbc,''),          attributions.fbc)`,
		a.SessionID, a.ProjectID,
		a.UTMSource, a.UTMMedium, a.UTMCampaign, a.UTMContent, a.UTMTerm,
		a.FBClid, a.GClid, a.TTClid, a.KWClid, a.FBP, a.FBC,
	)
	return err
}

func (s *Service) RecordIdentifier(ctx context.Context, projectID, visitorID uuid.UUID, idType, hash string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO visitor_identifiers (visitor_id, project_id, type, value_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project_id, value_hash) DO NOTHING`,
		visitorID, projectID, idType, hash,
	)
	return err
}

func (s *Service) GetAttribution(ctx context.Context, sessionID uuid.UUID) (*models.Attribution, error) {
	var a models.Attribution
	err := s.db.QueryRow(ctx, `
		SELECT session_id, project_id, utm_source, utm_medium, utm_campaign, utm_content, utm_term,
		       fbclid, gclid, ttclid, COALESCE(kwclid,''), fbp, fbc
		FROM attributions WHERE session_id = $1`, sessionID,
	).Scan(
		&a.SessionID, &a.ProjectID,
		&a.UTMSource, &a.UTMMedium, &a.UTMCampaign, &a.UTMContent, &a.UTMTerm,
		&a.FBClid, &a.GClid, &a.TTClid, &a.KWClid, &a.FBP, &a.FBC,
	)
	return &a, err
}

func (s *Service) FindSessionByEmailHash(ctx context.Context, projectID uuid.UUID, emailHash string) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(ctx, `
		SELECT s.id, s.visitor_id, s.project_id, s.started_at, s.last_activity,
		       s.landing_page, s.referrer, s.user_agent, s.ip, s.country, s.city,
		       s.device, s.browser, s.os
		FROM sessions s
		JOIN visitor_identifiers vi ON vi.visitor_id = s.visitor_id
		WHERE vi.project_id = $1 AND vi.value_hash = $2
		ORDER BY s.started_at DESC
		LIMIT 1`,
		projectID, emailHash,
	).Scan(
		&sess.ID, &sess.VisitorID, &sess.ProjectID, &sess.StartedAt, &sess.LastActivity,
		&sess.LandingPage, &sess.Referrer, &sess.UserAgent,
		&sess.IP, &sess.Country, &sess.City,
		&sess.Device, &sess.Browser, &sess.OS,
	)
	return &sess, err
}

func (s *Service) ListSessions(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]models.Session, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, visitor_id, project_id, started_at, last_activity,
		       landing_page, referrer, user_agent, ip, country, city, device, browser, os
		FROM sessions
		WHERE project_id = $1
		ORDER BY started_at DESC
		LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var s models.Session
		if err := rows.Scan(
			&s.ID, &s.VisitorID, &s.ProjectID, &s.StartedAt, &s.LastActivity,
			&s.LandingPage, &s.Referrer, &s.UserAgent,
			&s.IP, &s.Country, &s.City,
			&s.Device, &s.Browser, &s.OS,
		); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (s *Service) RecordEvent(ctx context.Context, sessionID, projectID uuid.UUID, name string, props map[string]interface{}) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO events (session_id, project_id, name, properties)
		VALUES ($1, $2, $3, $4)`,
		sessionID, projectID, name, props,
	)
	return err
}

func (s *Service) CountSessions(ctx context.Context, projectID uuid.UUID, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM sessions WHERE project_id = $1 AND started_at >= $2`,
		projectID, since,
	).Scan(&count)
	return count, err
}
