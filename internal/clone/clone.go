// Package clone implementa um clonador de sites (ofertas / landing pages):
// rastreia todas as páginas do mesmo domínio a partir de uma URL inicial, baixa
// o HTML de cada página junto com os assets referenciados (CSS, JS, imagens,
// fontes), reescreve os links internos e referências para caminhos locais e
// empacota tudo num ZIP pronto para uso offline.
//
// Inspirado no goclone (https://github.com/goclone-dev/goclone, licença MIT),
// porém reescrito do zero usando apenas a biblioteca padrão e com proteção
// contra SSRF (bloqueia hosts/IPs internos).
package clone

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options configura uma operação de clone.
type Options struct {
	URL       string        // URL inicial do site/oferta
	UserAgent string        // User-Agent usado nas requisições
	MaxPages  int           // limite de páginas HTML rastreadas (default 60)
	MaxAssets int           // limite de assets baixados (default 400)
	MaxBytes  int64         // limite total de bytes baixados (default 150 MB)
	Timeout   time.Duration // tempo máximo total da operação (default 120s)
	Render    bool          // renderiza JS via Chromium headless (clona SPAs)
}

// Result é o resultado de um clone.
type Result struct {
	Zip        []byte // arquivo .zip em memória
	Title      string // <title> da página inicial
	BaseURL    string // URL final da página inicial (após redirects)
	PageCount  int    // quantas páginas HTML foram clonadas
	AssetCount int    // quantos assets foram baixados
	TotalBytes int64  // total de bytes baixados
}

const (
	defaultUA        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	defaultMaxPages  = 60
	defaultMaxAssets = 400
	defaultMaxBytes  = 150 << 20 // 150 MB
	defaultTimeout   = 120 * time.Second
	maxConcurrency   = 12
)

// ── Regexes de extração ───────────────────────────────────────────────────────

var (
	reTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reBase    = regexp.MustCompile(`(?is)<base[^>]*\bhref\s*=\s*["']([^"']+)["']`)
	reLink    = regexp.MustCompile(`(?is)<link\b[^>]*?\bhref\s*=\s*["']([^"']+)["']`)
	reScript  = regexp.MustCompile(`(?is)<script\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)
	reImg     = regexp.MustCompile(`(?is)<img\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)
	reSource  = regexp.MustCompile(`(?is)<(?:source|video|audio)\b[^>]*?\bsrc\s*=\s*["']([^"']+)["']`)
	rePoster  = regexp.MustCompile(`(?is)\bposter\s*=\s*["']([^"']+)["']`)
	reSrcset  = regexp.MustCompile(`(?is)\bsrcset\s*=\s*["']([^"']+)["']`)
	reAnchor  = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*["']([^"']+)["']`)
	reCSSURL  = regexp.MustCompile(`(?is)url\(\s*["']?([^"')]+)["']?\s*\)`)
	reCSSImp  = regexp.MustCompile(`(?is)@import\s+["']([^"']+)["']`)
	reDataURI = regexp.MustCompile(`(?i)^data:`)
)

// extensões que NÃO são páginas HTML (não devem entrar na fila de crawl).
var nonPageExt = map[string]bool{
	".css": true, ".js": true, ".json": true, ".xml": true, ".rss": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".svg": true, ".ico": true, ".bmp": true, ".avif": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp4": true, ".webm": true, ".mp3": true, ".wav": true, ".ogg": true,
	".pdf": true, ".zip": true, ".rar": true, ".gz": true, ".doc": true,
	".docx": true, ".xls": true, ".xlsx": true, ".csv": true, ".txt": true,
}

type page struct {
	url   *url.URL
	local string // caminho local relativo, ex "index.html" ou "promo/index.html"
	html  string
}

// Clone rastreia e baixa o site inteiro e retorna um ZIP.
func Clone(ctx context.Context, opt Options) (*Result, error) {
	if opt.UserAgent == "" {
		opt.UserAgent = defaultUA
	}
	if opt.MaxPages <= 0 {
		opt.MaxPages = defaultMaxPages
	}
	if opt.MaxAssets <= 0 {
		opt.MaxAssets = defaultMaxAssets
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = defaultMaxBytes
	}
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}

	entry, err := normalizeURL(opt.URL)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	// Modo SPA: renderiza com Chromium headless e captura toda a rede,
	// preservando os caminhos originais (espelho pronto para servir).
	if opt.Render {
		rend := newRenderer(ctx, opt.UserAgent)
		defer rend.close()
		return cloneRendered(ctx, rend, entry, opt)
	}

	client := newSafeClient()

	// loadPage devolve o HTML de uma página (HTTP cru) + URL final.
	loadPage := func(raw string) (string, *url.URL, bool) {
		body, fu, ct, e := fetch(ctx, client, raw, opt.UserAgent)
		if e != nil || !isHTML(ct, fu) {
			return "", nil, false
		}
		return string(body), fu, true
	}
	pageConcurrency := maxConcurrency

	// ── 1. Baixa a página inicial (define o host canônico) ──────────
	firstHTML, firstURL, ok := loadPage(entry.String())
	if !ok {
		return nil, fmt.Errorf("falha ao baixar a página")
	}
	firstBody := []byte(firstHTML)
	canonHost := canonicalHost(firstURL.Host)

	// ── 2. Crawl BFS das páginas do mesmo host ──────────────────────
	pages := []page{}
	pageByKey := map[string]string{} // urlKey → local
	visited := map[string]bool{}

	firstKey := urlKey(firstURL)
	firstLocal := pageLocalPath(firstURL)
	visited[firstKey] = true
	pageByKey[firstKey] = firstLocal
	pages = append(pages, page{url: firstURL, local: firstLocal, html: string(firstBody)})

	frontier := extractSameHostLinks(string(firstBody), firstURL, canonHost)

	for len(frontier) > 0 && len(pages) < opt.MaxPages {
		// dedupe + filtra o que já foi visto / enfileira respeitando o limite
		var batch []*url.URL
		for _, u := range frontier {
			k := urlKey(u)
			if visited[k] {
				continue
			}
			visited[k] = true
			batch = append(batch, u)
			if len(pages)+len(batch) >= opt.MaxPages {
				break
			}
		}
		if len(batch) == 0 {
			break
		}

		// baixa o nível atual concorrentemente
		type fetched struct {
			u    *url.URL
			body string
		}
		results := make([]fetched, len(batch))
		var wg sync.WaitGroup
		sem := make(chan struct{}, pageConcurrency)
		for i, u := range batch {
			i, u := i, u
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				body, finalURL, ok := loadPage(u.String())
				if !ok {
					return
				}
				if canonicalHost(finalURL.Host) != canonHost {
					return
				}
				results[i] = fetched{u: finalURL, body: body}
			}()
		}
		wg.Wait()

		var nextFrontier []*url.URL
		for _, r := range results {
			if r.u == nil {
				continue
			}
			k := urlKey(r.u)
			if _, ok := pageByKey[k]; ok {
				continue
			}
			local := pageLocalPath(r.u)
			pageByKey[k] = local
			pages = append(pages, page{url: r.u, local: local, html: r.body})
			nextFrontier = append(nextFrontier, extractSameHostLinks(r.body, r.u, canonHost)...)
			if len(pages) >= opt.MaxPages {
				break
			}
		}
		frontier = nextFrontier
	}

	// ── 3. Coleta todos os assets de todas as páginas ───────────────
	absToLocal := map[string]string{} // asset abs → "assets/xxxx.ext"
	for _, p := range pages {
		for raw := range extractHTMLRefs(p.html) {
			abs := resolveRef(p.url, raw)
			if abs == "" {
				continue
			}
			if _, ok := absToLocal[abs]; !ok {
				absToLocal[abs] = localPath(abs)
			}
		}
	}

	// ── 4. Baixa os assets (com 1 nível de CSS aninhado) ────────────
	dl := newDownloader(ctx, client, opt)
	files := dl.run(absToLocal)

	absToFile := map[string]string{} // asset abs → nome do arquivo (sem "assets/")
	for abs, local := range absToLocal {
		absToFile[abs] = strings.TrimPrefix(local, "assets/")
	}
	for abs, local := range dl.extraLocal {
		absToFile[abs] = strings.TrimPrefix(local, "assets/")
	}

	// ── 5. Reescreve CSS (url() / @import) ──────────────────────────
	for i := range files {
		if files[i].isCSS {
			files[i].data = []byte(rewriteCSS(string(files[i].data), files[i].srcURL, absToFile))
		}
	}

	// ── 6. Reescreve cada página (assets + links internos) ──────────
	for i := range pages {
		pages[i].html = rewritePage(pages[i], absToLocal, pageByKey)
	}

	// ── 7. Empacota tudo num ZIP ────────────────────────────────────
	zipBytes, err := buildZip(pages, files)
	if err != nil {
		return nil, err
	}

	title := ""
	if m := reTitle.FindStringSubmatch(string(firstBody)); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}

	return &Result{
		Zip:        zipBytes,
		Title:      title,
		BaseURL:    firstURL.String(),
		PageCount:  len(pages),
		AssetCount: len(files),
		TotalBytes: dl.totalBytes,
	}, nil
}

// ── Extração de referências ───────────────────────────────────────────────────

func extractHTMLRefs(htmlStr string) map[string]struct{} {
	refs := map[string]struct{}{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "#") || reDataURI.MatchString(s) ||
			strings.HasPrefix(s, "javascript:") || strings.HasPrefix(s, "mailto:") ||
			strings.HasPrefix(s, "tel:") {
			return
		}
		refs[s] = struct{}{}
	}

	for _, re := range []*regexp.Regexp{reLink, reScript, reImg, reSource} {
		for _, m := range re.FindAllStringSubmatch(htmlStr, -1) {
			add(m[1])
		}
	}
	for _, m := range rePoster.FindAllStringSubmatch(htmlStr, -1) {
		add(m[1])
	}
	for _, m := range reSrcset.FindAllStringSubmatch(htmlStr, -1) {
		for _, part := range strings.Split(m[1], ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			if len(fields) > 0 {
				add(fields[0])
			}
		}
	}
	for _, m := range reCSSURL.FindAllStringSubmatch(htmlStr, -1) {
		add(m[1])
	}
	return refs
}

// extractSameHostLinks devolve os links <a href> que apontam para o mesmo host
// e que parecem ser páginas HTML (não assets/downloads).
func extractSameHostLinks(htmlStr string, base *url.URL, canonHost string) []*url.URL {
	var out []*url.URL
	seen := map[string]bool{}
	for _, m := range reAnchor.FindAllStringSubmatch(htmlStr, -1) {
		raw := strings.TrimSpace(m[1])
		if raw == "" || strings.HasPrefix(raw, "#") || reDataURI.MatchString(raw) ||
			strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "mailto:") ||
			strings.HasPrefix(raw, "tel:") {
			continue
		}
		ref, err := url.Parse(html.UnescapeString(raw))
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" {
			continue
		}
		if canonicalHost(abs.Host) != canonHost {
			continue
		}
		if nonPageExt[strings.ToLower(path.Ext(abs.Path))] {
			continue
		}
		abs.Fragment = ""
		k := urlKey(abs)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, abs)
	}
	return out
}

// ── Downloader concorrente de assets ──────────────────────────────────────────

type cloneFile struct {
	local  string
	data   []byte
	isCSS  bool
	srcURL *url.URL
}

type downloader struct {
	ctx    context.Context
	client *http.Client
	opt    Options

	mu         sync.Mutex
	files      []cloneFile
	extraLocal map[string]string
	totalBytes int64
	count      int
}

func newDownloader(ctx context.Context, client *http.Client, opt Options) *downloader {
	return &downloader{ctx: ctx, client: client, opt: opt, extraLocal: map[string]string{}}
}

func (d *downloader) run(absToLocal map[string]string) []cloneFile {
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)
	for abs, local := range absToLocal {
		abs, local := abs, local
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d.fetchAsset(abs, local)
		}()
	}
	wg.Wait()
	return d.files
}

func (d *downloader) fetchAsset(abs, local string) {
	d.mu.Lock()
	if d.count >= d.opt.MaxAssets || d.totalBytes >= d.opt.MaxBytes {
		d.mu.Unlock()
		return
	}
	d.count++
	d.mu.Unlock()

	body, finalURL, _, err := fetch(d.ctx, d.client, abs, d.opt.UserAgent)
	if err != nil {
		return
	}
	isCSS := strings.HasSuffix(strings.ToLower(local), ".css")

	d.mu.Lock()
	d.totalBytes += int64(len(body))
	over := d.totalBytes > d.opt.MaxBytes
	d.mu.Unlock()
	if over {
		return
	}

	if isCSS {
		d.fetchNestedCSS(string(body), finalURL)
	}

	d.mu.Lock()
	d.files = append(d.files, cloneFile{local: local, data: body, isCSS: isCSS, srcURL: finalURL})
	d.mu.Unlock()
}

func (d *downloader) fetchNestedCSS(css string, cssURL *url.URL) {
	nested := map[string]string{}
	collect := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || reDataURI.MatchString(raw) || strings.HasPrefix(raw, "#") {
			return
		}
		abs := resolveRef(cssURL, raw)
		if abs == "" {
			return
		}
		d.mu.Lock()
		_, seen := d.extraLocal[abs]
		d.mu.Unlock()
		if seen {
			return
		}
		nested[abs] = localPath(abs)
	}
	for _, m := range reCSSURL.FindAllStringSubmatch(css, -1) {
		collect(m[1])
	}
	for _, m := range reCSSImp.FindAllStringSubmatch(css, -1) {
		collect(m[1])
	}

	for abs, local := range nested {
		d.mu.Lock()
		if d.count >= d.opt.MaxAssets || d.totalBytes >= d.opt.MaxBytes {
			d.mu.Unlock()
			return
		}
		d.count++
		d.extraLocal[abs] = local
		d.mu.Unlock()

		body, finalURL, _, err := fetch(d.ctx, d.client, abs, d.opt.UserAgent)
		if err != nil {
			continue
		}
		d.mu.Lock()
		d.totalBytes += int64(len(body))
		d.files = append(d.files, cloneFile{local: local, data: body, srcURL: finalURL})
		d.mu.Unlock()
	}
}

// ── Reescrita ─────────────────────────────────────────────────────────────────

// rewritePage reescreve, numa página, as referências de assets (para a pasta
// assets/, relativa à profundidade da página) e os links internos <a href>
// (para o arquivo local da página de destino). Links externos viram absolutos.
func rewritePage(p page, assetAbs map[string]string, pageByKey map[string]string) string {
	dir := path.Dir(p.local)
	pairs := map[string]string{}

	// assets
	for raw := range extractHTMLRefs(p.html) {
		abs := resolveRef(p.url, raw)
		if local, ok := assetAbs[abs]; ok {
			pairs[raw] = relPath(dir, local)
		}
	}

	// links internos <a href>
	for _, m := range reAnchor.FindAllStringSubmatch(p.html, -1) {
		raw := strings.TrimSpace(m[1])
		if raw == "" || strings.HasPrefix(raw, "#") || reDataURI.MatchString(raw) ||
			strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "mailto:") ||
			strings.HasPrefix(raw, "tel:") {
			continue
		}
		abs := resolveRef(p.url, raw)
		if abs == "" {
			continue
		}
		if local, ok := pageByKey[abs]; ok {
			pairs[raw] = relPath(dir, local)
		} else if _, isAsset := pairs[raw]; !isAsset {
			// página não rastreada / externa → mantém clicável (absoluta)
			pairs[raw] = abs
		}
	}

	return applyReplacements(p.html, pairs)
}

func rewriteCSS(css string, cssURL *url.URL, absToFile map[string]string) string {
	replace := func(raw string) string {
		raw = strings.TrimSpace(raw)
		if raw == "" || reDataURI.MatchString(raw) {
			return raw
		}
		abs := resolveRef(cssURL, raw)
		if f, ok := absToFile[abs]; ok {
			return f
		}
		return raw
	}
	css = reCSSURL.ReplaceAllStringFunc(css, func(m string) string {
		sub := reCSSURL.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		return "url(\"" + replace(sub[1]) + "\")"
	})
	css = reCSSImp.ReplaceAllStringFunc(css, func(m string) string {
		sub := reCSSImp.FindStringSubmatch(m)
		if sub == nil {
			return m
		}
		return "@import \"" + replace(sub[1]) + "\""
	})
	return css
}

func applyReplacements(s string, pairs map[string]string) string {
	if len(pairs) == 0 {
		return s
	}
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	// substitui as referências mais longas primeiro (evita match parcial)
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	args := make([]string, 0, len(pairs)*2)
	for _, k := range keys {
		args = append(args, k, pairs[k])
	}
	return strings.NewReplacer(args...).Replace(s)
}

// ── ZIP ───────────────────────────────────────────────────────────────────────

func buildZip(pages []page, files []cloneFile) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	seen := map[string]bool{}

	write := func(name string, data []byte) error {
		if name == "" || seen[name] {
			return nil
		}
		seen[name] = true
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}

	for _, p := range pages {
		if err := write(p.local, []byte(p.html)); err != nil {
			return nil, err
		}
	}
	for _, f := range files {
		if err := write(f.local, f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── Helpers de URL / caminho ──────────────────────────────────────────────────

func normalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("url vazia")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("url inválida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("apenas http/https são suportados")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url sem host")
	}
	return u, nil
}

func resolveRef(base *url.URL, raw string) string {
	raw = html.UnescapeString(strings.TrimSpace(raw))
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(ref)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	abs.Fragment = ""
	return abs.String()
}

func canonicalHost(h string) string {
	h = strings.ToLower(h)
	h = strings.TrimPrefix(h, "www.")
	return h
}

// urlKey gera uma chave de deduplicação para uma página (sem fragmento).
func urlKey(u *url.URL) string {
	c := *u
	c.Fragment = ""
	return c.String()
}

// pageLocalPath mapeia a URL de uma página para um caminho de arquivo local,
// preservando a estrutura de diretórios do site.
func pageLocalPath(u *url.URL) string {
	name := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
	if name == "" || strings.HasSuffix(u.Path, "/") {
		if name == "" {
			name = "index.html"
		} else {
			name += "/index.html"
		}
	} else if ext := path.Ext(name); ext == "" {
		name += ".html"
	} else if ext != ".html" && ext != ".htm" {
		name += ".html"
	}
	if u.RawQuery != "" {
		h := shortHash(u.RawQuery)
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + "_" + h + ext
	}
	return name
}

// relPath devolve o caminho de `to` (relativo à raiz) a partir do diretório
// `fromDir` (relativo à raiz). Ambos usam "/" como separador.
func relPath(fromDir, to string) string {
	if fromDir == "." || fromDir == "" {
		return to
	}
	depth := strings.Count(fromDir, "/") + 1
	return strings.Repeat("../", depth) + to
}

// localPath devolve um caminho local determinístico para um asset.
func localPath(abs string) string {
	name := shortHash(abs)
	ext := ""
	if u, err := url.Parse(abs); err == nil {
		ext = strings.ToLower(path.Ext(u.Path))
	}
	if ext == "" || len(ext) > 6 {
		ext = ".bin"
	}
	return "assets/" + name + ext
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// mirrorAssetPath preserva o caminho original de um asset (ex.:
// "_next/static/chunks/x.js"), para o modo render/espelho.
func mirrorAssetPath(u *url.URL) string {
	name := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
	if name == "" {
		name = "index"
	}
	if u.RawQuery != "" {
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + "_" + shortHash(u.RawQuery) + ext
	}
	return name
}

// mirrorPagePath mapeia a URL de uma página para um arquivo, em estilo de
// diretório ("/promo" → "promo/index.html"), compatível com servidores
// estáticos servindo a partir da raiz.
func mirrorPagePath(u *url.URL) string {
	name := strings.TrimPrefix(path.Clean("/"+u.Path), "/")
	if name == "" || strings.HasSuffix(u.Path, "/") {
		if name == "" {
			name = "index.html"
		} else {
			name += "/index.html"
		}
	} else if ext := path.Ext(name); ext == "" {
		name += "/index.html"
	}
	if u.RawQuery != "" {
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + "_" + shortHash(u.RawQuery) + ext
	}
	return name
}

func isHTML(contentType string, u *url.URL) bool {
	if contentType != "" {
		ct := strings.ToLower(contentType)
		if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
			return true
		}
		// content-type explícito e não-HTML
		if strings.Contains(ct, "text/") || strings.Contains(ct, "application/") ||
			strings.Contains(ct, "image/") || strings.Contains(ct, "font/") {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(u.Path))
	return ext == "" || ext == ".html" || ext == ".htm"
}

// ── HTTP com proteção contra SSRF ─────────────────────────────────────────────

func newSafeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isBlockedIP(ip.IP) {
					return nil, fmt.Errorf("endereço bloqueado: %s", ip.IP)
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("excesso de redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect para esquema não suportado")
			}
			return nil
		},
	}
}

func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true // CGNAT 100.64.0.0/10
	}
	return false
}

func fetch(ctx context.Context, client *http.Client, rawURL, ua string) ([]byte, *url.URL, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, defaultMaxBytes))
	if err != nil {
		return nil, nil, "", err
	}
	return body, resp.Request.URL, resp.Header.Get("Content-Type"), nil
}
