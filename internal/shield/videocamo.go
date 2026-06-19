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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// Pós-processamento do container de saída:
	StripMetadata bool   // remove EXIF/metadados (origem, device, edição)
	Compression   string // "none" | "light" | "medium" | "high"

	// Injeção de áudio (prompt injection): mixa uma faixa TTS em volume baixo
	// com palavras do tópico, para a transcrição da IA "ouvir" o tópico errado.
	AudioTopic string // id do tópico (ver AudioTopics); "" desativa

	// Filtro de "desmarcação": variações imperceptíveis de cor/enquadramento/ruído
	// que mudam a assinatura do vídeo para o algoritmo achar que é outro criativo.
	Filter         bool    // ativa o filtro visual
	Saturation     float64 // saturação (1.0 = neutro; 0.5–1.5)
	FilterStrength int     // intensidade das demais variações (0–100)

	Opacity  float64               // opacidade da malha para TechMeshOverlay (0–1)
	Progress func(done, total int) // callback de progresso (opcional, não serializado)

	// Redimensionamento (opcional): "" | "9:16" | "1:1" — preenche o quadro
	// (cover, sem barras pretas) em 720p, pronto pra campanha.
	Resize string
}

// buildResizeFilter devolve o filtro ffmpeg de redimensionamento "cover" (sem
// barras pretas) em 720p para o formato escolhido. "" = sem redimensionar.
func buildResizeFilter(format string) string {
	var w, h int
	switch format {
	case "9:16":
		w, h = 720, 1280
	case "1:1":
		w, h = 720, 720
	case "16:9":
		w, h = 1280, 720
	case "4:5":
		w, h = 720, 900
	default:
		return ""
	}
	// scale cobrindo o quadro + crop central → preenche sem barras
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, h, w, h)
}

// buildVideoFilter monta a cadeia de filtros de vídeo do ffmpeg para a
// "desmarcação" do criativo. Retorna "" quando o filtro está desativado.
func buildVideoFilter(req CamoVideoRequest) string {
	if !req.Filter {
		return ""
	}
	sat := req.Saturation
	if sat <= 0 {
		sat = 1.0
	}
	if sat < 0.5 {
		sat = 0.5
	}
	if sat > 1.5 {
		sat = 1.5
	}
	s := req.FilterStrength
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	f := float64(s) / 100.0

	bright := f * 0.04       // brilho: 0 … 0.04
	contrast := 1.0 + f*0.06 // contraste: 1.00 … 1.06
	hue := f * 6.0           // matiz: 0 … 6 graus
	noise := int(f * 10.0)   // ruído: 0 … 10
	zoom := 1.0 + f*0.03     // enquadramento: 0% … 3% de zoom central

	parts := []string{
		fmt.Sprintf("eq=saturation=%.3f:brightness=%.3f:contrast=%.3f", sat, bright, contrast),
	}
	if hue > 0.1 {
		parts = append(parts, fmt.Sprintf("hue=h=%.2f", hue))
	}
	if zoom > 1.0005 {
		// zoom leve + crop central de volta ao tamanho ~original (muda o enquadramento)
		parts = append(parts,
			fmt.Sprintf("scale=trunc(iw*%.4f/2)*2:trunc(ih*%.4f/2)*2", zoom, zoom),
			fmt.Sprintf("crop=trunc(iw/%.4f/2)*2:trunc(ih/%.4f/2)*2", zoom, zoom),
		)
	}
	if noise > 0 {
		parts = append(parts, fmt.Sprintf("noise=alls=%d:allf=t", noise))
	}
	return strings.Join(parts, ",")
}

// AudioTopics mapeia o id de cada tópico para a frase (pt-BR) sintetizada via
// TTS e mixada em volume baixo sob o áudio original.
var AudioTopics = map[string]string{
	"financas":     "renda fixa, investimento, juros, tesouro direto, dividendos, poupança, economia, mercado financeiro",
	"marketing":    "marketing digital, tráfego pago, funil de vendas, copywriting, engajamento, conversão, audiência",
	"saude":        "saúde, bem estar, qualidade de vida, exercício físico, sono, hidratação, equilíbrio",
	"nutricao":     "nutrição, alimentação saudável, proteína, vitaminas, dieta equilibrada, refeição, metabolismo",
	"motivacional": "motivação, mentalidade, foco, disciplina, propósito, crescimento pessoal, hábitos",
	"tecnologia":   "tecnologia, tutorial, programação, software, inovação, ferramenta, automação",
	"culinaria":    "culinária, receita, ingredientes, tempero, cozinha, preparo, sabor",
	"educacao":     "educação infantil, aprendizado, brincadeira, alfabetização, desenvolvimento, criança, escola",
}

// compressionParams mapeia o nível de compressão para (crf, preset) do x264.
// Quanto maior o CRF, menor o arquivo (e mais perda). "none" = visualmente sem perda.
func compressionParams(level string) (crf, preset string) {
	switch strings.ToLower(level) {
	case "light":
		return "23", "medium"
	case "medium":
		return "28", "medium"
	case "high":
		return "32", "slow"
	default: // none
		return "18", "fast"
	}
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

	// ── Processa frames em paralelo com pool de goroutines ───────────────────
	camoDir := filepath.Join(tmpDir, "camo")
	if err := os.MkdirAll(camoDir, 0755); err != nil {
		return nil, err
	}

	// Pré-gera seeds de forma determinística (xorshift é serial, não thread-safe)
	rng := &xorshift{state: req.Seed | 1}
	seeds := make([]uint64, len(frames))
	for i := range seeds {
		seeds[i] = rng.next()
	}

	// Resultado por frame (pré-alocado, acesso por índice → sem lock)
	type frameResult struct {
		maxDelta  int
		meanDelta float64
		psnr      float64
		err       error
	}
	results := make([]frameResult, len(frames))

	// Pool de goroutines: usa todos os CPUs disponíveis
	numWorkers := runtime.NumCPU()
	if numWorkers < 2 {
		numWorkers = 2
	}

	type frameTask struct {
		idx  int
		path string
	}
	taskCh := make(chan frameTask, len(frames))
	for i, p := range frames {
		taskCh <- frameTask{idx: i, path: p}
	}
	close(taskCh)

	var wg sync.WaitGroup
	var doneCount atomic.Int64

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range taskCh {
				frameData, readErr := os.ReadFile(t.path)
				if readErr != nil {
					results[t.idx].err = fmt.Errorf("ler frame %d: %w", t.idx, readErr)
					doneCount.Add(1)
					continue
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
					Seed:       seeds[t.idx],
					CoverImage: cover,
					BlurRadius: req.BlurRadius,
					Opacity:    req.Opacity,
				})
				if camoErr != nil {
					results[t.idx].err = fmt.Errorf("processar frame %d: %w", t.idx, camoErr)
					doneCount.Add(1)
					continue
				}

				outPath := filepath.Join(camoDir, fmt.Sprintf("frame_%05d.png", t.idx+1))
				if writeErr := os.WriteFile(outPath, camo.ImageData, 0644); writeErr != nil {
					results[t.idx].err = writeErr
					doneCount.Add(1)
					continue
				}

				results[t.idx] = frameResult{
					maxDelta:  camo.MaxDelta,
					meanDelta: camo.MeanDelta,
					psnr:      camo.PSNR,
				}

				n := int(doneCount.Add(1))
				if req.Progress != nil {
					req.Progress(n, len(frames))
				}
			}
		}()
	}
	wg.Wait()

	// Verifica erros e agrega métricas
	var totalMax int
	var totalMean, totalPSNR float64
	for i, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		totalMax += r.maxDelta
		totalMean += r.meanDelta
		totalPSNR += r.psnr
		_ = i
	}

	n := len(frames)
	avgMax := totalMax / n
	avgMean := totalMean / float64(n)
	avgPSNR := totalPSNR / float64(n)

	// ── Remonta vídeo ─────────────────────────────────────────────────────────
	outputPath := filepath.Join(tmpDir, "output.mp4")
	fpsStr := strconv.FormatFloat(fps, 'f', 6, 64)

	crf, preset := compressionParams(req.Compression)

	// ── Injeção de áudio (TTS do tópico em volume baixo) ──────────────────────
	ttsPath := ""
	if phrase := AudioTopics[req.AudioTopic]; phrase != "" {
		ttsPath = filepath.Join(tmpDir, "tts.wav")
		if e := generateTTS(phrase, ttsPath); e != nil {
			ttsPath = "" // best-effort: segue sem injeção se o TTS falhar
		}
	}
	srcHasAudio := hasAudioStream(inputPath)
	vf := buildVideoFilter(req)
	if rf := buildResizeFilter(req.Resize); rf != "" {
		if vf != "" {
			vf += "," + rf
		} else {
			vf = rf
		}
	}

	args := []string{
		"-framerate", fpsStr,
		"-i", filepath.Join(camoDir, "frame_%05d.png"), // input 0: vídeo
		"-i", inputPath, // input 1: áudio original
	}

	if ttsPath != "" {
		args = append(args, "-stream_loop", "-1", "-i", ttsPath) // input 2: TTS (em loop)

		// Com injeção de áudio precisamos de filter_complex; o filtro de vídeo,
		// se houver, entra no mesmo grafo gerando [vout].
		videoMap := "0:v:0"
		fc := ""
		if vf != "" {
			fc = "[0:v]" + vf + "[vout];"
			videoMap = "[vout]"
		}
		if srcHasAudio {
			// amix duration=first → casa com o áudio original (evita saída infinita do TTS em loop)
			fc += "[1:a]volume=1.0[a0];[2:a]volume=0.08[a1];[a0][a1]amix=inputs=2:duration=first[aout]"
		} else {
			fc += "[2:a]volume=0.15[aout]"
		}
		// -t limita a saída à duração do vídeo (o TTS está em loop infinito)
		durStr := strconv.FormatFloat(float64(len(frames))/fps, 'f', 3, 64)
		args = append(args, "-filter_complex", fc, "-map", videoMap, "-map", "[aout]", "-t", durStr)
	} else {
		// Sem injeção: filtro de vídeo via -vf (se houver) + áudio original opcional.
		if vf != "" {
			args = append(args, "-vf", vf)
		}
		args = append(args, "-map", "0:v:0", "-map", "1:a?")
	}

	args = append(args,
		"-c:v", "libx264",
		"-preset", preset,
		"-crf", crf,
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-movflags", "+faststart",
	)
	// Limpeza de metadados: remove EXIF/metadados do container e capítulos,
	// que entregam origem, device e histórico de edição (útil para evitar
	// detecção por hash ao subir o mesmo arquivo várias vezes).
	if req.StripMetadata {
		args = append(args,
			"-map_metadata", "-1",
			"-map_chapters", "-1",
			// -bitexact impede o ffmpeg de re-gravar a tag encoder/versão,
			// deixando só os marcadores de formato (não identificáveis).
			"-fflags", "+bitexact",
			"-flags:v", "+bitexact",
		)
	}
	args = append(args, "-y", outputPath)

	out, err = exec.Command("ffmpeg", args...).CombinedOutput()
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

// hasAudioStream verifica via ffprobe se o vídeo possui faixa de áudio.
func hasAudioStream(path string) bool {
	out, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_type",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	return err == nil && strings.Contains(string(out), "audio")
}

// generateTTS sintetiza a frase (pt-BR) em um WAV usando espeak-ng (ou espeak).
func generateTTS(text, outPath string) error {
	for _, bin := range []string{"espeak-ng", "espeak"} {
		if _, err := exec.LookPath(bin); err != nil {
			continue
		}
		out, err := exec.Command(bin,
			"-v", "pt-br",
			"-s", "160", // velocidade
			"-p", "50", // pitch
			"-w", outPath,
			text,
		).CombinedOutput()
		if err == nil {
			return nil
		}
		// alguns builds usam "pt" em vez de "pt-br"
		out, err = exec.Command(bin, "-v", "pt", "-s", "160", "-w", outPath, text).CombinedOutput()
		if err == nil {
			return nil
		}
		return fmt.Errorf("%s: %w (%s)", bin, err, out)
	}
	return fmt.Errorf("nenhum motor TTS disponível (espeak-ng/espeak)")
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
