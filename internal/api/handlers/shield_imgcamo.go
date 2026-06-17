package handlers

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/gofiber/fiber/v2"
)

// CamouflageImage godoc
//
//	POST /v1/dashboard/shield/imgcamo
//
// Recebe uma imagem (multipart/form-data, campo "image"),
// aplica perturbação adversarial imperceptível e retorna a imagem camuflada.
//
// Form fields:
//   - image     (file)   — PNG ou JPEG, máx 10 MB
//   - technique (string) — "random_noise" | "checkerboard" | "spectral" | "hybrid" (default)
//   - epsilon   (int)    — intensidade 1–15 (default 5)
func (h *ShieldHandler) CamouflageImage(c *fiber.Ctx) error {
	// ── Parse multipart ─────────────────────────────────────────────
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "multipart form inválido: " + err.Error(),
		})
	}

	files := form.File["image"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "campo 'image' obrigatório",
		})
	}
	fh := files[0]

	// Validação de tamanho (10 MB)
	const maxSize = 10 << 20
	if fh.Size > maxSize {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
			"error": "imagem muito grande (máx 10 MB)",
		})
	}

	// Validação de tipo
	mime := fh.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/png"
	}
	if !strings.Contains(mime, "image/png") &&
		!strings.Contains(mime, "image/jpeg") &&
		!strings.Contains(mime, "image/jpg") {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{
			"error": "somente PNG e JPEG são suportados",
		})
	}

	// Lê os bytes da imagem
	f, err := fh.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "falha ao abrir imagem",
		})
	}
	defer f.Close()

	imgData := make([]byte, fh.Size)
	if _, err := f.Read(imgData); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "falha ao ler imagem",
		})
	}

	// ── Parâmetros opcionais ────────────────────────────────────────
	tech := shield.CamoTechnique(c.FormValue("technique", "hybrid"))
	eps := 0
	if v := c.FormValue("epsilon"); v != "" {
		fmt.Sscanf(v, "%d", &eps)
	}
	if eps < 1 || eps > 15 {
		eps = 5
	}

	blurRadius := 0
	if v := c.FormValue("blur_radius"); v != "" {
		fmt.Sscanf(v, "%d", &blurRadius)
	}

	// Opacidade da malha (técnica mesh_overlay). Aceita fração 0–1.
	opacity := 0.0
	if v := c.FormValue("opacity"); v != "" {
		fmt.Sscanf(v, "%f", &opacity)
	}
	if opacity < 0 || opacity > 1 {
		opacity = 0
	}

	// ── Imagem de capa (opcional) ────────────────────────────────────
	var coverData []byte
	coverFiles := form.File["cover"]
	if len(coverFiles) > 0 {
		cfh := coverFiles[0]
		if cfh.Size > 10<<20 {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
				"error": "imagem de capa muito grande (máx 10 MB)",
			})
		}
		cf, err2 := cfh.Open()
		if err2 == nil {
			coverData = make([]byte, cfh.Size)
			cf.Read(coverData) //nolint:errcheck
			cf.Close()
		}
		// Se cover foi enviada, força a técnica cover_blend
		tech = shield.TechCoverBlend
	}

	// ── Processa ────────────────────────────────────────────────────
	result, err := shield.CamouflageImage(shield.CamoRequest{
		ImageData:  imgData,
		MimeType:   mime,
		Technique:  tech,
		CoverImage: coverData,
		BlurRadius: blurRadius,
		Opacity:    opacity,
		Epsilon:    eps,
		Seed:      rand.Uint64(),
	})
	if err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error": "falha ao processar imagem: " + err.Error(),
		})
	}

	// ── Responde com a imagem perturbada ────────────────────────────
	// Retorna também métricas nos headers para o frontend exibir
	c.Set("Content-Type", result.MimeType)
	c.Set("X-Camo-Epsilon", fmt.Sprintf("%d", eps))
	c.Set("X-Camo-MaxDelta", fmt.Sprintf("%d", result.MaxDelta))
	c.Set("X-Camo-MeanDelta", fmt.Sprintf("%.2f", result.MeanDelta))
	c.Set("X-Camo-PSNR", fmt.Sprintf("%.1f", result.PSNR))
	c.Set("X-Camo-Technique", string(tech))
	c.Set("X-Camo-Opacity", fmt.Sprintf("%.4f", opacity))
	c.Set("X-Image-Width", fmt.Sprintf("%d", result.OrigWidth))
	c.Set("X-Image-Height", fmt.Sprintf("%d", result.OrigHeight))
	c.Set("Access-Control-Expose-Headers",
		"X-Camo-Epsilon,X-Camo-MaxDelta,X-Camo-MeanDelta,X-Camo-PSNR,X-Camo-Technique,X-Camo-Opacity,X-Image-Width,X-Image-Height")

	return c.Send(result.ImageData)
}
