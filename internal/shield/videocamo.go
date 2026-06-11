package shield

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // registra decoder JPEG (também registrado em imgcamo.go; redundância inofensiva)
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ── Tipos públicos ────────────────────────────────────────────────────────────

// CamoVideoRequest descreve os parâmetros para camuflagem adversarial de vídeo.
type CamoVideoRequest struct {
	VideoData  []byte        // bytes do arquivo de vídeo (mp4/webm/mov)
	MimeType   string        // "video/mp4", "video/webm", "video/quicktime" ...
	Technique  CamoTechnique // técnica de perturbação (mesmas do imgcamo)
	Epsilon    int           // intensidade 1–15
	Seed       uint64        // semente PRNG
	CoverImage []byte        // imagem de capa opcional (força TechCoverBlend)
	BlurRadius int           // raio de mistura de frequências (2–30, default 8)
}

// CamoVideoResult contém o vídeo camuflado e métricas agregadas.
type CamoVideoResult struct {
	VideoData []byte  // bytes do vídeo mp4 gerado
	MimeType  string  // sempre "video/mp4"
	Frames    int     // total de frames processados
	FPS       float64 // taxa de frames do vídeo original
	MaxDelta  int     // Δ máximo médio por frame
	MeanDelta float64 // Δ médio por frame
	PSNR      float64 // PSNR médio (dB)
}

// ── Ponto de entrada ──────────────────────────────────────────────────────────

// CamouflageVideo processa um vídeo frame a frame aplicando camuflagem adversarial.
// Requer que ffmpeg esteja instalado no sistema (apk add ffmpeg / apt install ffmpeg).
func CamouflageVideo(req CamoVideoRequest) (*CamoVideoResult, error) {
	// ── Diretório temporário ──────────────────────────────────────────────────
	tmpDir, err := os.MkdirTemp("", "videocamo_*")
	if err != nil {
		return nil, fmt.Errorf("criar dir temp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// ── Escreve o vídeo original ──────────────────────────────────────────────
	ext := videoMimeToExt(req.MimeType)
	inputPath := filepath.Join(tmpDir, "input"+ext)
	if err := os.WriteFile(inputPath, req.VideoData, 0644); err != nil {
		return nil, fmt.Errorf("escrever vídeo: %w", err)
	}

	// ── Taxa de frames ────────────────────────────────────────────────────────
	fps, err := probeVideoFPS(inputPath)
	if err != nil || fps <= 0 {
		fps = 30.0
	}

	// ── Extrai frames ─────────────────────────────────────────────────────────
	framesDir := filepath.Join(tmpDir, "frames")
	if err := os.MkdirAll(framesDir, 0755); err != nil {
		return nil, err
	}

	out, err := exec.Command(
		"ffmpeg", "-i", inputPath,
		"-f", "image2",
		filepath.Join(framesDir, "frame_%05d.png"),
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("extrair frames: %w\nffmpeg: %s", err, out)
	}

	frames, err := filepath.Glob(filepath.Join(framesDir, "frame_*.png"))
	if err != nil || len(frames) == 0 {
		return nil, fmt.Errorf("nenhum frame extraído (ffmpeg: %s)", out)
	}
	sort.Strings(frames)

	// ── Pré-processa imagem de capa (decodifica + re-codifica como PNG uma vez) ─
	var coverPNG []byte
	if len(req.CoverImage) > 0 {
		coverImg, _, decErr := image.Decode(bytes.NewReader(req.CoverImage))
		if decErr == nil {
			var buf bytes.Buffer
			if encErr := png.Encode(&buf, coverImg); encErr == nil {
				coverPNG = buf.Bytes()
			}
		}
		// Se decodificação falhar, segue sem capa (ignora silenciosamente)
	}

	// ── Processa cada frame ───────────────────────────────────────────────────
	camoDir := filepath.Join(tmpDir, "camo")
	if err := os.MkdirAll(camoDir, 0755); err != nil {
		return nil, err
	}

	rng := &xorshift{state: req.Seed | 1} // seed != 0

	var totalMax   int
	var totalMean  float64
	var totalPSNR  float64

	for i, framePath := range frames {
		frameData, readErr := os.ReadFile(framePath)
		if readErr != nil {
			return nil, fmt.Errorf("ler frame %d: %w", i, readErr)
		}

		tech := req.Technique
		var cover []byte
		if len(coverPNG) > 0 {
			tech = TechCoverBlend
			cover = coverPNG
		}

		camo, camoErr := CamouflageImage(CamoRequest{
			ImageData:  frameData,
			MimeType:   "image/png",
			Technique:  tech,
			Epsilon:    req.Epsilon,
			Seed:       rng.next(),
			CoverImage: cover,
			BlurRadius: req.BlurRadius,
		})
		if camoErr != nil {
			return nil, fmt.Errorf("processar frame %d: %w", i, camoErr)
		}

		outPath := filepath.Join(camoDir, fmt.Sprintf("frame_%05d.png", i+1))
		if writeErr := os.WriteFile(outPath, camo.ImageData, 0644); writeErr != nil {
			return nil, writeErr
		}

		totalMax  += camo.MaxDelta
		totalMean += camo.MeanDelta
		totalPSNR += camo.PSNR
	}

	n := len(frames)
	avgMax  := totalMax / n
	avgMean := totalMean / float64(n)
	avgPSNR := totalPSNR / float64(n)

	// ── Remonta vídeo ─────────────────────────────────────────────────────────
	outputPath := filepath.Join(tmpDir, "output.mp4")
	fpsStr := strconv.FormatFloat(fps, 'f', 6, 64)

	out, err = exec.Command(
		"ffmpeg",
		"-framerate", fpsStr,
		"-i", filepath.Join(camoDir, "frame_%05d.png"),
		// áudio do vídeo original (mapa condicional — não falha se não houver áudio)
		"-i", inputPath,
		"-map", "0:v:0",
		"-map", "1:a?",
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "18",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-movflags", "+faststart",
		"-y", // sobrescreve se existir
		outputPath,
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("montar vídeo: %w\nffmpeg: %s", err, out)
	}

	videoData, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("ler vídeo montado: %w", err)
	}

	return &CamoVideoResult{
		VideoData: videoData,
		MimeType:  "video/mp4",
		Frames:    n,
		FPS:       fps,
		MaxDelta:  avgMax,
		MeanDelta: avgMean,
		PSNR:      avgPSNR,
	}, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// probeVideoFPS usa ffprobe para detectar a taxa de frames do vídeo.
func probeVideoFPS(path string) (float64, error) {
	out, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, err
	}

	s := strings.TrimSpace(string(out))
	// pode ser "30/1" ou "30000/1001"
	if parts := strings.SplitN(s, "/", 2); len(parts) == 2 {
		num, e1 := strconv.ParseFloat(parts[0], 64)
		den, e2 := strconv.ParseFloat(parts[1], 64)
		if e1 == nil && e2 == nil && den != 0 {
			return num / den, nil
		}
	}
	return strconv.ParseFloat(s, 64)
}

// videoMimeToExt retorna a extensão de arquivo para o MIME type de vídeo.
func videoMimeToExt(mime string) string {
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
