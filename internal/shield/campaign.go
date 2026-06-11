package shield

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
)

// ── Campaign ──────────────────────────────────────────────────────────────

// Campaign define as URLs e parâmetros de uma campanha de proteção.
type Campaign struct {
	ID        uuid.UUID `json:"id"         db:"id"`
	ProjectID uuid.UUID `json:"project_id" db:"project_id"`
	Name      string    `json:"name"       db:"name"`
	SafeURL   string    `json:"safe_url"   db:"safe_url"`   // exibida para bots/revisores
	MoneyURL  string    `json:"money_url"  db:"money_url"`  // exibida para humanos reais
	SplitPct  int       `json:"split_pct"  db:"split_pct"`  // % de humanos → money_url
	Enabled   bool      `json:"enabled"    db:"enabled"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
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
	_, err := s.db.Exec(ctx, `
		INSERT INTO shield_campaigns (id, project_id, name, safe_url, money_url, split_pct, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		c.ID, c.ProjectID, c.Name, c.SafeURL, c.MoneyURL, c.SplitPct, c.Enabled,
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
		SELECT id, project_id, name, safe_url, money_url, split_pct, enabled, created_at, updated_at
		FROM shield_campaigns WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Campaign
	for rows.Next() {
		var c Campaign
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.SafeURL, &c.MoneyURL,
			&c.SplitPct, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
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
	_, err := s.db.Exec(ctx, `
		UPDATE shield_campaigns
		SET name=$1, safe_url=$2, money_url=$3, split_pct=$4, enabled=$5, updated_at=NOW()
		WHERE id=$6 AND project_id=$7`,
		c.Name, c.SafeURL, c.MoneyURL, c.SplitPct, c.Enabled, c.ID, c.ProjectID,
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
