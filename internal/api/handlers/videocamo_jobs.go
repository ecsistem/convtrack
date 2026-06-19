package handlers

import (
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/ecsistem/convtrack/internal/videojobs"
	"github.com/gofiber/fiber/v2"
)

// VideoCamoJobsHandler expõe a fila assíncrona de camuflagem de vídeo.
type VideoCamoJobsHandler struct {
	queue *videojobs.Queue
}

// NewVideoCamoJobs cria o handler com a fila informada.
func NewVideoCamoJobs(q *videojobs.Queue) *VideoCamoJobsHandler {
	return &VideoCamoJobsHandler{queue: q}
}

// presetParams traduz um preset em parâmetros de camuflagem.
func presetParams(preset string) (tech shield.CamoTechnique, eps int, compression string, strip bool) {
	switch preset {
	case "max":
		return shield.TechHybrid, 10, "medium", true
	case "fast":
		return shield.TechRandomNoise, 4, "light", true
	default: // custom — quem decide é o caller (lê os campos do form)
		return shield.TechHybrid, 5, "none", true
	}
}

// Enqueue godoc
//
//	POST /v1/dashboard/shield/videocamo/jobs
//
// Form fields:
//   - video        (file)   — MP4/WebM/MOV, máx 200 MB
//   - preset        (string) — "fast" | "max" | "custom"
//   - topic         (string) — id do tópico de áudio (opcional)
//   - technique/epsilon/compression/strip_metadata/blur_radius — só no preset "custom"
//   - cover         (file)   — opcional (cover_blend)
func (h *VideoCamoJobsHandler) Enqueue(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "projeto não selecionado"})
	}

	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "multipart inválido: " + err.Error()})
	}
	files := form.File["video"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "campo 'video' obrigatório"})
	}
	vfh := files[0]
	const maxVideoSize = 200 << 20
	if vfh.Size > maxVideoSize {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": "vídeo muito grande (máx 200 MB)"})
	}
	mime := vfh.Header.Get("Content-Type")
	if mime == "" {
		mime = "video/mp4"
	}
	if !strings.HasPrefix(mime, "video/") {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{"error": "somente vídeos (mp4, webm, mov)"})
	}
	vf, err := vfh.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao abrir vídeo"})
	}
	defer vf.Close()
	videoData := make([]byte, vfh.Size)
	if _, err := vf.Read(videoData); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao ler vídeo"})
	}

	// ── Preset ──────────────────────────────────────────────────────────────
	preset := c.FormValue("preset", "fast")
	if preset != "fast" && preset != "max" && preset != "custom" {
		preset = "fast"
	}
	tech, eps, compression, strip := presetParams(preset)

	blurRadius := 0
	opacity := 0.0
	if preset == "custom" {
		if v := c.FormValue("technique"); v != "" {
			tech = shield.CamoTechnique(v)
		}
		if v := c.FormValue("opacity"); v != "" {
			fmt.Sscanf(v, "%f", &opacity) //nolint:errcheck
		}
		if v := c.FormValue("epsilon"); v != "" {
			fmt.Sscanf(v, "%d", &eps) //nolint:errcheck
		}
		if v := c.FormValue("compression"); v != "" {
			compression = v
		}
		if v := c.FormValue("strip_metadata"); v != "" {
			strip = v == "true" || v == "1"
		}
		if v := c.FormValue("blur_radius"); v != "" {
			fmt.Sscanf(v, "%d", &blurRadius) //nolint:errcheck
		}
	}

	// ── Filtro de "desmarcação" (cor/enquadramento/ruído) ───────────────────
	// Aplicado em todos os presets: muda a assinatura do vídeo para a IA.
	filterOn := c.FormValue("filter") != "false" // ligado por padrão
	saturation := 1.06
	if v := c.FormValue("saturation"); v != "" {
		fmt.Sscanf(v, "%f", &saturation) //nolint:errcheck
	}
	filterStrength := 35
	if preset == "max" {
		filterStrength = 55
	}
	if v := c.FormValue("filter_strength"); v != "" {
		fmt.Sscanf(v, "%d", &filterStrength) //nolint:errcheck
	}
	if filterStrength < 0 || filterStrength > 100 {
		filterStrength = 35
	}
	if eps < 1 || eps > 15 {
		eps = 5
	}
	switch compression {
	case "none", "light", "medium", "high":
	default:
		compression = "none"
	}

	// ── Redimensionamento (opcional) ─────────────────────────────────────────
	resize := c.FormValue("resize", "")
	switch resize {
	case "9:16", "1:1", "16:9", "4:5":
	default:
		resize = ""
	}

	// ── Tópico de áudio (prompt injection) ──────────────────────────────────
	topic := c.FormValue("topic", "")
	if topic != "" {
		if _, ok := shield.AudioTopics[topic]; !ok {
			topic = ""
		}
	}

	// ── Imagem de capa opcional (força cover_blend) ─────────────────────────
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

	// ── Lote: quantas variações gerar (cada uma vira um anúncio diferente) ──
	count := 1
	if v := c.FormValue("count"); v != "" {
		fmt.Sscanf(v, "%d", &count) //nolint:errcheck
	}
	if count < 1 {
		count = 1
	}
	if count > 10 {
		count = 10
	}

	base := shield.CamoVideoRequest{
		MimeType:       mime,
		Technique:      tech,
		Epsilon:        eps,
		CoverImage:     coverData,
		BlurRadius:     blurRadius,
		StripMetadata:  strip,
		Compression:    compression,
		AudioTopic:     topic,
		Filter:         filterOn,
		Saturation:     saturation,
		FilterStrength: filterStrength,
		Opacity:        opacity,
		Resize:         resize,
	}

	jobs := make([]videojobs.Job, 0, count)
	for i := 0; i < count; i++ {
		req := base
		req.Seed = rand.Uint64() // semente única → padrão de perturbação completamente diferente
		if count > 1 {
			req.Saturation     = jitterSaturation(base.Saturation, i)
			req.FilterStrength = jitterFilterStrength(base.FilterStrength, i)
			if base.Technique == shield.TechMeshOverlay {
				// Grade: mantém técnica, varia a opacidade entre variações
				req.Opacity = jitterOpacity(base.Opacity, i)
			} else {
				req.Epsilon    = jitterEpsilon(base.Epsilon, i)
				req.Technique  = jitterTechnique(base.Technique, preset, i)
			}
		}
		name := vfh.Filename
		if count > 1 {
			name = fmt.Sprintf("v%02d_%s", i+1, vfh.Filename)
		}
		job, err := h.queue.Enqueue(project.ID.String(), name, videoExt(mime), videoData, req, preset, topic)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "falha ao enfileirar: " + err.Error()})
		}
		jobs = append(jobs, job)
	}
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"data": jobs})
}

// jitterSaturation varia saturação ±12 % por variação (antes eram ±3 %).
func jitterSaturation(base float64, i int) float64 {
	if base <= 0 {
		base = 1.0
	}
	deltas := []float64{0, 0.12, -0.12, 0.20, -0.20, 0.08, -0.08, 0.16, -0.16, 0.06}
	v := base + deltas[i%len(deltas)]
	if v < 0.5 {
		v = 0.5
	}
	if v > 1.5 {
		v = 1.5
	}
	return v
}

// jitterEpsilon varia a intensidade adversarial ±3 por variação, mantendo 1–15.
func jitterEpsilon(base, i int) int {
	deltas := []int{0, 3, -3, 5, -5, 2, -2, 4, -4, 6}
	v := base + deltas[i%len(deltas)]
	if v < 1 {
		v = 1
	}
	if v > 15 {
		v = 15
	}
	return v
}

// jitterFilterStrength varia a intensidade do filtro de desmarcação ±20 por variação.
func jitterFilterStrength(base, i int) int {
	deltas := []int{0, 15, -15, 25, -25, 10, -10, 20, -20, 5}
	v := base + deltas[i%len(deltas)]
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return v
}

// jitterOpacity varia a opacidade da grade (mesh_overlay) entre variações (0.01–0.20).
// base vem em fração 0–1 (ex: 0.02 = 2%).
func jitterOpacity(base float64, i int) float64 {
	deltas := []float64{0, 0.03, -0.01, 0.06, -0.015, 0.09, 0.01, 0.12, -0.005, 0.05}
	v := base + deltas[i%len(deltas)]
	if v < 0.005 {
		v = 0.005
	}
	if v > 0.20 {
		v = 0.20
	}
	return v
}

// jitterTechnique rotaciona técnica entre variações para máxima divergência de assinatura.
// No preset "custom" respeita a técnica escolhida pelo usuário.
func jitterTechnique(base shield.CamoTechnique, preset string, i int) shield.CamoTechnique {
	if preset == "custom" || base == shield.TechCoverBlend {
		return base // não troca se o usuário escolheu manualmente ou usa cover
	}
	rotation := []shield.CamoTechnique{
		shield.TechHybrid,
		shield.TechSpectral,
		shield.TechCheckerboard,
		shield.TechHybrid,
		shield.TechRandomNoise,
		shield.TechHybrid,
		shield.TechSpectral,
		shield.TechHybrid,
		shield.TechCheckerboard,
		shield.TechRandomNoise,
	}
	return rotation[i%len(rotation)]
}

// List godoc — GET /v1/dashboard/shield/videocamo/jobs
func (h *VideoCamoJobsHandler) List(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "projeto não selecionado"})
	}
	return c.JSON(fiber.Map{"data": h.queue.List(project.ID.String())})
}

// Download godoc — GET /v1/dashboard/shield/videocamo/jobs/:id/download
func (h *VideoCamoJobsHandler) Download(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "projeto não selecionado"})
	}
	path, filename, ok := h.queue.ResultPath(project.ID.String(), c.Params("id"))
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "resultado não disponível"})
	}
	out := "camo_" + sanitizeFilename(filename)
	if !strings.HasSuffix(strings.ToLower(out), ".mp4") {
		out += ".mp4"
	}
	c.Set("Content-Type", "video/mp4")
	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, out))
	c.Set("Access-Control-Expose-Headers", "Content-Disposition")
	return c.SendFile(path)
}

// Delete godoc — DELETE /v1/dashboard/shield/videocamo/jobs/:id
func (h *VideoCamoJobsHandler) Delete(c *fiber.Ctx) error {
	project := middleware.GetProject(c)
	if project == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "projeto não selecionado"})
	}
	if !h.queue.Delete(project.ID.String(), c.Params("id")) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "job não encontrado"})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func videoExt(mime string) string {
	switch {
	case strings.Contains(mime, "webm"):
		return ".webm"
	case strings.Contains(mime, "quicktime"), strings.Contains(mime, "mov"):
		return ".mov"
	case strings.Contains(mime, "avi"):
		return ".avi"
	case strings.Contains(mime, "x-matroska"), strings.Contains(mime, "mkv"):
		return ".mkv"
	default:
		return ".mp4"
	}
}

func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	if name == "" {
		return "video"
	}
	return name
}
