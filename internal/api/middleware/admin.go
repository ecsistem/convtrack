package middleware

import (
	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/gofiber/fiber/v2"
)

// AdminOrManager allows access to admin panel routes for both admins and managers.
// Must be used AFTER JWTAuth. Sets "caller_is_admin" and "caller_is_manager" locals.
func AdminOrManager(authSvc *auth.Service) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenStr := ""
		if h := c.Get("Authorization"); len(h) > 7 {
			tokenStr = h[7:]
		}
		if tokenStr == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "missing token"})
		}
		claims, err := authSvc.ValidateAccessToken(tokenStr)
		if err != nil || (!claims.IsAdmin && !claims.IsManager) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "acesso negado"})
		}
		c.Locals("caller_is_admin", claims.IsAdmin)
		c.Locals("caller_is_manager", claims.IsManager)
		return c.Next()
	}
}
