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
	"github.com/ecsistem/convtrack/internal/shield"
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

	// ── Services ──────────────────────────────────────────────────────────────
	shieldSvc := shield.New(db, rawRedis)

	// ── Handlers ──────────────────────────────────────────────────────────────
	collectH      := handlers.NewCollect(sessionSvc, attrSvc)
	webhookH      := handlers.NewWebhook(convSvc, attrSvc)
	dashboardH    := handlers.NewDashboard(convSvc, sessionSvc)
	rulesSvc      := rules.New(db)
	rulesH        := handlers.NewRules(rulesSvc, convSvc)
	analyticsH    := handlers.NewAnalytics(analyticsSvc, sessionSvc, convSvc, db)
	authH         := handlers.NewAuth(authSvc)
	projectsH     := handlers.NewProjects(authSvc)
	integrationsH := handlers.NewIntegrations(db)
	shieldH       := handlers.NewShield(shieldSvc, rawRedis)

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
		collectCORS := middleware.CollectCORS()

		// Collect (API key, CORS aberto — tracker.js)
		app.Post("/v1/replay/events", collectCORS, apiKeyAuth, replayH.Collect)
		app.Post("/v1/replay/flush",  collectCORS, apiKeyAuth, replayH.Flush)
		app.Options("/v1/replay/events", collectCORS, func(c *fiber.Ctx) error { return c.SendStatus(204) })
		app.Options("/v1/replay/flush",  collectCORS, func(c *fiber.Ctx) error { return c.SendStatus(204) })

		// GET presigned URL — JWT auth, dashboard
		app.Get("/v1/replay/:sessionId",     dashCORS, jwtAuth, middleware.RequireProject, replayH.GetURL)
		app.Options("/v1/replay/:sessionId", dashCORS, func(c *fiber.Ctx) error { return c.SendStatus(204) })
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
	dash.Get("/campaigns",            analyticsH.GetCampaigns)
	dash.Post("/sync-costs",          analyticsH.SyncAdCosts)
	dash.Get("/online",               analyticsH.GetOnline)
	dash.Get("/leads",                analyticsH.GetLeads)
	dash.Post("/test-conversion",     analyticsH.TestConversion)
	dash.Get("/events",               analyticsH.GetEvents)
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
	dash.Get("/clone-violations",     analyticsH.GetCloneViolations)
	dash.Get("/integrations",         integrationsH.List)
	dash.Put("/integrations/:platform",      integrationsH.Upsert)
	dash.Delete("/integrations/:platform",   integrationsH.Delete)
	dash.Post("/integrations/:platform/test", integrationsH.Test)
	if replayH != nil {
		dash.Get("/replay/:sessionId", replayH.GetURL)
	}
	dash.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// APIBase para injeção de script no proxy reverso
	shieldSvc.APIBase = os.Getenv("API_BASE_URL")

	// ── Shield (público — tracker.js + fingerprint + smart redirect) ──────────
	shieldCollect := app.Group("/v1/shield", middleware.CollectCORS())
	shieldCollect.Post("/check",       apiKeyAuth, shieldH.Check)
	shieldCollect.Post("/fingerprint", apiKeyAuth, shieldH.Fingerprint)
	shieldCollect.Options("/*", func(c *fiber.Ctx) error { return c.SendStatus(204) })

	// Script de fingerprinting (sem auth)
	app.Get("/shield-fp.js", shieldH.ServeFPScript)

	// Smart redirect avançado (fingerprinting + A/B, server-side)
	app.Get("/r/:projectKey", apiKeyAuth, shieldH.SmartRedirectAdvanced)

	// ── Shield dashboard (JWT) ─────────────────────────────────────────────────
	dash.Get("/shield/config",             shieldH.GetConfig)
	dash.Put("/shield/config",             shieldH.PutConfig)
	dash.Get("/shield/logs",               shieldH.ListLogs)
	dash.Delete("/shield/logs",            shieldH.ClearLogs)
	dash.Get("/shield/stats",              shieldH.Stats)
	dash.Get("/shield/logs/stream",        shieldH.StreamLogs)
	// Campanhas
	dash.Get("/shield/campaigns",          shieldH.ListCampaigns)
	dash.Post("/shield/campaigns",         shieldH.CreateCampaign)
	dash.Put("/shield/campaigns/:id",      shieldH.UpdateCampaign)
	dash.Delete("/shield/campaigns/:id",   shieldH.DeleteCampaign)
	// Domínios
	dash.Get("/shield/domains",            shieldH.ListDomains)
	dash.Post("/shield/domains",           shieldH.CreateDomain)
	dash.Delete("/shield/domains/:id",     shieldH.DeleteDomain)
	// Webhooks
	dash.Get("/shield/webhooks",           shieldH.ListWebhooks)
	dash.Post("/shield/webhooks",          shieldH.CreateWebhook)
	dash.Delete("/shield/webhooks/:id",    shieldH.DeleteWebhook)
	// Visitas com fingerprint
	dash.Get("/shield/visits",             shieldH.ListVisits)

	// ── Proxy reverso por domínio (catch-all — deve ficar APÓS todas as rotas) ─
	// Habilitado apenas se API_BASE_URL estiver configurado
	if shieldSvc.APIBase != "" {
		app.Use(shieldSvc.DomainProxyMiddleware)
	}

	// ── Health & tracker ──────────────────────────────────────────────────────
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})
	app.Static("/tracker.js",    "./public/tracker.js")
	app.Static("/rrweb.min.js",  "./public/rrweb.min.js")
	app.Static("/test.html",     "./public/test.html")
	app.Static("/shield-fp.js",  "./public/shield-fp.js")

	return app
}

func jsonErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	return c.Status(code).JSON(fiber.Map{"error": err.Error()})
}
