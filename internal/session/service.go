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
	SessionID    uuid.UUID
	VisitorID    uuid.UUID
	ProjectID    uuid.UUID
	LandingPage  string
	Referrer     string
	UserAgent    string
	IP           string
	Country      string
	City         string
	Device       string
	Browser      string
	OS           string
	ScreenWidth  int
	ScreenHeight int
	Timezone     string
	Language     string
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
		RETURNING id, project_id, COALESCE(fingerprint,''), first_seen, last_seen`,
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
		`SELECT id, project_id, COALESCE(fingerprint,''), first_seen, last_seen FROM visitors WHERE id = $1 AND project_id = $2`,
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
		RETURNING id, project_id, COALESCE(fingerprint,''), first_seen, last_seen`,
		visitorID, projectID,
	).Scan(&v.ID, &v.ProjectID, &v.Fingerprint, &v.FirstSeen, &v.LastSeen)
	return &v, err
}

func (s *Service) UpsertSession(ctx context.Context, in UpsertSessionInput) (*models.Session, error) {
	var sess models.Session
	err := s.db.QueryRow(ctx, `
		INSERT INTO sessions (id, visitor_id, project_id, started_at, last_activity,
			landing_page, referrer, user_agent, ip, country, city, device, browser, os,
			screen_width, screen_height, timezone, language)
		VALUES ($1, $2, $3, NOW(), NOW(), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (id) DO UPDATE
			SET last_activity = NOW()
		RETURNING id, visitor_id, project_id, started_at, last_activity,
			landing_page, referrer, user_agent, ip,
			COALESCE(country,''), COALESCE(city,''),
			COALESCE(device,''), COALESCE(browser,''), COALESCE(os,''),
			COALESCE(screen_width,0), COALESCE(screen_height,0),
			COALESCE(timezone,''), COALESCE(language,''),
			COALESCE(duration_seconds,0), COALESCE(page_count,1), COALESCE(exit_page,'')`,
		in.SessionID, in.VisitorID, in.ProjectID,
		in.LandingPage, in.Referrer, in.UserAgent,
		in.IP, in.Country, in.City,
		in.Device, in.Browser, in.OS,
		in.ScreenWidth, in.ScreenHeight, in.Timezone, in.Language,
	).Scan(
		&sess.ID, &sess.VisitorID, &sess.ProjectID, &sess.StartedAt, &sess.LastActivity,
		&sess.LandingPage, &sess.Referrer, &sess.UserAgent,
		&sess.IP, &sess.Country, &sess.City,
		&sess.Device, &sess.Browser, &sess.OS,
		&sess.ScreenWidth, &sess.ScreenHeight,
		&sess.Timezone, &sess.Language,
		&sess.DurationSeconds, &sess.PageCount, &sess.ExitPage,
	)
	return &sess, err
}

// HeartbeatInput holds all metrics sent by the tracker on each heartbeat.
type HeartbeatInput struct {
	SessionID       string
	DurationSeconds int
	PageCount       int
	CurrentPage     string
	ClickCount      int
	InputCount      int
	ScrollDepthPct  int
	RageClicks      int
	IsFinal         bool // true = beforeunload / pagehide / visibilitychange:hidden
}

// Heartbeat — atualiza duração, interações e página atual da sessão.
// Quando IsFinal=true, seta ended_at para encerrar a sessão formalmente.
func (s *Service) Heartbeat(ctx context.Context, in HeartbeatInput) error {
	sessionID, err := uuid.Parse(in.SessionID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		UPDATE sessions SET
			last_activity    = NOW(),
			duration_seconds = GREATEST(duration_seconds, $2),
			page_count       = GREATEST(page_count, $3),
			exit_page        = CASE WHEN $4 != '' THEN $4 ELSE exit_page END,
			click_count      = GREATEST(click_count, $5),
			input_count      = GREATEST(input_count, $6),
			scroll_depth_pct = GREATEST(scroll_depth_pct, $7),
			rage_clicks      = GREATEST(rage_clicks, $8),
			ended_at         = CASE WHEN $9 AND ended_at IS NULL THEN NOW() ELSE ended_at END
		WHERE id = $1`,
		sessionID,
		in.DurationSeconds, in.PageCount, in.CurrentPage,
		in.ClickCount, in.InputCount, in.ScrollDepthPct, in.RageClicks,
		in.IsFinal,
	)
	return err
}

// UpdateGeo preenche country/city após lookup assíncrono de IP.
func (s *Service) UpdateGeo(ctx context.Context, sessionID uuid.UUID, country, city string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sessions SET country = $2, city = $3 WHERE id = $1 AND (country = '' OR country IS NULL)`,
		sessionID, country, city,
	)
	return err
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
		SELECT
			s.id, s.visitor_id, s.project_id, s.started_at, s.last_activity,
			s.landing_page, s.referrer, s.user_agent, s.ip,
			COALESCE(s.country,''), COALESCE(s.city,''),
			COALESCE(s.device,''), COALESCE(s.browser,''), COALESCE(s.os,''),
			COALESCE(s.screen_width,0), COALESCE(s.screen_height,0),
			COALESCE(s.timezone,''), COALESCE(s.language,''),
			COALESCE(s.duration_seconds,0), COALESCE(s.page_count,1), COALESCE(s.exit_page,''),
			COALESCE(s.click_count,0), COALESCE(s.input_count,0),
			COALESCE(s.scroll_depth_pct,0), COALESCE(s.rage_clicks,0),
			-- time_to_purchase
			(SELECT EXTRACT(EPOCH FROM MIN(c.created_at) - s.started_at)::INT
			 FROM conversions c
			 WHERE c.session_id = s.id AND c.status = 'approved') AS time_to_purchase,
			-- attribution
			COALESCE(a.utm_source,''), COALESCE(a.utm_medium,''), COALESCE(a.utm_campaign,''),
			-- event count
			(SELECT COUNT(*) FROM events e WHERE e.session_id = s.id) AS event_count,
			-- has replay
			EXISTS(SELECT 1 FROM replays r WHERE r.session_id = s.id) AS has_replay
		FROM sessions s
		LEFT JOIN attributions a ON a.session_id = s.id
		WHERE s.project_id = $1
		ORDER BY s.started_at DESC
		LIMIT $2 OFFSET $3`,
		projectID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []models.Session
	for rows.Next() {
		var sess models.Session
		if err := rows.Scan(
			&sess.ID, &sess.VisitorID, &sess.ProjectID, &sess.StartedAt, &sess.LastActivity,
			&sess.LandingPage, &sess.Referrer, &sess.UserAgent,
			&sess.IP, &sess.Country, &sess.City,
			&sess.Device, &sess.Browser, &sess.OS,
			&sess.ScreenWidth, &sess.ScreenHeight,
			&sess.Timezone, &sess.Language,
			&sess.DurationSeconds, &sess.PageCount, &sess.ExitPage,
			&sess.ClickCount, &sess.InputCount, &sess.ScrollDepthPct, &sess.RageClicks,
			&sess.TimeToPurchase,
			&sess.UTMSource, &sess.UTMMedium, &sess.UTMCampaign,
			&sess.EventCount, &sess.HasReplay,
		); err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
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

// OnlineCount returns the number of sessions active in the last 5 minutes.
func (s *Service) OnlineCount(ctx context.Context, projectID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM sessions
		 WHERE project_id = $1 AND last_activity > NOW() - INTERVAL '5 minutes'`,
		projectID,
	).Scan(&count)
	return count, err
}
