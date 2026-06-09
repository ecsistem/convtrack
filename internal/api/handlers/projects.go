package handlers

import (
	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ProjectsHandler struct {
	svc *auth.Service
}

func NewProjects(svc *auth.Service) *ProjectsHandler {
	return &ProjectsHandler{svc: svc}
}

// GET /v1/me/projects
func (h *ProjectsHandler) List(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projects, err := h.svc.ListProjects(c.Context(), accountID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if projects == nil {
		projects = []models.Project{}
	}
	return c.JSON(fiber.Map{"data": projects})
}

// POST /v1/me/projects
func (h *ProjectsHandler) Create(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	var body struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	if err := c.BodyParser(&body); err != nil || body.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name obrigatório"})
	}

	project, err := h.svc.CreateProject(c.Context(), accountID, body.Name, body.Domain)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(project)
}

// GET /v1/me/projects/:id
func (h *ProjectsHandler) Get(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projectID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	project, err := h.svc.GetProject(c.Context(), projectID, accountID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(project)
}

// PUT /v1/me/projects/:id
func (h *ProjectsHandler) Update(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projectID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	var body struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	project, err := h.svc.UpdateProject(c.Context(), projectID, accountID, body.Name, body.Domain)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(project)
}

// DELETE /v1/me/projects/:id
func (h *ProjectsHandler) Delete(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projectID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	if err := h.svc.DeleteProject(c.Context(), projectID, accountID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// PATCH /v1/me/projects/:id/clone-protection
func (h *ProjectsHandler) SetCloneProtection(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projectID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	project, err := h.svc.SetCloneProtection(c.Context(), projectID, accountID, body.Enabled)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(project)
}

// POST /v1/me/projects/:id/rotate-key
func (h *ProjectsHandler) RotateKey(c *fiber.Ctx) error {
	accountID, ok := middleware.GetAccountID(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	projectID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	project, err := h.svc.RotateAPIKey(c.Context(), projectID, accountID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(project)
}
