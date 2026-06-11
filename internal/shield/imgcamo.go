package shield

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"strings"
)

// CamoTechnique representa o algoritmo de perturbação adversarial.
type CamoTechnique string

const (
	// TechRandomNoise — ruído gaussiano pseudo-aleatório (ε por pixel).
	// Simples e eficaz contra detectores baseados em textura.
	TechRandomNoise CamoTechnique = "random_noise"

	// TechCheckerboard — padrão xadrez de alta frequência (±ε alternado por pixel).
	// Explora a sensibilidade de CNNs a padrões de frequência específicos.
	TechCheckerboard CamoTechnique = "checkerboard"

	// TechSpectral — perturbação no domínio de frequência (aproximação DCT).
	// Modifica coeficientes de alta frequência — invisível para humanos,
	// mas rompe a análise espectral de modelos de visão computacional.
	TechSpectral CamoTechnique = "spectral"

	// TechHybrid — combina ruído aleatório + padrão espectral (mais robusto).
	TechHybrid CamoTechnique = "hybrid"

	// TechCoverBlend — mistura frequência baixa da imagem de capa com alta freq do original.
	// A IA classifica pela estrutura global (capa); humanos veem o criativo original.
	// Requer CoverImage no CamoRequest.
	TechCoverBlend CamoTechnique = "cover_blend"
)

// CamoRequest é a entrada do serviço de camuflagem.
type CamoRequest struct {
	ImageData  []byte        // dados brutos da imagem (PNG ou JPEG)
	MimeType   string        // "image/png" ou "image/jpeg"
	Technique  CamoTechnique // algoritmo de perturbação
	Epsilon    int           // intensidade: 1–15 (quanto maior, mais robusta a perturbação)
	Seed       uint64        // semente para reprodutibilidade (0 = aleatório)
	CoverImage []byte        // imagem de capa para TechCoverBlend (PNG ou JPEG)
	BlurRadius int           // raio do blur de separação de frequência (default 8, range 2–30)
}

// CamoResult é a saída com a imagem perturbada.
type CamoResult struct {
	ImageData  []byte
	MimeType   string
	OrigWidth  int
	OrigHeight int
	// Métricas de invisibilidade
	MaxDelta   int     // maior diferença de pixel (0–255)
	MeanDelta  float64 // diferença média por pixel
	PSNR       float64 // Peak Signal-to-Noise Ratio em dB (>40 dB = imperceptível)
}

// CamouflageImage aplica perturbação adversarial imperceptível à imagem.
// O resultado é visualmente idêntico ao original, mas engana modelos de IA.
func CamouflageImage(req CamoRequest) (*CamoResult, error) {
	// ── Decodifica a imagem original ────────────────────────────────
	orig, fmt, err := image.Decode(bytes.NewReader(req.ImageData))
	if err != nil {
		return nil, err
	}
	_ = fmt

	bounds := orig.Bounds()
	w, h := bounds.Max.X, bounds.Max.Y

	// ── Copia para RGBA mutável ─────────────────────────────────────
	dst := image.NewNRGBA(bounds)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, orig.At(x, y))
		}
	}

	// ── Semente e intensidade ───────────────────────────────────────
	seed := req.Seed
	if seed == 0 {
		seed = rand.Uint64()
	}
	eps := req.Epsilon
	if eps < 1 {
		eps = 3
	}
	if eps > 15 {
		eps = 15
	}

	tech := req.Technique
	if tech == "" {
		tech = TechHybrid
	}

	// ── Aplica perturbação ──────────────────────────────────────────
	switch tech {
	case TechRandomNoise:
		applyRandomNoise(dst, bounds, seed, eps)
	case TechCheckerboard:
		applyCheckerboard(dst, bounds, seed, eps)
	case TechSpectral:
		applySpectral(dst, bounds, seed, eps)
	case TechHybrid:
		// duas passagens: ruído + espectral com metade da intensidade cada
		applyRandomNoise(dst, bounds, seed, max(1, eps/2))
		applySpectral(dst, bounds, seed+1, max(1, eps/2))
	case TechCoverBlend:
		if len(req.CoverImage) == 0 {
			// fallback sem capa: usa hybrid
			applyRandomNoise(dst, bounds, seed, max(1, eps/2))
			applySpectral(dst, bounds, seed+1, max(1, eps/2))
		} else {
			cover, _, err2 := image.Decode(bytes.NewReader(req.CoverImage))
			if err2 != nil {
				return nil, err2
			}
			radius := req.BlurRadius
			if radius < 2 {
				radius = 8
			}
			if radius > 30 {
				radius = 30
			}
			// alpha: quanto da estrutura da capa embute (eps mapeado para 5%–35%)
			alpha := 0.05 + float64(eps-1)*0.02 // eps=1→5%, eps=15→33%
			applyCoverBlend(dst, cover, bounds, radius, alpha)
		}
	}

	// ── Mede invisibilidade ─────────────────────────────────────────
	maxD, meanD, psnr := measureDelta(orig, dst, bounds)

	// ── Codifica a imagem resultante ────────────────────────────────
	var buf bytes.Buffer
	mimeOut := req.MimeType
	if mimeOut == "" {
		mimeOut = "image/png"
	}

	if strings.Contains(mimeOut, "jpeg") || strings.Contains(mimeOut, "jpg") {
		// JPEG: qualidade 95 preserva a perturbação
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 95}); err != nil {
			return nil, err
		}
	} else {
		mimeOut = "image/png"
		if err := png.Encode(&buf, dst); err != nil {
			return nil, err
		}
	}

	return &CamoResult{
		ImageData:  buf.Bytes(),
		MimeType:   mimeOut,
		OrigWidth:  w,
		OrigHeight: h,
		MaxDelta:   maxD,
		MeanDelta:  meanD,
		PSNR:       psnr,
	}, nil
}

// ── Técnicas de perturbação ──────────────────────────────────────────────────

// applyRandomNoise — ruído gaussiano pseudo-aleatório por canal RGB.
// Usa xorshift para performance sem dependências externas.
func applyRandomNoise(dst *image.NRGBA, bounds image.Rectangle, seed uint64, eps int) {
	rng := newXorshift(seed)
	feps := float64(eps)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := dst.NRGBAAt(x, y).R, dst.NRGBAAt(x, y).G, dst.NRGBAAt(x, y).B, dst.NRGBAAt(x, y).A
			// δ ∈ [-eps, +eps] distribuído como gaussiana truncada
			dr := int(gaussSample(rng) * feps)
			dg := int(gaussSample(rng) * feps)
			db := int(gaussSample(rng) * feps)
			dst.SetNRGBA(x, y, color.NRGBA{
				R: clamp8(int(r) + dr),
				G: clamp8(int(g) + dg),
				B: clamp8(int(b) + db),
				A: a,
			})
		}
	}
}

// applyCheckerboard — padrão xadrez ±eps por pixel (alta frequência).
// Extremamente eficaz contra CNNs que usam frequências altas como features.
func applyCheckerboard(dst *image.NRGBA, bounds image.Rectangle, seed uint64, eps int) {
	rng := newXorshift(seed)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			// Padrão base: sinal alterna a cada pixel
			sign := 1
			if (x+y)%2 == 0 {
				sign = -1
			}
			// Jitter aleatório leve para resistir a filtragem simples
			jitter := int(rng.next()%3) - 1
			delta := sign*(eps) + jitter

			c := dst.NRGBAAt(x, y)
			// Aplica mais no canal azul (menos sensível ao olho humano)
			dst.SetNRGBA(x, y, color.NRGBA{
				R: clamp8(int(c.R) + delta/2),
				G: clamp8(int(c.G) + delta/2),
				B: clamp8(int(c.B) + delta),
				A: c.A,
			})
		}
	}
}

// applySpectral — perturbação em padrões de frequência espacial.
// Injeta ondas senoidais de alta frequência com fase pseudo-aleatória.
// CNNs são sensíveis a esses padrões mesmo quando invisíveis para humanos.
func applySpectral(dst *image.NRGBA, bounds image.Rectangle, seed uint64, eps int) {
	rng := newXorshift(seed)
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y
	if w == 0 || h == 0 {
		return
	}

	// Frequências aleatórias em 3 bandas (alta, muito alta, ultra-alta)
	type wave struct{ fx, fy, phase, amp float64 }
	waves := make([]wave, 8)
	for i := range waves {
		// Frequências entre 0.1 e 0.5 ciclos/pixel (alta frequência)
		fx := 0.1 + float64(rng.next()%40)/100.0
		fy := 0.1 + float64(rng.next()%40)/100.0
		phase := float64(rng.next()%628) / 100.0 // 0..2π
		amp := float64(eps) * (0.3 + float64(rng.next()%70)/100.0)
		waves[i] = wave{fx, fy, phase, amp}
	}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		ny := float64(y - bounds.Min.Y)
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			nx := float64(x - bounds.Min.X)
			var deltaR, deltaG, deltaB float64
			for i, wv := range waves {
				v := wv.amp * math.Sin(2*math.Pi*(wv.fx*nx/float64(w)+wv.fy*ny/float64(h))+wv.phase)
				switch i % 3 {
				case 0:
					deltaR += v
				case 1:
					deltaG += v
				case 2:
					deltaB += v
				}
			}
			c := dst.NRGBAAt(x, y)
			dst.SetNRGBA(x, y, color.NRGBA{
				R: clamp8(int(c.R) + int(deltaR)),
				G: clamp8(int(c.G) + int(deltaG)),
				B: clamp8(int(c.B) + int(deltaB)),
				A: c.A,
			})
		}
	}
}

// ── Cover Blend ──────────────────────────────────────────────────────────────

// applyCoverBlend — mistura frequências da imagem de capa com o original.
//
// Princípio: CNNs de classificação dependem fortemente das frequências baixas
// (estrutura global, composição, paleta) para decidir a categoria da imagem.
// Frequências altas (bordas, texturas finas) são percebidas pelos humanos como
// os detalhes visuais principais.
//
// Algoritmo:
//  1. Blur(original, r)  → lowOrig  (freq baixa do original)
//  2. original - lowOrig → highOrig (freq alta do original)
//  3. Blur(cover, r)     → lowCover (freq baixa da capa, redimensionada)
//  4. result = highOrig + lerp(lowOrig, lowCover, alpha)
//
// Com alpha pequeno (5–20%) o resultado parece idêntico ao original para humanos,
// mas o modelo de IA "vê" a estrutura global da capa.
func applyCoverBlend(dst *image.NRGBA, cover image.Image, bounds image.Rectangle, radius int, alpha float64) {
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y
	if w == 0 || h == 0 {
		return
	}

	// 1. Baixa frequência do original (box blur)
	lowOrig := boxBlur(dst, bounds, radius)

	// 2. Baixa frequência da capa (redimensiona + box blur)
	coverScaled := resizeNearest(cover, w, h)
	coverBounds := image.Rect(0, 0, w, h)
	lowCover := boxBlur(coverScaled, coverBounds, radius)

	// 3. Composição pixel a pixel
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		row := (y - bounds.Min.Y) * w
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			idx := row + (x - bounds.Min.X)
			orig := dst.NRGBAAt(x, y)
			lo := lowOrig[idx]
			lc := lowCover[idx]

			// Frequência alta do original = orig - lowOrig
			hiR := int(orig.R) - lo[0]
			hiG := int(orig.G) - lo[1]
			hiB := int(orig.B) - lo[2]

			// Mistura das baixas frequências
			blendR := float64(lo[0])*(1-alpha) + float64(lc[0])*alpha
			blendG := float64(lo[1])*(1-alpha) + float64(lc[1])*alpha
			blendB := float64(lo[2])*(1-alpha) + float64(lc[2])*alpha

			dst.SetNRGBA(x, y, color.NRGBA{
				R: clamp8(int(blendR) + hiR),
				G: clamp8(int(blendG) + hiG),
				B: clamp8(int(blendB) + hiB),
				A: orig.A,
			})
		}
	}
}

// boxBlur aplica blur de caixa separável (passa H + passa V) e retorna
// uma slice [h][w] de [3]int com os canais RGB suavizados.
func boxBlur(src *image.NRGBA, bounds image.Rectangle, r int) [][3]int {
	// Usamos um slice linear row-major para evitar múltiplos níveis de slice.
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y
	tmp := make([][3]float64, w*h)
	out := make([][3]int, w*h)

	// Passa horizontal
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sR, sG, sB float64
			cnt := 0
			for dx := -r; dx <= r; dx++ {
				nx := x + dx
				if nx < 0 || nx >= w {
					continue
				}
				c := src.NRGBAAt(bounds.Min.X+nx, bounds.Min.Y+y)
				sR += float64(c.R)
				sG += float64(c.G)
				sB += float64(c.B)
				cnt++
			}
			if cnt > 0 {
				tmp[y*w+x] = [3]float64{sR / float64(cnt), sG / float64(cnt), sB / float64(cnt)}
			}
		}
	}

	// Passa vertical
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sR, sG, sB float64
			cnt := 0
			for dy := -r; dy <= r; dy++ {
				ny := y + dy
				if ny < 0 || ny >= h {
					continue
				}
				sR += tmp[ny*w+x][0]
				sG += tmp[ny*w+x][1]
				sB += tmp[ny*w+x][2]
				cnt++
			}
			if cnt > 0 {
				out[y*w+x] = [3]int{int(sR / float64(cnt)), int(sG / float64(cnt)), int(sB / float64(cnt))}
			}
		}
	}
	return out
}


// resizeNearest redimensiona src para (w, h) usando interpolação nearest-neighbor
// e retorna um *image.NRGBA com bounds (0,0)-(w,h).
func resizeNearest(src image.Image, w, h int) *image.NRGBA {
	sb := src.Bounds()
	sw := sb.Max.X - sb.Min.X
	sh := sb.Max.Y - sb.Min.Y
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := sb.Min.Y + (y*sh)/h
		for x := 0; x < w; x++ {
			sx := sb.Min.X + (x*sw)/w
			r, g, b, a := src.At(sx, sy).RGBA()
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(b >> 8),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

// ── Métricas de qualidade ────────────────────────────────────────────────────

func measureDelta(orig image.Image, perturbed *image.NRGBA, bounds image.Rectangle) (maxD int, meanD float64, psnr float64) {
	w := bounds.Max.X - bounds.Min.X
	h := bounds.Max.Y - bounds.Min.Y
	total := w * h
	if total == 0 {
		return 0, 0, math.Inf(1)
	}
	var sumSq float64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			or_, og, ob, _ := orig.At(x, y).RGBA()
			pr, pg, pb, _ := perturbed.At(x, y).RGBA()
			dr := int(or_>>8) - int(pr>>8)
			dg := int(og>>8) - int(pg>>8)
			db := int(ob>>8) - int(pb>>8)
			// L∞ por pixel
			d := max(abs(dr), max(abs(dg), abs(db)))
			if d > maxD {
				maxD = d
			}
			meanD += float64(d)
			// MSE por canal
			sumSq += float64(dr*dr + dg*dg + db*db)
		}
	}
	meanD /= float64(total)
	mse := sumSq / float64(total*3)
	if mse == 0 {
		psnr = math.Inf(1)
	} else {
		psnr = 10 * math.Log10(255*255/mse)
	}
	return
}

// ── Utilitários ──────────────────────────────────────────────────────────────

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// xorshift64 — PRNG ultra-rápido sem dependências externas.
type xorshift struct{ state uint64 }

func newXorshift(seed uint64) *xorshift {
	if seed == 0 {
		seed = 0xdeadbeefcafe1337
	}
	return &xorshift{state: seed}
}

func (x *xorshift) next() uint64 {
	x.state ^= x.state << 13
	x.state ^= x.state >> 7
	x.state ^= x.state << 17
	return x.state
}

// gaussSample — aproximação Box-Muller truncada usando xorshift.
// Retorna valor no intervalo aproximado [-1, +1].
func gaussSample(rng *xorshift) float64 {
	// Soma de 4 uniformes normalizada → gaussiana truncada via TCL
	var s float64
	for i := 0; i < 4; i++ {
		s += float64(rng.next()%201) - 100 // [-100, 100]
	}
	return s / 400.0 // ∈ [-1, +1]
}
