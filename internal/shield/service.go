package shield

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ecsistem/convtrack/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Service executa as verificações de proteção e persiste configurações/logs.
type Service struct {
	db      *pgxpool.Pool
	rdb     *redis.Client
	APIBase string // ex: "https://api.convtrack.com" — usado pelo proxy para injetar o script
}

// New cria um novo shield.Service.
func New(db *pgxpool.Pool, rdb *redis.Client) *Service {
	return &Service{db: db, rdb: rdb}
}

// DB expõe o pool de banco de dados para handlers que precisam de queries ad-hoc.
func (s *Service) DB() *pgxpool.Pool { return s.db }

// ── Config ────────────────────────────────────────────────────────────────

// GetConfig retorna a configuração de shield do projeto (cria default se não existe).
func (s *Service) GetConfig(ctx context.Context, projectID uuid.UUID) (*models.ShieldConfig, error) {
	cfg := &models.ShieldConfig{}
	row := s.db.QueryRow(ctx, `
		SELECT project_id::text, enabled, block_bots, block_headless, block_spy_tools,
		       block_vpn, block_datacenter, anti_devtools,
		       geo_mode, geo_countries, device_filter,
		       redirect_url, primary_url, fallback_urls, blocked_ips,
		       block_direct, allowed_sources,
		       updated_at::text
		FROM shield_configs WHERE project_id = $1`, projectID)

	var geoCountries, fallbackURLs, blockedIPs, allowedSources []string
	err := row.Scan(
		&cfg.ProjectID, &cfg.Enabled, &cfg.BlockBots, &cfg.BlockHeadless, &cfg.BlockSpyTools,
		&cfg.BlockVPN, &cfg.BlockDatacenter, &cfg.AntiDevTools,
		&cfg.GeoMode, &geoCountries, &cfg.DeviceFilter,
		&cfg.RedirectURL, &cfg.PrimaryURL, &fallbackURLs, &blockedIPs,
		&cfg.BlockDirect, &allowedSources,
		&cfg.UpdatedAt,
	)
	if err != nil {
		// cria default
		_, err2 := s.db.Exec(ctx,
			`INSERT INTO shield_configs (project_id) VALUES ($1) ON CONFLICT DO NOTHING`, projectID)
		if err2 != nil {
			return nil, err2
		}
		cfg.ProjectID = projectID.String()
		cfg.GeoMode = "disabled"
		cfg.DeviceFilter = "all"
		cfg.BlockBots = true
		cfg.BlockHeadless = true
		cfg.BlockSpyTools = true
		cfg.GeoCountries = []string{}
		cfg.FallbackURLs = []string{}
		cfg.BlockedIPs = []string{}
		cfg.AllowedSources = []string{}
		return cfg, nil
	}

	cfg.GeoCountries = geoCountries
	cfg.FallbackURLs = fallbackURLs
	cfg.BlockedIPs = blockedIPs
	cfg.AllowedSources = allowedSources
	return cfg, nil
}

// UpsertConfig salva/atualiza a configuração.
func (s *Service) UpsertConfig(ctx context.Context, projectID uuid.UUID, cfg *models.ShieldConfig) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO shield_configs (
			project_id, enabled, block_bots, block_headless, block_spy_tools,
			block_vpn, block_datacenter, anti_devtools,
			geo_mode, geo_countries, device_filter,
			redirect_url, primary_url, fallback_urls, blocked_ips,
			block_direct, allowed_sources, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,NOW())
		ON CONFLICT (project_id) DO UPDATE SET
			enabled          = EXCLUDED.enabled,
			block_bots       = EXCLUDED.block_bots,
			block_headless   = EXCLUDED.block_headless,
			block_spy_tools  = EXCLUDED.block_spy_tools,
			block_vpn        = EXCLUDED.block_vpn,
			block_datacenter = EXCLUDED.block_datacenter,
			anti_devtools    = EXCLUDED.anti_devtools,
			geo_mode         = EXCLUDED.geo_mode,
			geo_countries    = EXCLUDED.geo_countries,
			device_filter    = EXCLUDED.device_filter,
			redirect_url     = EXCLUDED.redirect_url,
			primary_url      = EXCLUDED.primary_url,
			fallback_urls    = EXCLUDED.fallback_urls,
			blocked_ips      = EXCLUDED.blocked_ips,
			block_direct     = EXCLUDED.block_direct,
			allowed_sources  = EXCLUDED.allowed_sources,
			updated_at       = NOW()`,
		projectID,
		cfg.Enabled, cfg.BlockBots, cfg.BlockHeadless, cfg.BlockSpyTools,
		cfg.BlockVPN, cfg.BlockDatacenter, cfg.AntiDevTools,
		cfg.GeoMode, cfg.GeoCountries, cfg.DeviceFilter,
		cfg.RedirectURL, cfg.PrimaryURL, cfg.FallbackURLs, cfg.BlockedIPs,
		cfg.BlockDirect, cfg.AllowedSources,
	)
	return err
}

// ── Logs ─────────────────────────────────────────────────────────────────

// ListLogs retorna os últimos logs de shield do projeto.
func (s *Service) ListLogs(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]models.ShieldLog, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var total int
	_ = s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM shield_logs WHERE project_id = $1`, projectID,
	).Scan(&total)

	rows, err := s.db.Query(ctx, `
		SELECT id::text, project_id::text, ip, user_agent, country, device,
		       reason, action, redirect_to, created_at::text
		FROM shield_logs
		WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, projectID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []models.ShieldLog
	for rows.Next() {
		var l models.ShieldLog
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.IP, &l.UserAgent, &l.Country,
			&l.Device, &l.Reason, &l.Action, &l.RedirectTo, &l.CreatedAt); err != nil {
			continue
		}
		logs = append(logs, l)
	}
	if logs == nil {
		logs = []models.ShieldLog{}
	}
	return logs, total, nil
}

// insertLog persiste o log e publica no Redis para o SSE.
func (s *Service) insertLog(ctx context.Context, l *models.ShieldLog, projectID uuid.UUID) {
	_, err := s.db.Exec(ctx, `
		INSERT INTO shield_logs (project_id, ip, user_agent, country, device, reason, action, redirect_to)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		projectID, l.IP, l.UserAgent, l.Country, l.Device, l.Reason, l.Action, l.RedirectTo,
	)
	if err != nil {
		return
	}
	// Publica no canal Redis para SSE
	data, _ := json.Marshal(l)
	_ = s.rdb.Publish(ctx, fmt.Sprintf("shield:%s", projectID), string(data))
}

// ── Check ─────────────────────────────────────────────────────────────────

// CheckRequest contém os dados de uma requisição a ser verificada.
type CheckRequest struct {
	IP           string
	UserAgent    string
	// Sinais do cliente (enviados pelo tracker.js)
	WebDriver    bool
	HeadlessHint bool // cliente detectou sinais de headless
	DevTools     bool
	// SkipVisit impede o registro em shield_visits.
	// Use true quando o Check é um pré-filtro rápido seguido de ProcessFingerprint
	// (ex: SmartRedirectAdvanced), para evitar dupla contagem.
	SkipVisit bool
	// Fontes de tráfego
	Referer   string // cabeçalho HTTP Referer
	UTMSource string // parâmetro ?utm_source=
}

// detectSource identifica a plataforma de origem a partir do Referer e utm_source.
// Retorna "meta", "tiktok", "kwai", "google" ou "direct".
func detectSource(referer, utmSource string) string {
	utm := strings.ToLower(utmSource)
	ref := strings.ToLower(referer)

	// UTM source tem prioridade — é o que o anunciante configura manualmente
	switch {
	case strings.Contains(utm, "facebook") || strings.Contains(utm, "meta") ||
		strings.Contains(utm, "fb") || strings.Contains(utm, "instagram"):
		return "meta"
	case strings.Contains(utm, "tiktok") || utm == "ttk":
		return "tiktok"
	case strings.Contains(utm, "kwai"):
		return "kwai"
	case strings.Contains(utm, "google") || strings.Contains(utm, "adwords") || strings.Contains(utm, "gads"):
		return "google"
	}

	// Fallback: Referer do navegador
	switch {
	case strings.Contains(ref, "facebook.com") || strings.Contains(ref, "fb.com") ||
		strings.Contains(ref, "instagram.com") || strings.Contains(ref, "l.facebook.com"):
		return "meta"
	case strings.Contains(ref, "tiktok.com") || strings.Contains(ref, "vm.tiktok.com"):
		return "tiktok"
	case strings.Contains(ref, "kwai.com") || strings.Contains(ref, "kwai.net") ||
		strings.Contains(ref, "kw.com"):
		return "kwai"
	case strings.Contains(ref, "google.com") || strings.Contains(ref, "google.") ||
		strings.Contains(ref, "googleadservices.com") || strings.Contains(ref, "doubleclick.net"):
		return "google"
	}

	return "direct"
}

// CheckResult é o resultado da verificação.
type CheckResult struct {
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
	Action      string `json:"action,omitempty"`      // "blocked" | "redirected"
	RedirectURL string `json:"redirect_url,omitempty"`
	AntiDevTools bool  `json:"anti_devtools"`
}

// Check executa todas as verificações de proteção para um projeto.
func (s *Service) Check(ctx context.Context, projectID uuid.UUID, req CheckRequest) (*CheckResult, error) {
	cfg, err := s.GetConfig(ctx, projectID)
	if err != nil {
		return &CheckResult{Allowed: true}, nil
	}

	if !cfg.Enabled {
		return &CheckResult{Allowed: true, AntiDevTools: cfg.AntiDevTools}, nil
	}

	ua := req.UserAgent

	// ── Verificações client-side ────────────────────────────────────────
	if req.WebDriver {
		return s.block(ctx, projectID, cfg, req, "webdriver"), nil
	}
	if req.HeadlessHint && cfg.BlockHeadless {
		return s.block(ctx, projectID, cfg, req, "headless"), nil
	}
	if req.DevTools && cfg.AntiDevTools {
		return s.block(ctx, projectID, cfg, req, "devtools"), nil
	}

	// ── UA-based (server-side) ──────────────────────────────────────────
	if cfg.BlockBots && isBot(ua) {
		return s.block(ctx, projectID, cfg, req, "bot"), nil
	}
	if cfg.BlockSpyTools && isSpyTool(ua) {
		return s.block(ctx, projectID, cfg, req, "spy_tool"), nil
	}
	if cfg.BlockHeadless && isHeadless(ua) {
		return s.block(ctx, projectID, cfg, req, "headless"), nil
	}

	// ── IP blocklist ────────────────────────────────────────────────────
	for _, blocked := range cfg.BlockedIPs {
		if blocked != "" && req.IP == blocked {
			return s.block(ctx, projectID, cfg, req, "ip_blocked"), nil
		}
	}

	// ── Device filter ───────────────────────────────────────────────────
	if cfg.DeviceFilter != "all" {
		dev := deviceFromUA(ua)
		if cfg.DeviceFilter == "mobile" && dev == "desktop" {
			return s.block(ctx, projectID, cfg, req, "device"), nil
		}
		if cfg.DeviceFilter == "desktop" && dev == "mobile" {
			return s.block(ctx, projectID, cfg, req, "device"), nil
		}
	}

	// ── IP intelligence (GEO + VPN + datacenter) ──────────────────────
	// Sempre chamado para coletar geo data (cidade/lat/lon) para o globe.
	// Cache Redis de 1h — custo zero em visitas repetidas do mesmo IP.
	ipInfo := s.lookupIP(ctx, req.IP)

	if cfg.BlockVPN && ipInfo.Proxy {
		return s.block(ctx, projectID, cfg, req, "vpn"), nil
	}
	if cfg.BlockDatacenter && ipInfo.Hosting {
		return s.block(ctx, projectID, cfg, req, "datacenter"), nil
	}

	if cfg.GeoMode == "allowlist" && ipInfo.Country != "" {
		allowed := false
		for _, c := range cfg.GeoCountries {
			if strings.EqualFold(c, ipInfo.Country) {
				allowed = true
				break
			}
		}
		if !allowed {
			req.UserAgent = ua // preserve
			result := s.block(ctx, projectID, cfg, req, "geo")
			result.Reason = fmt.Sprintf("geo:%s", ipInfo.Country)
			return result, nil
		}
	}
	if cfg.GeoMode == "blocklist" {
		for _, c := range cfg.GeoCountries {
			if strings.EqualFold(c, ipInfo.Country) {
				result := s.block(ctx, projectID, cfg, req, "geo")
				result.Reason = fmt.Sprintf("geo:%s", ipInfo.Country)
				return result, nil
			}
		}
	}

	// ── Fontes de tráfego ──────────────────────────────────────────────
	// Se block_direct=true, apenas tráfego das plataformas configuradas passa.
	// Tráfego "direto" (sem Referer/UTM reconhecido) vai para safe_url.
	if cfg.BlockDirect && len(cfg.AllowedSources) > 0 {
		src := detectSource(req.Referer, req.UTMSource)
		srcAllowed := false
		for _, allowed := range cfg.AllowedSources {
			if strings.EqualFold(allowed, src) {
				srcAllowed = true
				break
			}
		}
		if !srcAllowed {
			result := s.block(ctx, projectID, cfg, req, "direct_traffic")
			result.Reason = fmt.Sprintf("source:%s", src)
			return result, nil
		}
	}

	// Visita permitida — registra em shield_visits para métricas do dashboard.
	// Omite quando SkipVisit=true (ex: pré-filtro rápido do SmartRedirectAdvanced
	// que é seguido de ProcessFingerprint, o qual registra a visita completa).
	if !req.SkipVisit {
		go s.insertVisit(context.Background(), projectID, VisitRecord{
			IP:        req.IP,
			UserAgent: req.UserAgent,
			Country:   ipInfo.Country,
			City:      ipInfo.City,
			Lat:       ipInfo.Lat,
			Lon:       ipInfo.Lon,
			Device:    deviceFromUA(req.UserAgent),
			IsBot:     false,
			BotScore:  0,
			Signals:   []string{},
			Action:    "money",
		})
		// Dispara evento "visit" para webhooks que assinam este evento
		go s.FireWebhooks(context.Background(), projectID, EventVisit, map[string]interface{}{
			"ip":         req.IP,
			"device":     deviceFromUA(req.UserAgent),
			"user_agent": req.UserAgent,
		})
	}

	return &CheckResult{Allowed: true, AntiDevTools: cfg.AntiDevTools}, nil
}

// block monta o resultado de bloqueio, loga, registra em shield_visits e dispara webhooks.
func (s *Service) block(ctx context.Context, projectID uuid.UUID, cfg *models.ShieldConfig, req CheckRequest, reason string) *CheckResult {
	action := "blocked"
	redirectURL := cfg.RedirectURL

	if redirectURL != "" {
		action = "redirected"
	}

	device := deviceFromUA(req.UserAgent)

	log := &models.ShieldLog{
		IP:         req.IP,
		UserAgent:  req.UserAgent,
		Device:     device,
		Reason:     reason,
		Action:     action,
		RedirectTo: redirectURL,
	}
	go s.insertLog(context.Background(), log, projectID)
	go s.insertVisit(context.Background(), projectID, VisitRecord{
		IP:        req.IP,
		UserAgent: req.UserAgent,
		Device:    device,
		IsBot:     true,
		BotScore:  0.9,
		Signals:   []string{reason},
		Action:    action,
		DestURL:   redirectURL,
	})
	// ── CRÍTICO: dispara webhooks "bot_detected" para TODOS os entry points ──
	// (antes só era chamado em ProcessFingerprint, ignorando tracker.js e SlugCloak)
	go s.FireWebhooks(context.Background(), projectID, EventBotDetected, map[string]interface{}{
		"ip":         req.IP,
		"reason":     reason,
		"action":     action,
		"device":     device,
		"user_agent": req.UserAgent,
		"redirect":   redirectURL,
	})

	return &CheckResult{
		Allowed:      false,
		Reason:       reason,
		Action:       action,
		RedirectURL:  redirectURL,
		AntiDevTools: cfg.AntiDevTools,
	}
}

// VisitRecord representa os dados de uma visita a ser persistida em shield_visits.
type VisitRecord struct {
	IP        string
	UserAgent string
	Country   string
	City      string
	Lat       float64
	Lon       float64
	Device    string
	IsBot     bool
	BotScore  float64
	Signals   []string
	Action    string // "money" | "safe" | "blocked" | "redirected"
	DestURL   string
}

// insertVisit persiste um registro em shield_visits de forma assíncrona.
// Chamado para TODOS os visitantes (humanos e bots) de todos os entry points.
func (s *Service) insertVisit(ctx context.Context, projectID uuid.UUID, v VisitRecord) {
	if v.Signals == nil {
		v.Signals = []string{}
	}
	if v.Device == "" {
		v.Device = deviceFromUA(v.UserAgent)
	}
	_, _ = s.db.Exec(ctx, `
		INSERT INTO shield_visits
		    (project_id, ip, user_agent, country, city, lat, lon, device, is_bot, bot_score, signals, action, dest_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		projectID, v.IP, v.UserAgent, v.Country, v.City, v.Lat, v.Lon, v.Device,
		v.IsBot, v.BotScore, v.Signals, v.Action, v.DestURL,
	)
}

// ── IP Intelligence ──────────────────────────────────────────────────────

type ipAPIResult struct {
	Status  string  `json:"status"`
	Country string  `json:"countryCode"`
	City    string  `json:"city"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Proxy   bool    `json:"proxy"`
	Hosting bool    `json:"hosting"`
}

// lookupIP consulta ip-api.com com cache Redis de 1h.
func (s *Service) lookupIP(ctx context.Context, ip string) ipAPIResult {
	if ip == "" || ip == "127.0.0.1" || ip == "::1" ||
		strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") ||
		strings.HasPrefix(ip, "172.") {
		return ipAPIResult{}
	}

	cacheKey := "shield_ip:" + ip
	if s.rdb != nil {
		if cached, err := s.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
			var r ipAPIResult
			if json.Unmarshal(cached, &r) == nil {
				return r
			}
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://ip-api.com/json/%s?fields=status,countryCode,city,lat,lon,proxy,hosting", ip)
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return ipAPIResult{}
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil || resp.StatusCode != 200 {
		return ipAPIResult{}
	}
	defer resp.Body.Close()

	var result ipAPIResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Status != "success" {
		return ipAPIResult{}
	}

	// Cache por 1 hora
	if s.rdb != nil {
		if data, err := json.Marshal(result); err == nil {
			_ = s.rdb.Set(ctx, cacheKey, data, time.Hour)
		}
	}

	return result
}

// ClearLogs apaga todos os logs de shield do projeto.
func (s *Service) ClearLogs(ctx context.Context, projectID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM shield_logs WHERE project_id = $1`, projectID)
	return err
}

// StatRow agrupa contagem por motivo.
type StatRow struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// StatsLogs retorna contagem de bloqueios agrupados por reason (últimos 7 dias).
func (s *Service) StatsLogs(ctx context.Context, projectID uuid.UUID) ([]StatRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT reason, COUNT(*) AS cnt
		FROM shield_logs
		WHERE project_id = $1 AND created_at > NOW() - INTERVAL '7 days'
		GROUP BY reason ORDER BY cnt DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []StatRow
	for rows.Next() {
		var r StatRow
		if err := rows.Scan(&r.Reason, &r.Count); err != nil {
			continue
		}
		stats = append(stats, r)
	}
	if stats == nil {
		stats = []StatRow{}
	}
	return stats, nil
}

// ── Smart Redirect (server-side cloaking link) ───────────────────────────

// SmartRedirect retorna a URL para onde o visitante deve ser redirecionado
// após verificação server-side (sem JS). Usado pelo endpoint /r/:projectKey.
func (s *Service) SmartRedirect(ctx context.Context, projectID uuid.UUID, req CheckRequest) (destination string, blocked bool) {
	result, _ := s.Check(ctx, projectID, req)
	if result == nil || result.Allowed {
		cfg, _ := s.GetConfig(ctx, projectID)
		if cfg != nil && cfg.PrimaryURL != "" {
			return cfg.PrimaryURL, false
		}
		return "", false
	}
	// bloqueado
	return result.RedirectURL, true
}

// ── Geo Stats ──────────────────────────────────────────────────────────────

// GeoLocation agrega visitas por cidade para exibição no globe.
type GeoLocation struct {
	City    string  `json:"city"`
	Country string  `json:"country"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Count   int     `json:"count"`
}

// GetGeoStats retorna as top cidades de onde visitantes acessaram (últimos 30 dias),
// ordenadas por volume, excluindo registros sem cidade (bots/IPs locais).
func (s *Service) GetGeoStats(ctx context.Context, projectID uuid.UUID, days int) ([]GeoLocation, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := s.db.Query(ctx, `
		SELECT city, country, AVG(lat)::float, AVG(lon)::float, COUNT(*) AS cnt
		FROM shield_visits
		WHERE project_id = $1
		  AND city != ''
		  AND created_at >= NOW() - ($2 || ' days')::INTERVAL
		GROUP BY city, country
		ORDER BY cnt DESC
		LIMIT 60
	`, projectID, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GeoLocation
	for rows.Next() {
		var g GeoLocation
		if err := rows.Scan(&g.City, &g.Country, &g.Lat, &g.Lon, &g.Count); err != nil {
			continue
		}
		out = append(out, g)
	}
	if out == nil {
		out = []GeoLocation{}
	}
	return out, nil
}
