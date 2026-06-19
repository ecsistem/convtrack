package shield

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
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

// ClickIDParam retorna o nome do parâmetro de click-id esperado para a
// plataforma. Vazio = plataforma sem click-id padrão.
func ClickIDParam(platform string) string {
	switch strings.ToLower(platform) {
	case "meta", "facebook", "instagram":
		return "fbclid"
	case "google", "youtube":
		return "gclid"
	case "tiktok":
		return "ttclid"
	case "kwai":
		return "clickid"
	default:
		return ""
	}
}

// effectivePlatforms retorna as plataformas da campanha (platforms[], com
// fallback para o campo singular platform).
func (c *Campaign) effectivePlatforms() []string {
	if len(c.Platforms) > 0 {
		return c.Platforms
	}
	if c.Platform != "" {
		return []string{c.Platform}
	}
	return nil
}

// platformToSource normaliza uma plataforma da campanha para o vocabulário do
// detectSource ("meta"/"tiktok"/"kwai"/"google").
func platformToSource(platform string) string {
	switch strings.ToLower(platform) {
	case "meta", "facebook", "instagram":
		return "meta"
	case "google", "youtube":
		return "google"
	case "tiktok":
		return "tiktok"
	case "kwai":
		return "kwai"
	default:
		return ""
	}
}

// OriginMatches verifica se o Referer indica uma das plataformas da campanha,
// ignorando o utm_source. Usado quando origin_only está ativo. Retorna false
// para origem desconhecida/ausente (clique sem Referer não pode ser validado).
func (c *Campaign) OriginMatches(referer string) bool {
	src := detectSource(referer, "") // só o Referer, sem utm
	if src == "direct" {
		return false
	}
	for _, p := range c.effectivePlatforms() {
		if platformToSource(p) == src {
			return true
		}
	}
	return false
}

// ClickIDParams retorna a lista de parâmetros de click-id aceitos pela
// campanha (um por plataforma selecionada), sem duplicatas.
func (c *Campaign) ClickIDParams() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.effectivePlatforms() {
		if param := ClickIDParam(p); param != "" && !seen[param] {
			seen[param] = true
			out = append(out, param)
		}
	}
	return out
}

// normalizePlatforms garante que platforms não é nil e que platform (singular)
// espelha a primeira plataforma — mantendo compatibilidade.
func (c *Campaign) normalizePlatforms() {
	if c.Platforms == nil {
		c.Platforms = []string{}
	}
	// se só veio o singular, deriva o array
	if len(c.Platforms) == 0 && c.Platform != "" {
		c.Platforms = []string{c.Platform}
	}
	// platform singular reflete a primeira da lista
	if len(c.Platforms) > 0 {
		c.Platform = c.Platforms[0]
	}
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
	Platform       string    `json:"platform"        db:"platform"`        // primária (compat) — espelha platforms[0]
	Platforms      []string  `json:"platforms"       db:"platforms"`       // múltiplas fontes: meta|tiktok|kwai|google
	ChallengeMode  string    `json:"challenge_mode"  db:"challenge_mode"`  // redirect|captcha|both
	RequireKey     bool      `json:"require_key"     db:"require_key"`     // exige ?_sk=access_key
	AccessKey      string    `json:"access_key"      db:"access_key"`      // chave secreta gerada
	UnderReview    bool      `json:"under_review"    db:"under_review"`    // modo análise: todos veem safe_url
	RequireTtclid  bool      `json:"require_ttclid"  db:"require_ttclid"`  // legado: exige ?ttclid= (TikTok)
	RequireClickID bool      `json:"require_clickid" db:"require_clickid"` // exige o click-id da plataforma (fbclid/gclid/ttclid/clickid)
	OriginOnly     bool      `json:"origin_only"     db:"origin_only"`     // valida a origem real (Referer) e ignora o utm_source
	CreatedAt      time.Time `json:"created_at"      db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"      db:"updated_at"`
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
	originalSlug := sanitizeSlug(c.Slug)
	c.Slug = originalSlug
	if c.ChallengeMode == "" {
		c.ChallengeMode = "redirect"
	}
	c.normalizePlatforms()

	const maxAttempts = 6
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 && originalSlug != "" {
			c.Slug = randomSlugPrefix() + "-" + originalSlug
		}
		_, err := s.db.Exec(ctx, `
			INSERT INTO shield_campaigns
			  (id, project_id, name, slug, safe_url, money_url, split_pct, enabled,
			   platform, platforms, challenge_mode, require_key, access_key, under_review, require_ttclid, require_clickid, origin_only)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			c.ID, c.ProjectID, c.Name, c.Slug, c.SafeURL, c.MoneyURL, c.SplitPct, c.Enabled,
			c.Platform, c.Platforms, c.ChallengeMode, c.RequireKey, c.AccessKey, c.UnderReview, c.RequireTtclid, c.RequireClickID, c.OriginOnly,
		)
		if err == nil {
			c.CreatedAt = time.Now()
			c.UpdatedAt = c.CreatedAt
			return c, nil
		}
		if isUniqueSlugViolation(err) && originalSlug != "" {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("não foi possível gerar um slug único para '%s'", originalSlug)
}

// randomSlugPrefix gera um prefixo aleatório de 5 caracteres alfanuméricos.
func randomSlugPrefix() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 5)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}

// isUniqueSlugViolation verifica se o erro é violação de unique constraint no slug.
func isUniqueSlugViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		strings.Contains(pgErr.ConstraintName, "slug")
}

func (s *Service) ListCampaigns(ctx context.Context, projectID uuid.UUID) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, project_id, name, slug, safe_url, money_url, split_pct, enabled,
		       platform, platforms, challenge_mode, require_key, access_key, under_review, require_ttclid, require_clickid, origin_only,
		       created_at, updated_at
		FROM shield_campaigns WHERE project_id = $1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Campaign
	for rows.Next() {
		var c Campaign
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Slug, &c.SafeURL, &c.MoneyURL,
			&c.SplitPct, &c.Enabled, &c.Platform, &c.Platforms, &c.ChallengeMode,
			&c.RequireKey, &c.AccessKey, &c.UnderReview, &c.RequireTtclid, &c.RequireClickID, &c.OriginOnly,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
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
	c.normalizePlatforms()
	_, err := s.db.Exec(ctx, `
		UPDATE shield_campaigns
		SET name=$1, slug=$2, safe_url=$3, money_url=$4, split_pct=$5, enabled=$6,
		    platform=$7, platforms=$8, challenge_mode=$9, require_key=$10, access_key=$11,
		    under_review=$12, require_ttclid=$13, require_clickid=$14, origin_only=$15, updated_at=NOW()
		WHERE id=$16 AND project_id=$17`,
		c.Name, c.Slug, c.SafeURL, c.MoneyURL, c.SplitPct, c.Enabled,
		c.Platform, c.Platforms, c.ChallengeMode, c.RequireKey, c.AccessKey,
		c.UnderReview, c.RequireTtclid, c.RequireClickID, c.OriginOnly, c.ID, c.ProjectID,
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

// ErrDomainOwnedByOther indica que o domínio já pertence a outro projeto.
var ErrDomainOwnedByOther = fmt.Errorf("domínio já cadastrado por outro projeto")

// CreateDomain cria um domínio OU, se já existir no mesmo projeto, reassocia-o
// à campanha informada (upsert). Permite "escolher um domínio configurado"
// direto da campanha, inclusive no cadastro. Se o domínio pertencer a outro
// projeto, retorna ErrDomainOwnedByOther.
func (s *Service) CreateDomain(ctx context.Context, d *Domain) (*Domain, error) {
	d.ID = uuid.New()
	d.CreatedAt = time.Now()
	var returnedID uuid.UUID
	err := s.db.QueryRow(ctx, `
		INSERT INTO shield_domains (id, project_id, campaign_id, domain, ssl_enabled)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (domain) DO UPDATE
		  SET campaign_id = EXCLUDED.campaign_id,
		      ssl_enabled = EXCLUDED.ssl_enabled
		  WHERE shield_domains.project_id = EXCLUDED.project_id
		RETURNING id`,
		d.ID, d.ProjectID, d.CampaignID, d.Domain, d.SSLEnabled,
	).Scan(&returnedID)
	if err != nil {
		// Sem linha retornada = conflito com domínio de outro projeto.
		if err.Error() == "no rows in result set" {
			return nil, ErrDomainOwnedByOther
		}
		return nil, err
	}
	d.ID = returnedID
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
		       platform, platforms, challenge_mode, require_key, access_key,
		       under_review, require_ttclid, require_clickid, origin_only, created_at, updated_at
		FROM shield_campaigns
		WHERE slug = $1 AND enabled = true`, slug,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.Slug, &c.SafeURL, &c.MoneyURL,
		&c.SplitPct, &c.Enabled, &c.Platform, &c.Platforms, &c.ChallengeMode,
		&c.RequireKey, &c.AccessKey, &c.UnderReview, &c.RequireTtclid, &c.RequireClickID, &c.OriginOnly,
		&c.CreatedAt, &c.UpdatedAt)
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
