package shield

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var slugRe = regexp.MustCompile(`[^a-z0-9\-]`)

// sanitizeSlug converte para lowercase, substitui espaços por hífens e remove caracteres inválidos.
func sanitizeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = slugRe.ReplaceAllString(s, "")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// ── Campaign ──────────────────────────────────────────────────────────────

// Campaign define as URLs e parâmetros de uma campanha de proteção.
type Campaign struct {
	ID            uuid.UUID `json:"id"             db:"id"`
	ProjectID     uuid.UUID `json:"project_id"     db:"project_id"`
	Name          string    `json:"name"           db:"name"`
	Slug          string    `json:"slug"           db:"slug"`            // /slug na URL principal
	SafeURL       string    `json:"safe_url"       db:"safe_url"`        // exibida para bots/revisores
	MoneyURL      string    `json:"money_url"      db:"money_url"`       // exibida para humanos reais
	SplitPct      int       `json:"split_pct"      db:"split_pct"`       // % de humanos → money_url
	Enabled       bool      `json:"enabled"        db:"enabled"`
	Platform      string    `json:"platform"       db:"platform"`        // meta|tiktok|kwai|google|taboola|manual
	ChallengeMode string    `json:"challenge_mode" db:"challenge_mode"`  // redirect|captcha|both
	RequireKey    bool      `json:"require_key"    db:"require_key"`     // exige ?_sk=access_key
	AccessKey     string    `json:"access_key"     db:"access_key"`      // chave secreta gerada
	CreatedAt     time.Time `json:"created_at"     db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"     db:"updated_at"`
}

// Domain mapeia um hostname a uma campanha (para o proxy reverso).
type Domain struct {
	ID           uuid.UUID `json:"id"            db:"id"`
	ProjectID    uuid.UUID `json:"project_id"    db:"project_id"`
	CampaignID   uuid.UUID `json:"campaign_id"   db:"campaign_id"`
	Domain       string    `json:"domain"        db:"domain"`
	SSLEnabled   bool      `json:"ssl_enabled"   db:"ssl_enabled"`
	CreatedAt    time.Time `json:"created_at"    db:"created_at"`
	CampaignName string    `json:"campaign_name,omitempty"`
}

// ── Campaign CRUD ─────────────────────────────────────────────────────────

func (s *Service) CreateCampaign(ctx context.Context, c *Campaign) (*Campaign, error) {
	c.ID = uuid.New()
	if c.SplitPct <= 0 || c.SplitPct > 100 {
		c.SplitPct = 100
	}
	c.Slug = sanitizeSlug(c.Slug)
	if c.ChallengeMode == "" {
		c.ChallengeMode = "redirect"
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO shield_campaigns
		  (id, project_id, name, slug, safe_url, money_url, split_pct, enabled,
		   platform, challenge_mode, require_key, access_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		c.ID, c.ProjectID, c.Name, c.Slug, c.SafeURL, c.MoneyURL, c.SplitPct, c.Enabled,
		c.Platform, c.ChallengeMode, c.RequireKey, c.AccessKey,
	)
	if err != nil {
		return nil, err
	}
	c.CreatedAt = time.Now()
	c.UpdatedAt = c.CreatedAt
	return c, nil
}

func (s *Service) ListCampaigns(ctx context.Context, projectID uuid.UUID) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, name, slug, safe_url, money_url, split_pct, enabled,
		       platform, challenge_mode, require_key, access_key, created_at, updated_at
		FROM shield_campaigns WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Campaign
	for rows.Next() {
		var c Campaign
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Slug, &c.SafeURL, &c.MoneyURL,
			&c.SplitPct, &c.Enabled, &c.Platform, &c.ChallengeMode,
			&c.RequireKey, &c.AccessKey, &c.CreatedAt, &c.UpdatedAt); err != nil {
			continue
		}
		list = append(list, c)
	}
	if list == nil {
		list = []Campaign{}
	}
	return list, nil
}

func (s *Service) UpdateCampaign(ctx context.Context, c *Campaign) error {
	if c.SplitPct <= 0 || c.SplitPct > 100 {
		c.SplitPct = 100
	}
	c.Slug = sanitizeSlug(c.Slug)
	if c.ChallengeMode == "" {
		c.ChallengeMode = "redirect"
	}
	_, err := s.db.Exec(ctx, `
		UPDATE shield_campaigns
		SET name=$1, slug=$2, safe_url=$3, money_url=$4, split_pct=$5, enabled=$6,
		    platform=$7, challenge_mode=$8, require_key=$9, access_key=$10, updated_at=NOW()
		WHERE id=$11 AND project_id=$12`,
		c.Name, c.Slug, c.SafeURL, c.MoneyURL, c.SplitPct, c.Enabled,
		c.Platform, c.ChallengeMode, c.RequireKey, c.AccessKey, c.ID, c.ProjectID,
	)
	return err
}

func (s *Service) DeleteCampaign(ctx context.Context, id, projectID uuid.UUID) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM shield_campaigns WHERE id=$1 AND project_id=$2`, id, projectID)
	return err
}

// ── Domain CRUD ───────────────────────────────────────────────────────────

func (s *Service) ListDomains(ctx context.Context, projectID uuid.UUID) ([]Domain, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sd.id, sd.project_id, sd.campaign_id, sd.domain, sd.ssl_enabled, sd.created_at,
		       sc.name
		FROM shield_domains sd
		JOIN shield_campaigns sc ON sd.campaign_id = sc.id
		WHERE sd.project_id = $1
		ORDER BY sd.created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.CampaignID, &d.Domain,
			&d.SSLEnabled, &d.CreatedAt, &d.CampaignName); err != nil {
			continue
		}
		list = append(list, d)
	}
	if list == nil {
		list = []Domain{}
	}
	return list, nil
}

func (s *Service) CreateDomain(ctx context.Context, d *Domain) (*Domain, error) {
	d.ID = uuid.New()
	d.CreatedAt = time.Now()
	_, err := s.db.Exec(ctx, `
		INSERT INTO shield_domains (id, project_id, campaign_id, domain, ssl_enabled)
		VALUES ($1,$2,$3,$4,$5)`,
		d.ID, d.ProjectID, d.CampaignID, d.Domain, d.SSLEnabled,
	)
	if err != nil {
		return nil, err
	}
	if s.rdb != nil {
		_ = s.rdb.Del(ctx, "shield_domain:"+d.Domain)
	}
	return d, nil
}

// IsDomainSSLEnabled verifica se o domínio está cadastrado E com ssl_enabled=true.
// Usado pelo endpoint ask do Caddy On-Demand TLS — retorna false para domínios
// desconhecidos ou sem SSL, impedindo emissão não autorizada de certificados.
func (s *Service) IsDomainSSLEnabled(ctx context.Context, domain string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM shield_domains WHERE domain=$1 AND ssl_enabled=true)`,
		domain,
	).Scan(&ok)
	return ok
}

func (s *Service) DeleteDomain(ctx context.Context, id, projectID uuid.UUID) error {
	var domain string
	_ = s.db.QueryRow(ctx,
		`SELECT domain FROM shield_domains WHERE id=$1 AND project_id=$2`, id, projectID,
	).Scan(&domain)

	_, err := s.db.Exec(ctx,
		`DELETE FROM shield_domains WHERE id=$1 AND project_id=$2`, id, projectID)
	if err == nil && domain != "" && s.rdb != nil {
		_ = s.rdb.Del(ctx, "shield_domain:"+domain)
	}
	return err
}

// ResolveCampaignByDomain localiza a campanha associada ao hostname (com cache Redis 5min).
func (s *Service) ResolveCampaignByDomain(ctx context.Context, domain string) (*Campaign, error) {
	cacheKey := "shield_domain:" + domain
	if s.rdb != nil {
		if data, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
			var c Campaign
			if json.Unmarshal(data, &c) == nil {
				return &c, nil
			}
		}
	}

	var c Campaign
	err := s.db.QueryRow(ctx, `
		SELECT sc.id, sc.project_id, sc.name, sc.safe_url, sc.money_url, sc.split_pct,
		       sc.enabled, sc.created_at, sc.updated_at
		FROM shield_campaigns sc
		JOIN shield_domains sd ON sc.id = sd.campaign_id
		WHERE sd.domain = $1 AND sc.enabled = true`, domain,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.SafeURL, &c.MoneyURL, &c.SplitPct,
		&c.Enabled, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if s.rdb != nil {
		if data, err := json.Marshal(c); err == nil {
			_ = s.rdb.Set(ctx, cacheKey, data, 5*time.Minute)
		}
	}
	return &c, nil
}

// ResolveCampaignBySlug localiza a campanha pelo slug (com cache Redis 5min).
func (s *Service) ResolveCampaignBySlug(ctx context.Context, slug string) (*Campaign, error) {
	if slug == "" {
		return nil, nil
	}
	cacheKey := "shield_slug:" + slug
	if s.rdb != nil {
		if data, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
			var c Campaign
			if json.Unmarshal(data, &c) == nil {
				return &c, nil
			}
		}
	}

	var c Campaign
	err := s.db.QueryRow(ctx, `
		SELECT id, project_id, name, slug, safe_url, money_url, split_pct, enabled,
		       platform, challenge_mode, require_key, access_key, created_at, updated_at
		FROM shield_campaigns
		WHERE slug = $1 AND enabled = true`, slug,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.Slug, &c.SafeURL, &c.MoneyURL,
		&c.SplitPct, &c.Enabled, &c.Platform, &c.ChallengeMode,
		&c.RequireKey, &c.AccessKey, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	if s.rdb != nil {
		if data, err := json.Marshal(c); err == nil {
			_ = s.rdb.Set(ctx, cacheKey, data, 5*time.Minute)
		}
	}
	return &c, nil
}

// InvalidateSlugCache remove o cache de um slug (chamar após update/delete).
func (s *Service) InvalidateSlugCache(ctx context.Context, slug string) {
	if s.rdb != nil && slug != "" {
		_ = s.rdb.Del(ctx, "shield_slug:"+slug)
	}
}

// ── A/B Split ─────────────────────────────────────────────────────────────

// ChooseURL retorna money_url para split_pct% dos visitantes (hash por IP para consistência).
// Visitantes bloqueados recebem safe_url independentemente.
func ChooseURL(campaign *Campaign, ip string) (url string, isMoney bool) {
	if campaign.MoneyURL == "" {
		return campaign.SafeURL, false
	}
	h := fnv.New32a()
	h.Write([]byte(ip))
	bucket := int(h.Sum32() % 100)
	if bucket < campaign.SplitPct {
		return campaign.MoneyURL, true
	}
	return campaign.SafeURL, false
}
