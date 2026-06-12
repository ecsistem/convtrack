package middleware

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

// clientIP extrai o IP real do cliente respeitando proxies reversos.
func clientIP(c *fiber.Ctx) string {
	if xff := c.Get("CF-Connecting-IP"); xff != "" {
		return xff
	}
	if xff := c.Get("X-Real-IP"); xff != "" {
		return xff
	}
	return c.IP()
}

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
