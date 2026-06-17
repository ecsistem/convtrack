package handlers

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ecsistem/convtrack/internal/clone"
	"github.com/gofiber/fiber/v2"
)

// CloneHandler expõe o módulo de clonagem de ofertas.
type CloneHandler struct{}

// NewClone cria o handler de clonagem.
func NewClone() *CloneHandler { return &CloneHandler{} }

type cloneRequest struct {
	URL    string `json:"url"`
	Render bool   `json:"render"` // renderiza JS (Chromium) para clonar SPAs
}

var slugSanitize = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// CloneOffer godoc
//
//	POST /v1/dashboard/clone
//
// Recebe { "url": "https://oferta.com" }, clona a página (HTML + assets),
// reescreve as referências para caminhos locais e devolve um arquivo .zip.
func (h *CloneHandler) CloneOffer(c *fiber.Ctx) error {
	var req cloneRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "corpo inválido: " + err.Error(),
		})
	}
	if strings.TrimSpace(req.URL) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "campo 'url' obrigatório",
		})
	}

	res, err := clone.Clone(c.Context(), clone.Options{URL: req.URL, Render: req.Render})
	if err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error": "falha ao clonar a oferta: " + err.Error(),
		})
	}

	filename := zipName(res.Title, res.BaseURL)

	c.Set("Content-Type", "application/zip")
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Set("X-Clone-Title", sanitizeHeader(res.Title))
	c.Set("X-Clone-BaseURL", res.BaseURL)
	c.Set("X-Clone-Pages", fmt.Sprintf("%d", res.PageCount))
	c.Set("X-Clone-Assets", fmt.Sprintf("%d", res.AssetCount))
	c.Set("X-Clone-Bytes", fmt.Sprintf("%d", res.TotalBytes))
	c.Set("Access-Control-Expose-Headers",
		"X-Clone-Title,X-Clone-BaseURL,X-Clone-Pages,X-Clone-Assets,X-Clone-Bytes,Content-Disposition")

	return c.Send(res.Zip)
}

func zipName(title, baseURL string) string {
	base := title
	if strings.TrimSpace(base) == "" {
		base = baseURL
	}
	base = slugSanitize.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-._")
	if len(base) > 50 {
		base = base[:50]
	}
	if base == "" {
		base = "oferta"
	}
	return base + ".zip"
}

// sanitizeHeader remove caracteres inválidos para um valor de header HTTP.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}
