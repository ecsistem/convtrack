package clone

import (
	"net"
	"net/url"
	"strings"
	"testing"
)

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("ip inválido: %s", s)
	}
	return ip
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return u
}

func TestResolveRef(t *testing.T) {
	base := mustURL(t, "https://oferta.com/promo/")
	cases := map[string]string{
		"../img/a.png":                 "https://oferta.com/img/a.png",
		"/css/app.css":                 "https://oferta.com/css/app.css",
		"https://cdn.x.com/lib.js":     "https://cdn.x.com/lib.js",
		"//cdn.x.com/p.css":            "https://cdn.x.com/p.css",
		"style.css?v=2#frag":           "https://oferta.com/promo/style.css?v=2",
	}
	for in, want := range cases {
		if got := resolveRef(base, in); got != want {
			t.Errorf("resolveRef(%q) = %q, want %q", in, got, want)
		}
	}
	if got := resolveRef(base, "javascript:void(0)"); got != "" {
		t.Errorf("expected empty for non-http scheme, got %q", got)
	}
}

func TestExtractAndRewriteHTML(t *testing.T) {
	htmlStr := `<html><head>
<link rel="stylesheet" href="/css/app.css">
<script src="https://cdn.x.com/lib.js"></script>
</head><body>
<img src="img/hero.png" srcset="img/hero.png 1x, img/hero@2x.png 2x">
<a href="/outra-pagina">link</a>
<div style="background:url('bg.jpg')"></div>
</body></html>`

	refs := extractHTMLRefs(htmlStr)
	for _, want := range []string{"/css/app.css", "https://cdn.x.com/lib.js", "img/hero.png", "img/hero@2x.png", "bg.jpg"} {
		if _, ok := refs[want]; !ok {
			t.Errorf("ref %q não extraída", want)
		}
	}
	// <a href> de página NÃO deve ser tratado como asset
	if _, ok := refs["/outra-pagina"]; ok {
		t.Errorf("link de página foi tratado como asset")
	}

	base := mustURL(t, "https://oferta.com/")
	rawToLocal := map[string]string{}
	for raw := range refs {
		if abs := resolveRef(base, raw); abs != "" {
			rawToLocal[raw] = localPath(abs)
		}
	}
	out := applyReplacements(htmlStr, rawToLocal)
	if strings.Contains(out, `href="/css/app.css"`) {
		t.Errorf("CSS não foi reescrito:\n%s", out)
	}
	if !strings.Contains(out, "assets/") {
		t.Errorf("nenhum caminho local no HTML reescrito")
	}
	if !strings.Contains(out, `href="/outra-pagina"`) {
		t.Errorf("link de página foi alterado indevidamente")
	}
}

func TestRewriteCSS(t *testing.T) {
	cssURL := mustURL(t, "https://oferta.com/css/app.css")
	fontAbs := resolveRef(cssURL, "../fonts/x.woff2")
	absToFile := map[string]string{fontAbs: "abc123.woff2"}

	css := `@font-face{src:url(../fonts/x.woff2)} .h{background:url("../img/no.png")}`
	out := rewriteCSS(css, cssURL, absToFile)
	if !strings.Contains(out, `url("abc123.woff2")`) {
		t.Errorf("fonte não reescrita: %s", out)
	}
	// asset não mapeado permanece como estava (resolvido)
	if !strings.Contains(out, "no.png") {
		t.Errorf("asset desconhecido sumiu: %s", out)
	}
}

func TestPageLocalPath(t *testing.T) {
	cases := map[string]string{
		"https://x.com/":            "index.html",
		"https://x.com":             "index.html",
		"https://x.com/promo":       "promo.html",
		"https://x.com/promo/":      "promo/index.html",
		"https://x.com/a/b/c":       "a/b/c.html",
		"https://x.com/page.html":   "page.html",
		"https://x.com/checkout.php": "checkout.php.html",
	}
	for in, want := range cases {
		if got := pageLocalPath(mustURL(t, in)); got != want {
			t.Errorf("pageLocalPath(%q) = %q, want %q", in, got, want)
		}
	}
	// query muda o nome do arquivo
	if got := pageLocalPath(mustURL(t, "https://x.com/p?id=2")); !strings.HasPrefix(got, "p_") || !strings.HasSuffix(got, ".html") {
		t.Errorf("query não refletida no nome: %q", got)
	}
}

func TestRelPath(t *testing.T) {
	cases := []struct{ dir, to, want string }{
		{".", "assets/x.png", "assets/x.png"},
		{"promo", "assets/x.png", "../assets/x.png"},
		{"a/b", "c/index.html", "../../c/index.html"},
	}
	for _, c := range cases {
		if got := relPath(c.dir, c.to); got != c.want {
			t.Errorf("relPath(%q,%q) = %q, want %q", c.dir, c.to, got, c.want)
		}
	}
}

func TestSSRFBlocks(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "169.254.1.1", "100.64.0.1", "::1"} {
		if !isBlockedIP(parseIP(t, ip)) {
			t.Errorf("IP %s deveria ser bloqueado", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1"} {
		if isBlockedIP(parseIP(t, ip)) {
			t.Errorf("IP público %s não deveria ser bloqueado", ip)
		}
	}
}
