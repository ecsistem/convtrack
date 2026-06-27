package shield

import "strings"

// botPatterns — substrings de UA de bots/crawlers conhecidos
var botPatterns = []string{
	// Search engines
	"googlebot", "google-read-aloud", "google-inspectiontool",
	"bingbot", "msnbot", "baiduspider", "yandexbot", "duckduckbot",
	"slurp", "sogou", "exabot", "facebot", "ia_archiver",
	"applebot", "dotbot", "semrushbot", "ahrefsbot", "mj12bot",
	"petalbot", "bytespider", "gptbot", "chatgpt-user",
	// Social / link preview
	"facebookexternalhit", "facebookcatalog", "facebot",
	"twitterbot", "linkedinbot", "slackbot", "discordbot",
	"telegrambot", "whatsapp", "pinterest", "vkshare",
	"skypeuripreview", "snapchat",
	// Generic
	"bot", "crawler", "spider", "scraper", "fetcher", "archiver",
	"wget", "curl", "python-requests", "python-urllib", "python-httpx",
	"go-http-client", "java/", "okhttp", "axios/", "node-fetch",
	"node.js", "libwww", "lwp", "httpclient", "apache-httpclient",
	"mechanize", "scrapy", "phantomjs", "htmlunit",
	// SEO / Analytics tools
	"semrush", "ahrefs", "majestic", "rogerbot", "spyfu",
	"seokicks", "sistrix", "opensiteexplorer",
	// Security scanners
	"nikto", "nmap", "masscan", "zgrab", "nuclei",
	"sqlmap", "nessus", "openvas", "burpsuite", "w3af",
	"wpscan", "dirbuster", "gobuster", "feroxbuster", "burp",
	"owasp", "acunetix", "qualys", "skipfish",
	// Crawlers de serviço do Google (revisores / verificadores)
	"mediapartners-google", "adsbot-google", "googlebot-image",
	"googlebot-news", "googlebot-video", "apis-google",
	"google-site-verification", "googleproducer",
	"chrome-lighthouse", "lighthouse", "pagespeed",
	// SEO / análise adicionais
	"blexbot", "linkdexbot", "screaming frog", "dataforseobot", "ccbot",
	// Preview de links / social adicionais
	"embedly", "quora link preview", "redditbot",
	// Bibliotecas HTTP / clientes de API
	"aiohttp", "httpx", "undici", "postmanruntime", "insomnia",
	"httpie", "guzzlehttp", "superagent", "got (",
	"ruby", "php/", "perl",
	// Monitoramento / performance
	"cloudflare-alwaysonline", "uptimerobot", "pingdom", "statuscake",
	"gtmetrix", "webpagetest", "ptst", "ylt",
	// NOTA: "tiktok" e "bytedance" NÃO entram aqui de propósito — o
	// navegador in-app do TikTok contém esses tokens no UA e são tráfego
	// pago real. Apenas "bytespider" (crawler) é bloqueado.
}

// spyToolPatterns — ferramentas de espionagem de criativos/anúncios
var spyToolPatterns = []string{
	"adspy", "bigspy", "pipiads", "minea", "poweradspy",
	"adplexity", "advault", "socialadscout", "adbeat",
	"whatrunswhere", "moat", "spyfu", "pathmatics", "adforum",
	"adstransparency", "fbleadgen", "admobispy", "adsviser",
	"dropispy", "ecomhunt", "ppspy", "adsreveal", "anstrex",
	"advertiserscope", "adsspy", "adsmirror", "sellertools",
	"intelligynce", "adsvault", "nativeadsbuzz", "spy.pet",
}

// headlessPatterns — indicadores de browser headless/automação
var headlessPatterns = []string{
	"headlesschrome", "headless", "phantomjs", "nightmare",
	"puppeteer", "playwright", "selenium", "webdriver",
	"htmlunit", "triton", "slimerjs", "casperjs",
	"splash", "cypress", "testcafe",
}

// isBot verifica se o UA contém padrão de bot
func isBot(ua string) bool {
	lower := strings.ToLower(ua)
	for _, p := range botPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// isSpyTool verifica se o UA pertence a ferramenta de espionagem
func isSpyTool(ua string) bool {
	lower := strings.ToLower(ua)
	for _, p := range spyToolPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// softwareRenderers — nomes de GPU de software (rasterizadores), indicativos
// de browser headless/automação (sem GPU real).
var softwareRenderers = []string{
	"swiftshader", "llvmpipe", "mesa offscreen", "software rasterizer",
	"google swiftshader", "microsoft basic render", "virtualbox", "vmware",
}

// isSoftwareRenderer verifica se o renderer WebGL é um rasterizador de software.
func isSoftwareRenderer(renderer string) bool {
	lower := strings.ToLower(renderer)
	if lower == "" {
		return false
	}
	for _, p := range softwareRenderers {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// isHeadless verifica se o UA indica browser headless
func isHeadless(ua string) bool {
	lower := strings.ToLower(ua)
	for _, p := range headlessPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// deviceFromUA retorna "mobile", "tablet" ou "desktop"
func deviceFromUA(ua string) string {
	lower := strings.ToLower(ua)
	if strings.Contains(lower, "mobi") || strings.Contains(lower, "android") ||
		strings.Contains(lower, "iphone") || strings.Contains(lower, "ipod") ||
		strings.Contains(lower, "blackberry") || strings.Contains(lower, "windows phone") {
		return "mobile"
	}
	if strings.Contains(lower, "ipad") || strings.Contains(lower, "tablet") {
		return "tablet"
	}
	return "desktop"
}

// osFromUA identifica o sistema operacional a partir do User-Agent.
// Retorna "ios", "android", "windows", "macos", "linux" ou "other".
// Checagem de "windows phone" vem antes de "windows" pois o token "windows"
// aparece em ambos.
func osFromUA(ua string) string {
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") ||
		strings.Contains(lower, "ipod") || strings.Contains(lower, "cpu os") || strings.Contains(lower, "cpu iphone os"):
		return "ios"
	case strings.Contains(lower, "android"):
		return "android"
	case strings.Contains(lower, "windows"):
		return "windows"
	case strings.Contains(lower, "mac os x") || strings.Contains(lower, "macintosh"):
		return "macos"
	case strings.Contains(lower, "linux"):
		return "linux"
	default:
		return "other"
	}
}
