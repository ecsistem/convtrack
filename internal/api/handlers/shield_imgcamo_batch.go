package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/gofiber/fiber/v2"
)

// BatchCamouflageImage godoc
//
//	POST /v1/dashboard/shield/imgcamo/batch
//
// Igual ao /imgcamo, mas gera N variações (campo "count", 1–20) — cada uma com
// seed e intensidade levemente diferentes (hash/assinatura distintos) — e
// devolve tudo num .zip. Útil para subir vários anúncios a partir de um criativo.
func (h *ShieldHandler) BatchCamouflageImage(c *fiber.Ctx) error {
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "multipart inválido: " + err.Error()})
	}
	files := form.File["image"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "campo 'image' obrigatório"})
	}
	fh := files[0]
	if fh.Size > 10<<20 {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": "imagem muito grande (máx 10 MB)"})
	}
	mime := fh.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/png"
	}
	if !strings.Contains(mime, "image/png") && !strings.Contains(mime, "image/jpeg") && !strings.Contains(mime, "image/jpg") {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{"error": "somente PNG e JPEG"})
	}
	f, err := fh.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao abrir imagem"})
	}
	defer f.Close()
	imgData := make([]byte, fh.Size)
	if _, err := f.Read(imgData); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao ler imagem"})
	}

	// ── Parâmetros (mesmos do single) ───────────────────────────────────────
	tech := shield.CamoTechnique(c.FormValue("technique", "hybrid"))
	eps := 5
	if v := c.FormValue("epsilon"); v != "" {
		fmt.Sscanf(v, "%d", &eps) //nolint:errcheck
	}
	if eps < 1 || eps > 15 {
		eps = 5
	}
	blurRadius := 0
	if v := c.FormValue("blur_radius"); v != "" {
		fmt.Sscanf(v, "%d", &blurRadius) //nolint:errcheck
	}
	opacity := 0.0
	if v := c.FormValue("opacity"); v != "" {
		fmt.Sscanf(v, "%f", &opacity) //nolint:errcheck
	}
	compression := c.FormValue("compression", "none")
	switch compression {
	case "none", "light", "medium", "high":
	default:
		compression = "none"
	}
	resize := c.FormValue("resize", "")
	switch resize {
	case "9:16", "1:1", "16:9", "4:5":
	default:
		resize = ""
	}

	var coverData []byte
	if coverFiles := form.File["cover"]; len(coverFiles) > 0 {
		cfh := coverFiles[0]
		if cfh.Size <= 10<<20 {
			if cf, e := cfh.Open(); e == nil {
				coverData = make([]byte, cfh.Size)
				cf.Read(coverData) //nolint:errcheck
				cf.Close()
			}
		}
		tech = shield.TechCoverBlend
	}

	count := 1
	if v := c.FormValue("count"); v != "" {
		fmt.Sscanf(v, "%d", &count) //nolint:errcheck
	}
	if count < 1 {
		count = 1
	}
	if count > 20 {
		count = 20
	}

	// ── Gera as variações e empacota no zip ─────────────────────────────────
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	generated := 0
	for i := 0; i < count; i++ {
		variantEps := eps
		if count > 1 {
			// jitter ±1 (mantém 1–15) para diversificar a perturbação
			variantEps = clampInt(eps+(i%3)-1, 1, 15)
		}
		res, err := shield.CamouflageImage(shield.CamoRequest{
			ImageData:   imgData,
			MimeType:    mime,
			Technique:   tech,
			CoverImage:  coverData,
			BlurRadius:  blurRadius,
			Opacity:     opacity,
			Compression: compression,
			Resize:      resize,
			Epsilon:     variantEps,
			Seed:        rand.Uint64(),
		})
		if err != nil {
			continue
		}
		ext := ".png"
		if strings.Contains(res.MimeType, "jpeg") || strings.Contains(res.MimeType, "jpg") {
			ext = ".jpg"
		}
		w, err := zw.Create(fmt.Sprintf("variacao_%02d%s", i+1, ext))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		if _, err := w.Write(res.ImageData); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		generated++

		if project := middleware.GetProject(c); project != nil {
			variantName := fmt.Sprintf("variacao_%02d_%s", i+1, fh.Filename)
			data := res.ImageData
			go h.saveImgCamoLog(project.ID, variantName, string(tech), variantEps, res.MimeType, data)
		}
	}
	if generated == 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{"error": "falha ao gerar variações"})
	}
	if err := zw.Close(); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	c.Set("Content-Type", "application/zip")
	c.Set("Content-Disposition", `attachment; filename="variacoes.zip"`)
	c.Set("X-Camo-Count", fmt.Sprintf("%d", generated))
	c.Set("Access-Control-Expose-Headers", "X-Camo-Count,Content-Disposition")
	return c.Send(buf.Bytes())
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
