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

	ip := c.IP()
	ua := c.Get("User-Agent")

	// ── Verificação rápida server-side ────────────────────────────────
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

	// Repassa headers do cliente (exceto Host e Connection)
	c.Request().Header.VisitAll(func(k, v []byte) {
		key := string(k)
		if key == "Host" || key == "Connection" || key == "Transfer-Encoding" {
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
