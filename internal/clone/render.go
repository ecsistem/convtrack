package clone

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// renderer encapsula um navegador headless (Chromium via chromedp) reutilizado
// entre as páginas do crawl. Usado no modo "Renderizar JS" para clonar SPAs
// (sites client-side) cujo conteúdo só existe após a execução do JavaScript.
type renderer struct {
	allocCtx context.Context
	cancel   context.CancelFunc
}

// newRenderer cria o allocator do Chromium. Respeita CHROME_BIN se definido.
func newRenderer(parent context.Context, ua string) *renderer {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.UserAgent(ua),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("mute-audio", true),
	)
	if p := os.Getenv("CHROME_BIN"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	allocCtx, cancel := chromedp.NewExecAllocator(parent, opts...)
	return &renderer{allocCtx: allocCtx, cancel: cancel}
}

func (r *renderer) close() { r.cancel() }

type capturedResp struct {
	body        []byte
	contentType string
	url         *url.URL
}

// capturePage navega até rawURL, espera o JS renderizar e captura TODAS as
// respostas de rede (chunks, css, fontes, imagens, dados). Devolve o HTML final
// do DOM, a URL final e o mapa de recursos capturados (chave = urlKey).
func (r *renderer) capturePage(ctx context.Context, rawURL string) (string, *url.URL, map[string]capturedResp, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, nil, err
	}
	// Proteção SSRF: valida o IP do host antes de o Chrome navegar.
	if err := checkHostAllowed(ctx, u.Hostname()); err != nil {
		return "", nil, nil, err
	}

	tabCtx, cancel := chromedp.NewContext(r.allocCtx)
	defer cancel()
	tabCtx, tcancel := context.WithTimeout(tabCtx, 45*time.Second)
	defer tcancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	resp := map[string]capturedResp{}
	urlByReq := map[network.RequestID]string{}
	ctByReq := map[network.RequestID]string{}

	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		switch e := ev.(type) {
		case *network.EventResponseReceived:
			mu.Lock()
			urlByReq[e.RequestID] = e.Response.URL
			ctByReq[e.RequestID] = e.Response.MimeType
			mu.Unlock()
		case *network.EventLoadingFinished:
			wg.Add(1)
			go func(id network.RequestID) {
				defer wg.Done()
				mu.Lock()
				ru, ct := urlByReq[id], ctByReq[id]
				mu.Unlock()
				if ru == "" {
					return
				}
				pu, e := url.Parse(ru)
				if e != nil || pu.Host == "" {
					return
				}
				c := chromedp.FromContext(tabCtx)
				if c == nil || c.Target == nil {
					return
				}
				body, e := network.GetResponseBody(id).Do(cdp.WithExecutor(tabCtx, c.Target))
				if e != nil {
					return
				}
				mu.Lock()
				resp[urlKey(pu)] = capturedResp{body: body, contentType: ct, url: pu}
				mu.Unlock()
			}(e.RequestID)
		}
	})

	var htmlOut, finalURL string
	err = chromedp.Run(tabCtx,
		network.Enable(),
		chromedp.Navigate(rawURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2800*time.Millisecond), // deixa o JS hidratar e carregar chunks
		chromedp.OuterHTML("html", &htmlOut, chromedp.ByQuery),
		chromedp.Location(&finalURL),
	)
	if err != nil {
		return "", nil, nil, err
	}
	waitWG(&wg, 6*time.Second) // espera os GetResponseBody em voo

	fu, e := url.Parse(finalURL)
	if e != nil || fu.Host == "" {
		fu = u
	}
	mu.Lock()
	out := resp
	mu.Unlock()
	return htmlOut, fu, out, nil
}

const renderReadme = `Este clone foi gerado em modo "Renderizar JS" e PRESERVA os caminhos originais
do site (ex.: /_next/...), para que apps Next.js/React/SPA funcionem por completo.

>>> Abrir o index.html direto (file://) NÃO funciona. <<<

Sirva a pasta a partir da RAIZ, por exemplo:

  npx serve .
  # ou
  python3 -m http.server 8000

…ou faça upload da pasta em um host estático (Vercel, Netlify, Nginx, etc.).
`

// cloneRendered clona um site no modo render: renderiza cada página com o
// Chromium, captura toda a rede e monta um espelho fiel (sem reescrever os
// caminhos), pronto para ser servido a partir da raiz.
func cloneRendered(ctx context.Context, rend *renderer, entry *url.URL, opt Options) (*Result, error) {
	firstHTML, firstURL, firstResp, err := rend.capturePage(ctx, entry.String())
	if err != nil {
		return nil, fmt.Errorf("falha ao renderizar a página: %w", err)
	}
	canonHost := canonicalHost(firstURL.Host)

	var pages []page
	var files []cloneFile
	pageByLocal := map[string]bool{}
	assetByLocal := map[string]bool{}
	visited := map[string]bool{}
	var totalBytes int64

	process := func(docHTML string, docURL *url.URL, resp map[string]capturedResp) {
		docLocal := mirrorPagePath(docURL)
		body := docHTML
		if r, ok := resp[urlKey(docURL)]; ok && len(r.body) > 0 &&
			strings.Contains(strings.ToLower(r.contentType), "html") {
			body = string(r.body) // HTML original do servidor (hidrata melhor)
		}
		if !pageByLocal[docLocal] {
			pageByLocal[docLocal] = true
			pages = append(pages, page{url: docURL, local: docLocal, html: body})
		}
		for _, r := range resp {
			if canonicalHost(r.url.Host) != canonHost {
				continue // recursos cross-host ficam apontando para a origem
			}
			if urlKey(r.url) == urlKey(docURL) {
				continue // é o próprio documento
			}
			local := mirrorAssetPath(r.url)
			if assetByLocal[local] || pageByLocal[local] {
				continue
			}
			assetByLocal[local] = true
			files = append(files, cloneFile{local: local, data: r.body})
			totalBytes += int64(len(r.body))
		}
	}

	visited[urlKey(firstURL)] = true
	process(firstHTML, firstURL, firstResp)
	frontier := extractSameHostLinks(pages[0].html, firstURL, canonHost)

	for len(frontier) > 0 && len(pages) < opt.MaxPages {
		var u *url.URL
		for len(frontier) > 0 {
			cand := frontier[0]
			frontier = frontier[1:]
			if !visited[urlKey(cand)] {
				u = cand
				break
			}
		}
		if u == nil {
			break
		}
		visited[urlKey(u)] = true

		h, fu, resp, e := rend.capturePage(ctx, u.String())
		if e != nil || canonicalHost(fu.Host) != canonHost {
			continue
		}
		process(h, fu, resp)
		frontier = append(frontier, extractSameHostLinks(h, fu, canonHost)...)
	}

	// README com instruções de como servir
	files = append(files, cloneFile{local: "COMO-USAR.txt", data: []byte(renderReadme)})

	zipBytes, err := buildZip(pages, files)
	if err != nil {
		return nil, err
	}

	title := ""
	if m := reTitle.FindStringSubmatch(pages[0].html); m != nil {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}

	return &Result{
		Zip:        zipBytes,
		Title:      title,
		BaseURL:    firstURL.String(),
		PageCount:  len(pages),
		AssetCount: len(files),
		TotalBytes: totalBytes,
	}, nil
}

// waitWG espera o WaitGroup com timeout.
func waitWG(wg *sync.WaitGroup, timeout time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// checkHostAllowed resolve o host e bloqueia IPs internos/privados (SSRF).
func checkHostAllowed(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("host vazio")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if isBlockedIP(ip.IP) {
			return fmt.Errorf("endereço bloqueado: %s", ip.IP)
		}
	}
	return nil
}
