package shield

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/ecsistem/convtrack/internal/models"
	"github.com/google/uuid"
)

// FingerprintData contém todos os sinais coletados pelo shield-fp.js.
type FingerprintData struct {
	// ── 13 sinais de browser fingerprint ──────────────────────────────
	CanvasHash    string   `json:"canvas_hash"`    // hash do canvas 2D
	WebGLVendor   string   `json:"webgl_vendor"`   // GPU vendor (NVIDIA, Mesa, SwiftShader…)
	WebGLRenderer string   `json:"webgl_renderer"` // GPU renderer string
	WebGLHash     string   `json:"webgl_hash"`     // hash da cena WebGL
	AudioHash     string   `json:"audio_hash"`     // hash do AudioContext fingerprint
	ScreenWidth   int      `json:"screen_width"`
	ScreenHeight  int      `json:"screen_height"`
	ColorDepth    int      `json:"color_depth"`
	PixelRatio    float64  `json:"pixel_ratio"`
	Timezone      string   `json:"timezone"`
	Language      string   `json:"language"`
	Platform      string   `json:"platform"`
	CPUCores      int      `json:"cpu_cores"`
	MemoryGB      int      `json:"memory_gb"`      // navigator.deviceMemory
	TouchPoints   int      `json:"touch_points"`
	WebRTCIPs     []string `json:"webrtc_ips"`     // IPs vazados pelo WebRTC
	FontsHash     string   `json:"fonts_hash"`     // hash da lista de fontes disponíveis
	Plugins       int      `json:"plugins"`        // navigator.plugins.length
	// ── Sinais de automação detectados pelo cliente ────────────────────
	WebDriver    bool `json:"webdriver"`      // navigator.webdriver
	HeadlessHint bool `json:"headless_hint"`  // UA com padrão headless
	DevTools     bool `json:"devtools"`       // DevTools detectado aberto
	// ── Contexto da sessão ───────────────────────────────────────────
	SessionHash string `json:"session_hash"`
	APIKey      string `json:"api_key"`
	CampaignID  string `json:"campaign_id,omitempty"`
}

// FingerprintResult é retornado ao cliente após o processamento.
type FingerprintResult struct {
	Allowed      bool     `json:"allowed"`
	BotScore     float64  `json:"bot_score"`
	IsBot        bool     `json:"is_bot"`
	IsSuspicious bool     `json:"is_suspicious"`
	Signals      []string `json:"signals,omitempty"`
	RedirectURL  string   `json:"redirect_url,omitempty"`
	Action       string   `json:"action"` // "money" | "safe" | "blocked" | "redirected"
	AntiDevTools bool     `json:"anti_devtools"`
	CampaignID   string   `json:"campaign_id,omitempty"`
}

// ProcessFingerprint analisa os sinais do cliente e retorna uma decisão.
func (s *Service) ProcessFingerprint(ctx context.Context, projectID uuid.UUID, ip, ua string, fp *FingerprintData) (*FingerprintResult, error) {
	cfg, _ := s.GetConfig(ctx, projectID)
	if cfg == nil || !cfg.Enabled {
		return &FingerprintResult{Allowed: true, Action: "money"}, nil
	}

	var signals []string

	// ── Sinais reportados pelo cliente ────────────────────────────────
	if fp.WebDriver {
		signals = append(signals, "webdriver")
	}
	if fp.HeadlessHint {
		signals = append(signals, "headless")
	}
	if fp.DevTools && cfg.AntiDevTools {
		signals = append(signals, "devtools")
	}

	// ── UA-based (server-side) ────────────────────────────────────────
	if cfg.BlockBots && isBot(ua) {
		signals = append(signals, "bot")
	}
	if cfg.BlockSpyTools && isSpyTool(ua) {
		signals = append(signals, "spy_tool")
	}
	if cfg.BlockHeadless && isHeadless(ua) {
		addUniq(&signals, "headless")
	}

	// ── Sinais de fingerprint ausente ────────────────────────────────
	if fp.CanvasHash == "" || fp.CanvasHash == "none" || fp.CanvasHash == "error" {
		signals = append(signals, "no_canvas")
	}
	if fp.WebGLVendor == "" || fp.WebGLVendor == "none" || fp.WebGLVendor == "Brian Paul" {
		// "Brian Paul" é o fallback de software renderer (Mesa/SwiftShader — indica headless)
		signals = append(signals, "no_webgl")
	}
	// Renderer de software (SwiftShader/llvmpipe/Mesa/ANGLE software) → headless.
	// GPUs reais retornam nomes de hardware (Apple/Adreno/Mali/NVIDIA/Intel/AMD).
	if isSoftwareRenderer(fp.WebGLRenderer) {
		signals = append(signals, "headless")
	}
	if fp.AudioHash == "" || fp.AudioHash == "none" || fp.AudioHash == "error" {
		signals = append(signals, "no_audio")
	}

	// ── Anomalias de tela ─────────────────────────────────────────────
	// Headless Chrome default: 800×600 ou 0×0
	if (fp.ScreenWidth == 800 && fp.ScreenHeight == 600) ||
		(fp.ScreenWidth == 1280 && fp.ScreenHeight == 720 && fp.PixelRatio == 1.0) ||
		(fp.ScreenWidth == 0 || fp.ScreenHeight == 0) {
		signals = append(signals, "suspicious_screen")
	}

	// Sem plugins (automações geralmente têm 0 plugins mesmo simulando Chrome)
	if fp.Plugins == 0 && fp.CanvasHash != "" {
		signals = append(signals, "no_plugins")
	}

	// ── WebRTC VPN/datacenter leak ────────────────────────────────────
	for _, rtcIP := range fp.WebRTCIPs {
		if isKnownDatacenterPrefix(rtcIP) {
			signals = append(signals, "webrtc_vpn")
			break
		}
	}

	// ── IP blocklist manual ───────────────────────────────────────────
	for _, blocked := range cfg.BlockedIPs {
		if blocked != "" && ip == blocked {
			signals = append(signals, "ip_blocked")
			break
		}
	}

	// ── Device filter ─────────────────────────────────────────────────
	if cfg.DeviceFilter != "all" {
		dev := deviceFromUA(ua)
		if cfg.DeviceFilter == "mobile" && dev == "desktop" {
			signals = append(signals, "device")
		}
		if cfg.DeviceFilter == "desktop" && dev == "mobile" {
			signals = append(signals, "device")
		}
	}

	// ── Faixas estáticas de revisor/datacenter/VPN (não dependem do ip-api) ──
	if cfg.BlockBots && isReviewerIP(ip) {
		signals = append(signals, "reviewer_range")
	}

	// ── IP intelligence (async; usa cache Redis) ──────────────────────
	var country string
	if cfg.BlockVPN || cfg.BlockDatacenter || cfg.GeoMode != "disabled" {
		ipInfo := s.lookupIP(ctx, ip)
		country = ipInfo.Country // captura país para shield_visits
		if cfg.BlockVPN && (ipInfo.Proxy || isVPNIP(ip) || isVPNASN(ipInfo.As)) {
			signals = append(signals, "vpn")
		}
		if cfg.BlockDatacenter && (ipInfo.Hosting || isDatacenterIP(ip) || isDatacenterASN(ipInfo.As)) {
			signals = append(signals, "datacenter")
		}
		if cfg.GeoMode == "allowlist" && ipInfo.Country != "" {
			if !containsCI(cfg.GeoCountries, ipInfo.Country) {
				signals = append(signals, "geo")
			}
		}
		if cfg.GeoMode == "blocklist" && ipInfo.Country != "" {
			if containsCI(cfg.GeoCountries, ipInfo.Country) {
				signals = append(signals, "geo")
			}
		}
	}

	// ── ML Score ──────────────────────────────────────────────────────
	botScore := ComputeBotScore(signals)
	isBot := IsBot(botScore, DefaultThreshold)
	isSuspicious := IsSuspicious(botScore)

	// ── Hash combinado do fingerprint ─────────────────────────────────
	raw := strings.Join([]string{
		fp.CanvasHash, fp.WebGLHash, fp.AudioHash, fp.FontsHash,
		fp.Timezone, fp.Language, fp.Platform,
		fmt.Sprintf("%d|%d|%d|%.2f|%d|%d", fp.CPUCores, fp.MemoryGB,
			fp.ScreenWidth, fp.PixelRatio, fp.ColorDepth, fp.TouchPoints),
	}, "|")
	combinedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:16]

	// Persiste fingerprint de forma assíncrona
	go s.storeFingerprint(ctx, projectID, ip, ua, fp, signals, combinedHash, botScore, isBot)

	// ── Decisão ───────────────────────────────────────────────────────
	result := &FingerprintResult{
		BotScore:     botScore,
		IsBot:        isBot,
		IsSuspicious: isSuspicious,
		Signals:      signals,
		AntiDevTools: cfg.AntiDevTools,
	}

	device := deviceFromUA(ua)

	if !isBot {
		result.Allowed = true
		result.Action = "money"
		// Rotatividade de link: destino sorteado do pool (primary + fallbacks).
		result.RedirectURL = pickRotationURL(cfg)

		// Registra visita humana em shield_visits
		go s.insertVisit(context.Background(), projectID, VisitRecord{
			IP:        ip,
			UserAgent: ua,
			Country:   country,
			Device:    device,
			IsBot:     false,
			BotScore:  botScore,
			Signals:   signals,
			Action:    "money",
		})
		// Log "permitido" (verde no dashboard).
		go s.insertLog(context.Background(), &models.ShieldLog{
			IP:        ip,
			UserAgent: ua,
			Country:   country,
			Device:    device,
			Reason:    "human",
			Action:    "allowed",
		}, projectID)
	} else {
		result.Allowed = false
		if cfg.RedirectURL != "" {
			result.Action = "redirected"
			result.RedirectURL = cfg.RedirectURL
		} else {
			result.Action = "blocked"
		}

		// Log no shield_logs + shield_visits e dispara webhooks
		log := &models.ShieldLog{
			IP:         ip,
			UserAgent:  ua,
			Device:     device,
			Reason:     "ml:" + strings.Join(signals, ","),
			Action:     result.Action,
			RedirectTo: result.RedirectURL,
		}
		go s.insertLog(context.Background(), log, projectID)
		go s.insertVisit(context.Background(), projectID, VisitRecord{
			IP:        ip,
			UserAgent: ua,
			Country:   country,
			Device:    device,
			IsBot:     true,
			BotScore:  botScore,
			Signals:   signals,
			Action:    result.Action,
			DestURL:   result.RedirectURL,
		})
		go s.FireWebhooks(context.Background(), projectID, "bot_detected", map[string]interface{}{
			"ip":        ip,
			"score":     botScore,
			"signals":   signals,
			"action":    result.Action,
			"user_agent": ua,
		})
	}

	return result, nil
}

// storeFingerprint persiste o fingerprint no banco.
func (s *Service) storeFingerprint(ctx context.Context, projectID uuid.UUID, ip, ua string,
	fp *FingerprintData, signals []string, combinedHash string, botScore float64, isBotResult bool) {

	webrtcIPs := fp.WebRTCIPs
	if webrtcIPs == nil {
		webrtcIPs = []string{}
	}
	if signals == nil {
		signals = []string{}
	}

	_, _ = s.db.Exec(ctx, `
		INSERT INTO shield_fingerprints (
			project_id, session_hash,
			canvas_hash, webgl_vendor, webgl_renderer, webgl_hash,
			audio_hash, screen_width, screen_height, color_depth, pixel_ratio,
			timezone, language, platform, cpu_cores, memory_gb, touch_points,
			webrtc_ips, fonts_hash, plugins, combined_hash,
			bot_score, signals, is_bot, ip, user_agent
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
		projectID, fp.SessionHash,
		fp.CanvasHash, fp.WebGLVendor, fp.WebGLRenderer, fp.WebGLHash,
		fp.AudioHash, fp.ScreenWidth, fp.ScreenHeight, fp.ColorDepth, fp.PixelRatio,
		fp.Timezone, fp.Language, fp.Platform, fp.CPUCores, fp.MemoryGB, fp.TouchPoints,
		webrtcIPs, fp.FontsHash, fp.Plugins, combinedHash,
		botScore, signals, isBotResult, ip, ua,
	)
}

// ── Helpers ───────────────────────────────────────────────────────────────

// isKnownDatacenterPrefix verifica prefixos IP de datacenters comuns.
func isKnownDatacenterPrefix(ip string) bool {
	prefixes := []string{
		"3.", "13.", "18.", "34.", "35.", "44.", "52.", "54.",   // AWS / GCP
		"20.", "40.", "51.", "104.", "168.62.",                  // Azure
		"185.220.", "45.33.", "172.104.", "45.79.", "66.228.",   // Linode / outros
		"167.88.", "195.175.", "77.247.",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(ip, p) {
			return true
		}
	}
	return false
}

func addUniq(slice *[]string, val string) {
	for _, v := range *slice {
		if v == val {
			return
		}
	}
	*slice = append(*slice, val)
}

func containsCI(slice []string, val string) bool {
	val = strings.ToUpper(val)
	for _, v := range slice {
		if strings.ToUpper(v) == val {
			return true
		}
	}
	return false
}
