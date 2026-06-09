package api

import (
	"context"
	"fmt"
	"os"

	"github.com/ecsistem/convtrack/internal/analytics"
	"github.com/ecsistem/convtrack/internal/api/handlers"
	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/attribution"
	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/ecsistem/convtrack/internal/cache"
	"github.com/ecsistem/convtrack/internal/conversion"
	"github.com/ecsistem/convtrack/internal/queue"
	"github.com/ecsistem/convtrack/internal/replay"
	"github.com/ecsistem/convtrack/internal/rules"
	"github.com/ecsistem/convtrack/internal/session"
	"github.com/ecsistem/convtrack/internal/storage"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func NewApp(db *pgxpool.Pool, rdb *cache.Cache, rawRedis *redis.Client) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:      "ConvTrack API v1",
		ErrorHandler: jsonErrorHandler,
		BodyLimit:    8 * 1024 * 1024,
	})

	app.Use(recover.New())
	app.Use(logger.New())

	frontendOrigin := os.Getenv("FRONTEND_ORIGIN")

	// ── Services ──────────────────────────────────────────────────────────────
	sessionSvc  := session.New(db)
	attrSvc     := attribution.New(sessionSvc)
	q           := queue.New(rawRedis)
	convSvc     := conversion.NewWithQueue(db, attrSvc, q)
	authSvc     := auth.New(db)
	analyticsSvc := analytics.New(db)

	// S3 for session replay (optional)
	var replayH *handlers.ReplayHandler
	s3Client, err := storage.NewS3Client(context.Background())
	if err != nil {
		fmt.Printf("warn: S3 not configured, replay disabled: %v\n", err)
	} else {
		replaySvc := replay.New(db, rawRedis, s3Client)
		replayH = handlers.NewReplay(replaySvc)
	}

	// ── Handlers ──────────────────────────────────────────────────────────────
	collectH      := handlers.NewCollect(sessionSvc, attrSvc)
	webhookH      := handlers.NewWebhook(convSvc, attrSvc)
	dashboardH    := handlers.NewDashboard(convSvc, sessionSvc)
	rulesSvc      := rules.New(db)
	rulesH        := handlers.NewRules(rulesSvc)
	analyticsH    := handlers.NewAnalytics(analyticsSvc)
	authH         := handlers.NewAuth(authSvc)
	projectsH     := handlers.NewProjects(authSvc)
	integrationsH := handlers.NewIntegrations(db)

	// ── Middleware factories ───────────────────────────────────────────────────
	apiKeyAuth    := middleware.APIKey(db, rdb)
	jwtAuth       := middleware.JWTAuth(authSvc, db)
	dashCORS      := middleware.DashboardCORS(frontendOrigin)
	domainCheckMw := middleware.DomainCheck(db)

	// ── Public auth endpoints ─────────────────────────────────────────────────
	authGroup := app.Group("/v1/auth", dashCORS)
	authGroup.Post("/register", authH.Register)
	authGroup.Post("/login",    authH.Login)
	authGroup.Post("/refresh",  authH.Refresh)
	authGroup.Post("/logout",   authH.Logout)
	authGroup.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// ── Collect endpoints (CORS aberto, API key + domain check) ──────────────
	collect := app.Group("/v1/collect", middleware.CollectCORS())
	collect.Post("/session",    apiKeyAuth, domainCheckMw, collectH.Session)
	collect.Post("/event",      apiKeyAuth, domainCheckMw, collectH.Event)
	collect.Post("/identify",   apiKeyAuth, domainCheckMw, collectH.Identify)
	collect.Post("/conversion", apiKeyAuth, domainCheckMw, rulesH.ClientConversion)
	collect.Post("/heartbeat",  apiKeyAuth, collectH.Heartbeat)
	collect.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// ── Trigger Rules (público — lido pelo tracker.js) ────────────────────────
	app.Get("/v1/rules", middleware.CollectCORS(), apiKeyAuth, rulesH.Public)
	app.Options("/v1/rules", middleware.CollectCORS(), func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// ── Test Events SSE ───────────────────────────────────────────────────────
	testEventsH := handlers.NewTestEvents(rawRedis)
	app.Get("/v1/test-events/stream", dashCORS, apiKeyAuth, testEventsH.Stream)

	// ── Session Replay ───────────────────────────────────────────────────────
	if replayH != nil {
		rep := app.Group("/v1/replay", middleware.CollectCORS())
		rep.Post("/events", apiKeyAuth, replayH.Collect)
		rep.Post("/flush",  apiKeyAuth, replayH.Flush)
		rep.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })
	}

	// ── Webhook endpoints ─────────────────────────────────────────────────────
	app.Post("/v1/webhooks/:projectKey/:platform", webhookH.Handle)

	// ── /v1/me — JWT-protected, account-scoped ────────────────────────────────
	me := app.Group("/v1/me", dashCORS, jwtAuth)
	me.Get("/",                    authH.Me)
	me.Get("/projects",            projectsH.List)
	me.Post("/projects",           projectsH.Create)
	me.Get("/projects/:id",        projectsH.Get)
	me.Put("/projects/:id",        projectsH.Update)
	me.Delete("/projects/:id",     projectsH.Delete)
	me.Post("/projects/:id/rotate-key",         projectsH.RotateKey)
	me.Patch("/projects/:id/clone-protection",  projectsH.SetCloneProtection)
	me.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// ── /v1/dashboard — JWT auth + project context ────────────────────────────
	dash := app.Group("/v1/dashboard", dashCORS, jwtAuth, middleware.RequireProject)
	dash.Get("/overview",             dashboardH.Overview)
	dash.Get("/analytics",            analyticsH.Get)
	dash.Get("/conversions",          dashboardH.Conversions)
	dash.Get("/sessions",             dashboardH.Sessions)
	dash.Get("/settings",             analyticsH.GetSettings)
	dash.Put("/settings",             analyticsH.PutSettings)
	dash.Get("/ad-costs",             analyticsH.ListAdCosts)
	dash.Post("/ad-costs",            analyticsH.AddAdCost)
	dash.Delete("/ad-costs/:id",      analyticsH.DeleteAdCost)
	dash.Get("/rules",                rulesH.List)
	dash.Post("/rules",               rulesH.Create)
	dash.Put("/rules/:id",            rulesH.Update)
	dash.Patch("/rules/:id/toggle",   rulesH.Toggle)
	dash.Delete("/rules/:id",         rulesH.Delete)
	dash.Get("/integrations",         integrationsH.List)
	dash.Put("/integrations/:platform",      integrationsH.Upsert)
	dash.Delete("/integrations/:platform",   integrationsH.Delete)
	dash.Post("/integrations/:platform/test", integrationsH.Test)
	if replayH != nil {
		dash.Get("/replay/:sessionId", replayH.GetURL)
	}
	dash.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// ── Health & tracker ──────────────────────────────────────────────────────
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})
	app.Static("/tracker.js", "./public/tracker.js")

	return app
}

func jsonErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	return c.Status(code).JSON(fiber.Map{"error": err.Error()})
}
