package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

// CollectCORS allows any origin for the tracker endpoints
func CollectCORS() fiber.Handler {
	return cors.New(cors.Config{
		AllowOrigins:     "*",
		AllowMethods:     "GET,POST,OPTIONS",
		AllowHeaders:     "Content-Type, X-API-Key",
		AllowCredentials: false,
		MaxAge:           86400,
	})
}

// DashboardCORS restricts to the frontend origin
func DashboardCORS(frontendOrigin string) fiber.Handler {
	if frontendOrigin == "" {
		frontendOrigin = "http://localhost:3000"
	}
	return cors.New(cors.Config{
		AllowOrigins:     frontendOrigin,
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowHeaders:     "Content-Type, Authorization, X-Project-Id",
		ExposeHeaders:    "Content-Length",
		AllowCredentials: true,
		MaxAge:           86400,
	})
}
