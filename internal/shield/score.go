package shield

// botSignalWeights define o peso de cada sinal na pontuação de bot (0.0–1.0).
// Baseado em análise empírica de padrões de tráfego inválido.
var botSignalWeights = map[string]float64{
	// Automação confirmada
	"webdriver":          0.95, // navigator.webdriver = true
	"headless":           0.90, // UA HeadlessChrome/Puppeteer/Playwright/Selenium
	"bot":                0.85, // UA de crawler/bot conhecido
	"spy_tool":           0.80, // UA de AdSpy, BigSpy, Minea, etc.
	"ip_blocked":         0.99, // IP na lista de bloqueio manual

	// Rede
	"vpn":                0.60, // ip-api.com: proxy = true
	"datacenter":         0.55, // ip-api.com: hosting = true
	"webrtc_vpn":         0.45, // WebRTC leak revelou IP de datacenter

	// Fingerprint ausente (indicativo de headless/automação)
	"no_canvas":          0.40, // Canvas API não disponível/bloqueada
	"no_webgl":           0.35, // WebGL não disponível
	"no_audio":           0.30, // AudioContext não disponível
	"no_plugins":         0.20, // navigator.plugins.length = 0

	// Anomalias de dispositivo
	"suspicious_screen":  0.30, // resolução padrão de headless (800×600, 1280×720 sem DPR)
	"timezone_mismatch":  0.30, // fuso horário ≠ país do IP
	"devtools":           0.40, // DevTools detectado aberto

	// Filtros de política
	"geo":                0.30, // país fora da allowlist ou na blocklist
	"device":             0.20, // tipo de dispositivo não permitido
}

// DefaultThreshold é o score mínimo para classificar como bot.
const DefaultThreshold = 0.70

// ComputeBotScore calcula um score de 0.0 a 1.0 a partir de sinais detectados.
//
// Fórmula: 60% do sinal mais alto + 40% da média dos demais.
// Isso garante que um sinal muito forte (webdriver = 0.95) já seja suficiente,
// mas múltiplos sinais fracos também elevam o score.
func ComputeBotScore(signals []string) float64 {
	if len(signals) == 0 {
		return 0.0
	}

	maxW := 0.0
	sumW := 0.0
	count := 0

	seen := make(map[string]bool)
	for _, sig := range signals {
		if seen[sig] {
			continue // evita contar o mesmo sinal duas vezes
		}
		seen[sig] = true

		w, ok := botSignalWeights[sig]
		if !ok {
			continue
		}
		if w > maxW {
			maxW = w
		}
		sumW += w
		count++
	}

	if count == 0 {
		return 0.0
	}

	avg := sumW / float64(count)
	score := 0.6*maxW + 0.4*avg
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// IsBot retorna true se o score superar o threshold configurado.
func IsBot(score, threshold float64) bool {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	return score >= threshold
}

// IsSuspicious retorna true se o score indicar suspeita (mas abaixo do threshold de bot).
func IsSuspicious(score float64) bool {
	return score >= 0.40 && score < DefaultThreshold
}
