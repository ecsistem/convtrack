package shield

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// realClientIP extrai o IP real do visitante respeitando proxies reversos
// (Cloudflare, nginx) — essencial quando o site está atrás de um proxy.
func realClientIP(c *fiber.Ctx) string {
	if v := c.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := c.Get("X-Real-IP"); v != "" {
		return v
	}
	if xff := c.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return c.IP()
}

// staticAssetExt — extensões de sub-recurso de página (CSS, JS, imagens,
// fontes, mídia). Requisições para esses arquivos não são "visitas" — são
// disparadas automaticamente pelo navegador ao renderizar a página que já
// foi liberada na requisição de navegação. Não devem gerar log nem rodar
// Check() de novo (evita logs duplicados/poluídos e decisões inconsistentes
// de money/safe entre o HTML e seus próprios assets).
var staticAssetExt = []string{
	".css", ".js", ".mjs", ".map",
	".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico", ".avif",
	".woff", ".woff2", ".ttf", ".eot", ".otf",
	".mp4", ".webm", ".mp3", ".wav",
	".json", ".xml", ".txt", ".pdf",
}

// isStaticAsset verifica se o path da requisição é um sub-recurso estático
// (pela extensão), não a navegação principal da página.
func isStaticAsset(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range staticAssetExt {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// domainProxyDecisionCookie nomeia o cookie que guarda a decisão (money/safe)
// tomada na requisição de navegação, para os assets da mesma página reusarem
// sem rodar Check()/log de novo.
func domainProxyDecisionCookie(campaignID string) string {
	id := campaignID
	if len(id) > 8 {
		id = id[:8]
	}
	return "ct_dpx_" + id
}

// DomainProxyMiddleware é um middleware Fiber que intercepta requisições para domínios
// registrados em shield_domains e as roteia como proxy reverso.
// Para domínios não registrados, chama c.Next() sem interferir.
func (s *Service) DomainProxyMiddleware(c *fiber.Ctx) error {
	host := string(c.Request().Header.Peek("Host"))
	// Remove porta, se houver
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	campaign, err := s.ResolveCampaignByDomain(c.Context(), host)
	if err != nil || campaign == nil {
		return c.Next() // não é um domínio do Shield — deixa passar
	}

	path := string(c.Request().URI().Path())
	cookieName := domainProxyDecisionCookie(campaign.ID.String())

	// ── Sub-recurso (CSS/JS/imagem/fonte) ───────────────────────────────
	// Reusa a decisão da navegação principal — sem Check(), sem log.
	if isStaticAsset(path) {
		if cached := c.Cookies(cookieName); cached != "" {
			return s.proxyRequest(c, cached, campaign)
		}
		// Sem cookie (hotlink direto ao asset) — resolve sem registrar log.
		targetURL, _ := ChooseURL(campaign, realClientIP(c))
		if targetURL == "" {
			targetURL = campaign.SafeURL
		}
		if targetURL == "" {
			return c.Status(fiber.StatusNotFound).SendString("campaign URL not configured")
		}
		return s.proxyRequest(c, targetURL, campaign)
	}

	ip := realClientIP(c)
	ua := c.Get("User-Agent")

	// ── Navegação principal — Verificação server-side + log ────────────
	result, _ := s.Check(c.Context(), campaign.ProjectID, CheckRequest{
		IP:        ip,
		UserAgent: ua,
	})

	var targetURL string
	if result == nil || result.Allowed {
		targetURL, _ = ChooseURL(campaign, ip)
	} else {
		if result.RedirectURL != "" {
			targetURL = result.RedirectURL
		} else if campaign.SafeURL != "" {
			targetURL = campaign.SafeURL
		}
	}

	if targetURL == "" {
		return c.Status(fiber.StatusNotFound).SendString("campaign URL not configured")
	}

	// Guarda a decisão para os assets desta mesma página reusarem.
	c.Cookie(&fiber.Cookie{
		Name:     cookieName,
		Value:    targetURL,
		MaxAge:   600, // 10min — cobre o tempo de carregar a página
		HTTPOnly: true,
		SameSite: "Lax",
	})

	return s.proxyRequest(c, targetURL, campaign)
}

// ProxyTo é a versão pública de proxyRequest para uso nos handlers de slug.
func (s *Service) ProxyTo(c *fiber.Ctx, targetURL string, campaign *Campaign) error {
	return s.proxyRequest(c, targetURL, campaign)
}

// proxyRequest encaminha a requisição ao targetURL e retorna a resposta,
// injetando o script de fingerprinting em páginas HTML.
func (s *Service) proxyRequest(c *fiber.Ctx, targetURL string, campaign *Campaign) error {
	path := string(c.Request().URI().Path())
	qs := string(c.Request().URI().QueryString())

	// Monta URL completa
	base := strings.TrimRight(targetURL, "/")
	fullURL := base
	if path != "" && path != "/" {
		fullURL = base + path
	}
	if qs != "" {
		fullURL += "?" + qs
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	method := string(c.Request().Header.Method())
	var bodyReader io.Reader
	if len(c.Body()) > 0 {
		bodyReader = bytes.NewReader(c.Body())
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).SendString("proxy: request build error")
	}

	// Repassa headers do cliente (exceto Host, Connection e Accept-Encoding).
	// Accept-Encoding é removido para que o upstream responda sem compressão —
	// o proxy descomprime apenas HTML (readBody); respostas binárias/CSS/JS
	// viriam crus se viessem gzip sem serem descomprimidas antes de reenviar.
	c.Request().Header.VisitAll(func(k, v []byte) {
		key := string(k)
		if key == "Host" || key == "Connection" || key == "Transfer-Encoding" || key == "Accept-Encoding" {
			return
		}
		req.Header.Set(key, string(v))
	})
	req.Header.Set("X-Forwarded-For", c.IP())
	req.Header.Set("X-Real-IP", c.IP())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).SendString("upstream error")
	}
	defer resp.Body.Close()

	// Status e headers da resposta upstream
	c.Status(resp.StatusCode)
	for key, vals := range resp.Header {
		lk := strings.ToLower(key)
		if lk == "content-encoding" || lk == "transfer-encoding" || lk == "content-length" {
			continue // gestão interna
		}
		for _, v := range vals {
			c.Set(key, v)
		}
	}

	// ── Injeção de fingerprint em páginas HTML ─────────────────────────
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		body, err := readBody(resp)
		if err != nil {
			return c.Status(fiber.StatusBadGateway).SendString("body read error")
		}
		injected := s.injectScript(string(body), campaign)
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(injected)
	}

	// Stream de respostas não-HTML
	body, _ := io.ReadAll(resp.Body)
	return c.Send(body)
}

// injectScript injeta o script de fingerprinting antes de </head> ou </body>.
func (s *Service) injectScript(html string, campaign *Campaign) string {
	if s.APIBase == "" {
		return html
	}
	apiKey := campaign.ProjectID.String() // usa project_id como api_key para o fp script
	scriptTag := fmt.Sprintf(
		`<script src="%s/shield-fp.js?k=%s&c=%s" defer></script>`,
		s.APIBase, apiKey, campaign.ID.String(),
	)

	lower := strings.ToLower(html)
	if idx := strings.Index(lower, "</head>"); idx != -1 {
		return html[:idx] + scriptTag + html[idx:]
	}
	if idx := strings.Index(lower, "</body>"); idx != -1 {
		return html[:idx] + scriptTag + html[idx:]
	}
	return html + scriptTag
}

// readBody descomprime gzip se necessário.
func readBody(resp *http.Response) ([]byte, error) {
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		return io.ReadAll(gr)
	}
	return io.ReadAll(resp.Body)
}
