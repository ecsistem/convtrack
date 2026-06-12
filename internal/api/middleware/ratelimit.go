package middleware

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

// ClientIP extrai o IP real do cliente respeitando proxies reversos
// (Cloudflare, nginx). Essencial para que filtros por faixa de IP avaliem o
// visitante real e não o IP do proxy.
func ClientIP(c *fiber.Ctx) string {
	if v := c.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := c.Get("X-Real-IP"); v != "" {
		return v
	}
	if xff := c.Get("X-Forwarded-For"); xff != "" {
		// primeiro IP da lista = cliente original
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return c.IP()
}

// clientIP é o alias interno usado pelo rate limiter.
func clientIP(c *fiber.Ctx) string { return ClientIP(c) }

// rateLimited monta um limiter por IP com mensagem 429 padronizada.
func rateLimited(max int, window time.Duration) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: window,
		KeyGenerator: func(c *fiber.Ctx) string {
			return clientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "muitas requisições — tente novamente em instantes",
			})
		},
	})
}

// AuthRateLimit: rotas sensíveis de autenticação (login, registro, reset).
// 10 tentativas por minuto por IP.
func AuthRateLimit() fiber.Handler {
	return rateLimited(10, time.Minute)
}

// ForgotPasswordRateLimit: bem restrito para evitar spam de emails.
// 3 pedidos por 10 minutos por IP.
func ForgotPasswordRateLimit() fiber.Handler {
	return rateLimited(3, 10*time.Minute)
}

// CollectRateLimit: endpoints de coleta (alto volume legítimo).
// 600 req/min por IP (~10 req/s).
func CollectRateLimit() fiber.Handler {
	return rateLimited(600, time.Minute)
}

// ReplayRateLimit: upload de replay (lotes frequentes mas controlados).
// 300 req/min por IP.
func ReplayRateLimit() fiber.Handler {
	return rateLimited(300, time.Minute)
}

// WebhookRateLimit: webhooks de plataformas de venda.
// 120 req/min por IP.
func WebhookRateLimit() fiber.Handler {
	return rateLimited(120, time.Minute)
}

// CloakRateLimit: rotas públicas do cloaker (/:slug, /r/...). Um humano real
// nunca abre o link dezenas de vezes por minuto — pega scrapers/bot farms.
// 60 req/min por IP.
func CloakRateLimit() fiber.Handler {
	return rateLimited(60, time.Minute)
}
