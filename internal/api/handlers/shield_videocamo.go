package handlers

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/gofiber/fiber/v2"
)

// CamouflageVideo godoc
//
//	POST /v1/dashboard/shield/videocamo
//
// Recebe um vídeo (multipart/form-data, campo "video") e uma imagem de capa
// opcional (campo "cover"), aplica camuflagem adversarial frame a frame e
// retorna o vídeo camuflado em MP4.
//
// Form fields:
//   - video      (file)   — MP4, WebM ou MOV, máx 200 MB
//   - cover      (file)   — PNG ou JPEG, máx 10 MB  (opcional; força technique=cover_blend)
//   - technique  (string) — "random_noise" | "checkerboard" | "spectral" | "hybrid" | "cover_blend"
//   - epsilon    (int)    — intensidade 1–15 (default 5)
//   - blur_radius (int)   — raio de mistura 2–30 (default 8, só para cover_blend)
//
// Response headers:
//
//	X-Camo-PSNR, X-Camo-MaxDelta, X-Camo-MeanDelta, X-Camo-Technique,
//	X-Camo-Epsilon, X-Video-Frames, X-Video-FPS
func (h *ShieldHandler) CamouflageVideo(c *fiber.Ctx) error {
	// ── Parse multipart ────────────────────────────────────────────────────
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "multipart form inválido: " + err.Error(),
		})
	}

	// ── Vídeo ──────────────────────────────────────────────────────────────
	files := form.File["video"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "campo 'video' obrigatório",
		})
	}
	vfh := files[0]

	const maxVideoSize = 200 << 20 // 200 MB
	if vfh.Size > maxVideoSize {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
			"error": "vídeo muito grande (máx 200 MB)",
		})
	}

	mime := vfh.Header.Get("Content-Type")
	if mime == "" {
		mime = "video/mp4"
	}
	if !strings.HasPrefix(mime, "video/") {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{
			"error": "somente arquivos de vídeo são suportados (mp4, webm, mov)",
		})
	}

	vf, err := vfh.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "falha ao abrir vídeo",
		})
	}
	defer vf.Close()

	videoData := make([]byte, vfh.Size)
	if _, err := vf.Read(videoData); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "falha ao ler vídeo",
		})
	}

	// ── Parâmetros opcionais ────────────────────────────────────────────────
	tech := shield.CamoTechnique(c.FormValue("technique", "hybrid"))
	eps := 0
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

	// ── Limpeza de metadados + compressão ───────────────────────────────────
	stripMeta := c.FormValue("strip_metadata") == "true" || c.FormValue("strip_metadata") == "1"
	compression := c.FormValue("compression", "none")
	switch compression {
	case "none", "light", "medium", "high":
	default:
		compression = "none"
	}

	// ── Imagem de capa (opcional) ───────────────────────────────────────────
	var coverData []byte
	if coverFiles := form.File["cover"]; len(coverFiles) > 0 {
		cfh := coverFiles[0]
		if cfh.Size <= 10<<20 {
			cf, err2 := cfh.Open()
			if err2 == nil {
				coverData = make([]byte, cfh.Size)
				cf.Read(coverData) //nolint:errcheck
				cf.Close()
			}
		}
		// capa fornecida → força técnica de mistura por frequências
		tech = shield.TechCoverBlend
	}

	// ── Processa ────────────────────────────────────────────────────────────
	result, err := shield.CamouflageVideo(shield.CamoVideoRequest{
		VideoData:     videoData,
		MimeType:      mime,
		Technique:     tech,
		Epsilon:       eps,
		Seed:          rand.Uint64(),
		CoverImage:    coverData,
		BlurRadius:    blurRadius,
		StripMetadata: stripMeta,
		Compression:   compression,
	})
	if err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error": "falha ao processar vídeo: " + err.Error(),
		})
	}

	// ── Responde ────────────────────────────────────────────────────────────
	c.Set("Content-Type", "video/mp4")
	c.Set("Content-Disposition", `attachment; filename="camo_video.mp4"`)
	c.Set("X-Camo-Epsilon", fmt.Sprintf("%d", eps))
	c.Set("X-Camo-MaxDelta", fmt.Sprintf("%d", result.MaxDelta))
	c.Set("X-Camo-MeanDelta", fmt.Sprintf("%.2f", result.MeanDelta))
	c.Set("X-Camo-PSNR", fmt.Sprintf("%.1f", result.PSNR))
	c.Set("X-Camo-Technique", string(tech))
	c.Set("X-Video-Frames", fmt.Sprintf("%d", result.Frames))
	c.Set("X-Video-FPS", fmt.Sprintf("%.2f", result.FPS))
	c.Set("Access-Control-Expose-Headers",
		"X-Camo-Epsilon,X-Camo-MaxDelta,X-Camo-MeanDelta,X-Camo-PSNR,"+
			"X-Camo-Technique,X-Video-Frames,X-Video-FPS")

	return c.Send(result.VideoData)
}
