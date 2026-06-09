package middleware

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DomainCheck verifica o domínio da request ANTES de processar o handler.
//
// Comportamento:
//   - Se clone_protection = true e o domínio não bate → retorna JSON com
//     {"clone_detected": true, "redirect_url": "..."} e NÃO processa a request.
//     O tracker.js lê isso e redireciona o visitante para o site original.
//   - Se clone_protection = false e o domínio não bate → apenas loga a violação
//     de forma assíncrona e continua processando normalmente.
func DomainCheck(db *pgxpool.Pool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		project := GetProject(c)
		if project == nil || project.Domain == "" {
			return c.Next()
		}

		// Extrai domínio do Origin ou Referer
		requestDomain := extractDomain(c.Get("Origin"))
		if requestDomain == "" {
			requestDomain = extractDomain(c.Get("Referer"))
		}
		if requestDomain == "" {
			return c.Next() // request sem origem (ex: curl, Postman) — ignora
		}

		if !domainsMatch(project.Domain, requestDomain) {
			go logViolation(db, project.ID.String(), project.Domain, requestDomain, c.IP(), c.Get("User-Agent"))

			if project.CloneProtection {
				redirectURL := buildRedirectURL(project.Domain, c.Get("Referer"), c.Get("Origin"))
				return c.JSON(fiber.Map{
					"clone_detected": true,
					"redirect_url":   redirectURL,
				})
			}
		}

		return c.Next()
	}
}

// domainsMatch verifica se o domínio da request é o mesmo ou um subdomínio do projeto.
//
// Exemplos:
//
//	project.Domain = "exemplo.com.br"
//	requestDomain  = "www.exemplo.com.br"   → true
//	requestDomain  = "checkout.exemplo.com.br" → true
//	requestDomain  = "outro.com"            → false
func domainsMatch(projectDomain, requestDomain string) bool {
	// Normaliza (remove www, lowercase, trailing dot)
	pd := normalizeDomain(projectDomain)
	rd := normalizeDomain(requestDomain)

	if pd == rd {
		return true
	}
	// Aceita subdomínios: rd termina com ".pd"
	return strings.HasSuffix(rd, "."+pd)
}

func normalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "www.")
	d = strings.TrimSuffix(d, ".")
	return d
}

func extractDomain(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	// Origin já pode vir como "https://exemplo.com" — parseia normalmente
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// buildRedirectURL constrói a URL de redirecionamento para o domínio original do projeto.
// Preserva o path e a query string do Referer para que UTMs e parâmetros não se percam.
//
// Exemplos:
//
//	projectDomain = "minhapagina.com.br"
//	referer       = "https://clone-malicioso.com/oferta?utm_source=fb&fbclid=abc"
//	→ "https://minhapagina.com.br/oferta?utm_source=fb&fbclid=abc"
func buildRedirectURL(projectDomain, referer, origin string) string {
	// Garante scheme no domínio do projeto
	if !strings.Contains(projectDomain, "://") {
		projectDomain = "https://" + projectDomain
	}
	base, err := url.Parse(projectDomain)
	if err != nil {
		return projectDomain
	}

	// Tenta extrair path + query do Referer
	src := referer
	if src == "" {
		src = origin
	}
	if src == "" {
		return base.String()
	}

	if !strings.Contains(src, "://") {
		src = "https://" + src
	}
	srcURL, err := url.Parse(src)
	if err != nil {
		return base.String()
	}

	base.Path     = srcURL.Path
	base.RawQuery = srcURL.RawQuery
	base.Fragment = srcURL.Fragment
	return base.String()
}

func logViolation(db *pgxpool.Pool, projectID, projectDomain, requestDomain, ip, ua string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := db.Exec(ctx, `
		INSERT INTO domain_violations
			(project_id, project_domain, request_domain, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5)`,
		projectID, projectDomain, requestDomain, ip, ua,
	)
	if err != nil {
		fmt.Printf("domain_check: log violation: %v\n", err)
	}
}
