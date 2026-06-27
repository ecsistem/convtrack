package handlers

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ecsistem/convtrack/internal/api/middleware"
	"github.com/ecsistem/convtrack/internal/plans"
	"github.com/ecsistem/convtrack/internal/shield"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── Slug cloaker (público — sem autenticação) ─────────────────────────────

// GET /:slug — entrada principal do cloaker por slug.
//
// Fluxo:
//  1. require_key → se ?_sk ausente/errado → Página White
//  2. Bot check → bots → Página White
//  3. challenge_mode:
//     - "redirect" (default): redireciona direto para Black/White
//     - "captcha":  serve CAPTCHA de clique; após confirmar → Página Black
//     - "both":     CAPTCHA → confirmar → loading page com fingerprint → Página Black
//     - "iframe":   carrega a Página Black de forma invisível via iframe/JS,
//                   com checagem extra de fingerprint
//
// pagebot: quando configurado (ex: "cloudflare"), todo envio à Página White
// passa primeiro por um interstitial que segura o tráfego até uma ação do
// usuário (clique) — finge ser uma checagem de segurança real.
func (h *ShieldHandler) SlugCloak(c *fiber.Ctx) error {
	slug := c.Params("slug")
	campaign, err := h.svc.ResolveCampaignBySlug(c.Context(), slug)
	if err != nil || campaign == nil {
		return c.Next() // slug não existe — deixa passar para outras rotas
	}

	ip := middleware.ClientIP(c)
	ua := c.Get("User-Agent")
	// URL completa da tentativa de acesso (path + query string completos)
	rawURL := string(c.Request().URI().PathOriginal()) + "?" + string(c.Request().URI().QueryString())

	// servePagebotOrRedirect entrega a Página White — direto, ou atrás do
	// interstitial Pagebot quando configurado na campanha.
	servePagebotOrRedirect := func(target string) error {
		if campaign.Pagebot == "cloudflare" {
			c.Set("Content-Type", "text/html; charset=utf-8")
			c.Set("Cache-Control", "no-store")
			return c.SendString(buildPagebotHTML(target))
		}
		return c.Redirect(target, fiber.StatusFound)
	}

	safeRedirect := func(reason string) error {
		safeURL := campaign.SafeURL
		if safeURL == "" {
			safeURL = "https://google.com"
		}
		h.svc.LogFiltered(campaign.ProjectID, ip, ua, reason, safeURL, rawURL)
		return servePagebotOrRedirect(safeURL)
	}

	// ── 0. Em análise — safe_url para todos ────────────────────────────────
	if campaign.UnderReview {
		return safeRedirect("under_review")
	}

	// ── 0b. Click ID obrigatório (revisor de anúncio abre sem o click pago) ──
	// Verifica TODAS as ocorrências do parâmetro (não só a primeira) e
	// rejeita macros de anúncio não resolvidos (ex: __CLICK_ID__) — sinal de
	// que a URL foi copiada crua de spy tool em vez de clicada de fato.
	// Spy tools costumam duplicar o parâmetro: o macro original intacto +
	// um valor forjado, esperando que só o primeiro seja checado.
	if campaign.RequireTtclid && !hasValidQueryValue(c, "ttclid") {
		return safeRedirect("no_ttclid")
	}
	if campaign.RequireClickID {
		params := campaign.ClickIDParams()
		if len(params) > 0 {
			hasClickID := false
			for _, param := range params {
				if hasValidQueryValue(c, param) {
					hasClickID = true
					break
				}
			}
			if !hasClickID {
				return safeRedirect("no_click_id")
			}
		}
	}

	// ── 0d. Origem real (valida o Referer HTTP) ────────────────────────────
	if campaign.OriginOnly {
		if !campaign.OriginMatches(c.Get("Referer")) {
			return safeRedirect("origin_blocked")
		}
	}

	// ── 1. Chave de acesso secreta ──────────────────────────────────────────
	if campaign.RequireKey && campaign.AccessKey != "" {
		if c.Query("_sk") != campaign.AccessKey {
			return safeRedirect("invalid_key")
		}
	}

	// ── 2. Bot check + fonte de tráfego ────────────────────────────────────
	result, _ := h.svc.Check(c.Context(), campaign.ProjectID, shield.CheckRequest{
		IP:        ip,
		UserAgent: ua,
		Referer:   c.Get("Referer"),
		UTMSource: c.Query("utm_source"),
		RawURL:    rawURL,
	})

	isBot := result != nil && !result.Allowed

	// Bots sempre vão para a Página White — sem CAPTCHA
	if isBot {
		safeURL := campaign.SafeURL
		if result != nil && result.RedirectURL != "" {
			safeURL = result.RedirectURL
		}
		if safeURL == "" {
			safeURL = "https://google.com"
		}
		return servePagebotOrRedirect(safeURL)
	}

	// ── 3. Humano: challenge_mode ───────────────────────────────────────────
	moneyURL, _ := shield.ChooseURL(campaign, ip)
	if moneyURL == "" {
		return c.Status(fiber.StatusNotFound).SendString("campaign not configured")
	}

	// Repassa query strings (UTMs etc.) sem _sk para a URL destino
	qs := buildQS(c, "_sk") // remove _sk antes de repassar
	if qs != "" {
		if strings.Contains(moneyURL, "?") {
			moneyURL += "&" + qs
		} else {
			moneyURL += "?" + qs
		}
	}

	mode := campaign.ChallengeMode
	if mode == "" {
		mode = "redirect"
	}

	switch mode {
	case "captcha", "both":
		// Verifica se já passou pelo CAPTCHA (cookie de sessão)
		cookieName := "ct_cp_" + campaign.ID.String()[:8]
		cookieVal := c.Cookies(cookieName)
		if cookieVal == campaign.ID.String()[:16] {
			// Já resolveu — vai direto para a Página Black
			return c.Redirect(moneyURL, fiber.StatusFound)
		}
		// Serve CAPTCHA de clique
		ch := shield.GenerateCaptcha(campaign.ID.String(), campaign.AccessKey)
		html := buildCaptchaHTML(slug, ch.Token, campaign.Name, moneyURL, mode)
		c.Set("Content-Type", "text/html; charset=utf-8")
		c.Set("Cache-Control", "no-store")
		return c.SendString(html)

	case "iframe":
		// Carrega a Página Black de forma invisível via iframe, com checagem
		// extra de fingerprint embutida na página — útil contra revisores que
		// olham só o HTML/network superficialmente.
		html := buildIframeHTML(moneyURL, h.svc.APIBase, campaign.ProjectID.String(), campaign.ID.String())
		c.Set("Content-Type", "text/html; charset=utf-8")
		c.Set("Cache-Control", "no-store")
		return c.SendString(html)

	default: // "redirect" — comportamento atual
		return c.Redirect(moneyURL, fiber.StatusFound)
	}
}

// POST /:slug/verify — verifica a resposta do CAPTCHA.
func (h *ShieldHandler) VerifyCaptcha(c *fiber.Ctx) error {
	slug := c.Params("slug")
	campaign, err := h.svc.ResolveCampaignBySlug(c.Context(), slug)
	if err != nil || campaign == nil {
		return c.Status(fiber.StatusNotFound).SendString("not found")
	}

	token  := c.FormValue("token")
	answer := c.FormValue("answer")
	next   := c.FormValue("next")   // money_url pré-computada
	mode   := c.FormValue("mode")   // captcha | both

	if !shield.VerifyCaptcha(token, answer, campaign.ID.String(), campaign.AccessKey) {
		// Token inválido/expirado/clique instantâneo → novo desafio com erro
		ch := shield.GenerateCaptcha(campaign.ID.String(), campaign.AccessKey)
		html := buildCaptchaHTML(slug, ch.Token, campaign.Name, next, mode)
		html = strings.Replace(html, `id="captcha-error" class="captcha-error hidden"`,
			`id="captcha-error" class="captcha-error"`, 1)
		c.Set("Content-Type", "text/html; charset=utf-8")
		c.Set("Cache-Control", "no-store")
		return c.SendString(html)
	}

	// ✅ Correto — seta cookie (1h) para evitar re-verificação
	cookieName := "ct_cp_" + campaign.ID.String()[:8]
	c.Cookie(&fiber.Cookie{
		Name:     cookieName,
		Value:    campaign.ID.String()[:16],
		MaxAge:   3600,
		HTTPOnly: true,
		SameSite: "Lax",
	})

	// Para mode=both, mostra loading spinner antes do money URL
	if mode == "both" {
		html := buildLoadingHTML(next, campaign.Name)
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(html)
	}

	return c.Redirect(next, fiber.StatusFound)
}

// ── HTML builders ──────────────────────────────────────────────────────────

// buildQS retorna a query string atual excluindo os parâmetros em exclude.
func buildQS(c *fiber.Ctx, exclude ...string) string {
	args := c.Request().URI().QueryArgs()
	var parts []string
	args.VisitAll(func(k, v []byte) {
		key := string(k)
		for _, ex := range exclude {
			if key == ex {
				return
			}
		}
		parts = append(parts, key+"="+string(v))
	})
	return strings.Join(parts, "&")
}

// hasValidQueryValue verifica se ALGUMA ocorrência do parâmetro na query
// string é não-vazia e não é um macro de anúncio não resolvido. Necessário
// porque c.Query() retorna só a primeira ocorrência — spy tools duplicam o
// parâmetro (macro original + valor forjado) para furar checagem ingênua.
func hasValidQueryValue(c *fiber.Ctx, param string) bool {
	values := c.Request().URI().QueryArgs().PeekMulti(param)
	for _, v := range values {
		if !shield.IsUnresolvedMacro(string(v)) {
			return true
		}
	}
	return false
}

// buildCaptchaHTML gera a página do CAPTCHA de clique (sem pergunta/conta).
// O usuário só precisa clicar — o servidor valida token + delay mínimo
// (VerifyCaptcha) para filtrar replays automatizados que já chegam com token.
func buildCaptchaHTML(slug, token, campaignName, moneyURL, mode string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Verificação de Segurança</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%%;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif}
body{
  background:#09090b;
  display:flex;align-items:center;justify-content:center;min-height:100%%;
  background-image:radial-gradient(ellipse at 50%% 0%%,rgba(99,102,241,.12) 0%%,transparent 60%%);
}
.card{
  width:100%%;max-width:420px;margin:24px;
  background:#18181b;border:1px solid rgba(255,255,255,.08);border-radius:20px;
  padding:40px 36px;
  box-shadow:0 24px 48px rgba(0,0,0,.4),0 0 0 1px rgba(255,255,255,.04) inset;
}
.shield{
  width:52px;height:52px;border-radius:14px;
  background:linear-gradient(135deg,#4f46e5,#7c3aed);
  display:flex;align-items:center;justify-content:center;
  font-size:24px;margin:0 auto 24px;
  box-shadow:0 8px 24px rgba(79,70,229,.35);
}
h1{font-size:18px;font-weight:600;color:#f4f4f5;text-align:center;margin-bottom:6px}
.sub{font-size:13px;color:#71717a;text-align:center;margin-bottom:32px;line-height:1.5}
.challenge-box{
  background:#09090b;border:1px solid rgba(255,255,255,.08);border-radius:12px;
  padding:24px;text-align:center;margin-bottom:24px;
}
.check-row{
  display:flex;align-items:center;gap:12px;
  background:#18181b;border:1.5px solid rgba(255,255,255,.1);border-radius:10px;
  padding:16px;cursor:pointer;transition:border .15s,background .15s;
}
.check-row:hover{border-color:#6366f1;background:#1c1c20}
.checkbox{
  width:22px;height:22px;border-radius:6px;flex-shrink:0;
  border:1.5px solid rgba(255,255,255,.2);background:#27272a;
}
.check-row span{font-size:14px;color:#d4d4d8;font-weight:500;text-align:left}
button{
  width:100%%;height:48px;border:none;border-radius:10px;cursor:pointer;
  background:linear-gradient(135deg,#4f46e5,#7c3aed);
  color:#fff;font-size:15px;font-weight:600;letter-spacing:.2px;
  transition:opacity .15s,transform .1s;margin-top:16px;
}
button:hover{opacity:.9}
button:active{transform:scale(.98)}
button:disabled{opacity:.5;cursor:not-allowed}
.captcha-error{
  background:rgba(239,68,68,.1);border:1px solid rgba(239,68,68,.25);
  border-radius:8px;padding:10px 14px;text-align:center;
  color:#f87171;font-size:13px;margin-bottom:16px;
}
.captcha-error.hidden{display:none}
.footer{margin-top:24px;text-align:center;font-size:11px;color:#3f3f46}
.footer span{color:#52525b}
</style>
</head>
<body>
<div class="card">
  <div class="shield">🛡️</div>
  <h1>Verificação de Segurança</h1>
  <p class="sub">Confirme que você é humano para continuar.</p>

  <p id="captcha-error" class="captcha-error hidden">Verificação falhou. Tente novamente.</p>

  <form id="cf" method="POST" action="/%s/verify" autocomplete="off">
    <input type="hidden" name="token" value="%s">
    <input type="hidden" name="next"  value="%s">
    <input type="hidden" name="mode"  value="%s">
    <input type="hidden" name="answer" value="1">
    <div class="challenge-box">
      <div class="check-row" id="check-row" role="checkbox" aria-checked="false" tabindex="0">
        <input type="checkbox" id="human-check" name="checked" required style="position:absolute;opacity:0;pointer-events:none">
        <span class="checkbox" id="checkbox-visual"></span>
        <span>Não sou um robô</span>
      </div>
      <button id="submit-btn" type="submit" disabled>Continuar →</button>
    </div>
  </form>

  <p class="footer">Protegido por <span>ConvTrack Shield</span></p>
</div>
<script>
// Sem <label for=...> — evita o navegador disparar um clique nativo extra no
// input (que somado a este handler alternava o estado duas vezes por clique
// e cancelava a marcação visualmente).
function toggleHumanCheck(){
  var cb = document.getElementById('human-check');
  cb.checked = !cb.checked;
  document.getElementById('checkbox-visual').style.background = cb.checked ? '#6366f1' : '#27272a';
  document.getElementById('check-row').setAttribute('aria-checked', String(cb.checked));
  document.getElementById('submit-btn').disabled = !cb.checked;
}
document.getElementById('check-row').addEventListener('click', toggleHumanCheck);
document.getElementById('check-row').addEventListener('keydown', function(e){
  if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); toggleHumanCheck(); }
});
</script>
</body>
</html>`, slug, token, moneyURL, mode)
}

// buildIframeHTML gera a página do modo "iframe" — carrega a Página Black de
// forma invisível dentro de um iframe após o script de fingerprinting rodar,
// reforçando a detecção contra bots avançados que só inspecionam o HTML
// inicial ou o tráfego de rede superficialmente.
func buildIframeHTML(moneyURL, apiBase, projectID, campaignID string) string {
	fpScript := ""
	if apiBase != "" {
		fpScript = fmt.Sprintf(`<script src="%s/shield-fp.js?k=%s&c=%s" defer></script>`, apiBase, projectID, campaignID)
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Carregando...</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%%;overflow:hidden;background:#fff}
iframe{position:fixed;inset:0;width:100%%;height:100%%;border:none}
</style>
%s
</head>
<body>
<script>
(function(){
  // Validação adicional de fingerprint (shield-fp.js) já disparou no carregamento
  // desta página. Pequeno atraso simula o tempo de uma checagem real e evita que
  // o iframe apareça antes do script de fingerprint terminar de coletar sinais.
  setTimeout(function(){
    var f = document.createElement('iframe');
    f.src = %q;
    document.body.appendChild(f);
  }, 250);
})();
</script>
</body>
</html>`, fpScript, moneyURL)
}

// buildPagebotHTML gera o interstitial "Pagebot" (estilo Cloudflare) exibido
// antes da Página White. Segura o tráfego até o usuário clicar — a maioria
// dos scrapers/scanners automatizados não interage e nunca chega ao destino.
func buildPagebotHTML(target string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Verificando seu navegador...</title>
<style>
*,*::before,*::after{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%%;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif}
body{
  background:#0e1014;color:#cdd3da;
  display:flex;align-items:center;justify-content:center;min-height:100%%;
}
.card{width:100%%;max-width:460px;margin:24px;text-align:center}
.spinner{
  width:36px;height:36px;border-radius:50%%;margin:0 auto 24px;
  border:3px solid rgba(255,255,255,.08);border-top-color:#f6821f;
  animation:spin .8s linear infinite;
}
@keyframes spin{to{transform:rotate(360deg)}}
h1{font-size:16px;font-weight:500;color:#e4e7eb;margin-bottom:8px}
.sub{font-size:13px;color:#7c8591;line-height:1.6;margin-bottom:28px}
.btn-wrap{display:none}
.btn-wrap.show{display:block}
button{
  border:none;border-radius:6px;cursor:pointer;height:42px;padding:0 28px;
  background:#f6821f;color:#fff;font-size:14px;font-weight:600;
  transition:opacity .15s;
}
button:hover{opacity:.9}
.footer{margin-top:32px;font-size:11px;color:#4a5057}
</style>
</head>
<body>
<div class="card">
  <div id="spinner" class="spinner"></div>
  <h1 id="status-text">Verificando seu navegador antes de acessar o site.</h1>
  <p class="sub">Este processo é automático. Seu navegador será redirecionado em breve.</p>
  <div id="btn-wrap" class="btn-wrap">
    <button id="continue-btn">Continuar →</button>
  </div>
  <p class="footer">Performance &amp; security by Pagebot</p>
</div>
<script>
setTimeout(function(){
  document.getElementById('spinner').style.display='none';
  document.getElementById('status-text').textContent='Verificação concluída.';
  document.getElementById('btn-wrap').className='btn-wrap show';
}, 1800);
document.getElementById('continue-btn').addEventListener('click', function(){
  window.location.replace(%q);
});
</script>
</body>
</html>`, target)
}

// buildLoadingHTML gera a página de loading para o modo "both" (pós-CAPTCHA).
func buildLoadingHTML(moneyURL, campaignName string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Redirecionando...</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{
  background:#09090b;display:flex;flex-direction:column;
  align-items:center;justify-content:center;min-height:100vh;gap:20px;
  font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;
  background-image:radial-gradient(ellipse at 50%% 0%%,rgba(99,102,241,.1) 0%%,transparent 60%%);
}
.spinner{
  width:44px;height:44px;border-radius:50%%;
  border:3px solid rgba(255,255,255,.06);
  border-top-color:#6366f1;
  animation:spin .75s linear infinite;
}
@keyframes spin{to{transform:rotate(360deg)}}
p{color:#52525b;font-size:14px}
</style>
</head>
<body>
<div class="spinner"></div>
<p>Redirecionando...</p>
<script>
setTimeout(function(){window.location.replace(%q);},800);
</script>
</body>
</html>`, moneyURL)
}

// ── Campanhas ─────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/campaigns
func (h *ShieldHandler) ListCampaigns(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.ListCampaigns(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/shield/campaigns
func (h *ShieldHandler) CreateCampaign(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	// Plan limit check
	accountID, _ := middleware.GetAccountID(c)
	lim, plan := h.accountLimits(c, accountID)
	if lim.MaxCampaigns >= 0 {
		var count int
		_ = h.db.QueryRow(c.Context(),
			`SELECT COUNT(*) FROM shield_campaigns WHERE project_id = $1`, p.ID,
		).Scan(&count)
		if count >= lim.MaxCampaigns {
			return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
				"error": fmt.Sprintf("limite de %d campanhas atingido no plano %s — faça upgrade", lim.MaxCampaigns, plan),
				"limit": lim.MaxCampaigns,
				"plan":  plan,
			})
		}
	}

	var cam shield.Campaign
	if err := c.BodyParser(&cam); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	cam.ProjectID = p.ID
	created, err := h.svc.CreateCampaign(c.Context(), &cam)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// PUT /v1/dashboard/shield/campaigns/:id
func (h *ShieldHandler) UpdateCampaign(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	var cam shield.Campaign
	if err := c.BodyParser(&cam); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	cam.ID = id
	cam.ProjectID = p.ID
	if err := h.svc.UpdateCampaign(c.Context(), &cam); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	h.svc.InvalidateSlugCache(c.Context(), cam.Slug)
	return c.JSON(fiber.Map{"ok": true})
}

// DELETE /v1/dashboard/shield/campaigns/:id
func (h *ShieldHandler) DeleteCampaign(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	// busca o slug antes de deletar para invalidar o cache
	campaigns, _ := h.svc.ListCampaigns(c.Context(), p.ID)
	for _, cam := range campaigns {
		if cam.ID == id {
			h.svc.InvalidateSlugCache(c.Context(), cam.Slug)
			break
		}
	}
	if err := h.svc.DeleteCampaign(c.Context(), id, p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ── Domínios ──────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/domains
func (h *ShieldHandler) ListDomains(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.ListDomains(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/shield/domains
func (h *ShieldHandler) CreateDomain(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}

	// Plan limit check
	accountID, _ := middleware.GetAccountID(c)
	lim, plan := h.accountLimits(c, accountID)
	if lim.MaxDomains == 0 {
		return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
			"error": fmt.Sprintf("domínios personalizados não disponíveis no plano %s — faça upgrade", plan),
			"plan":  plan,
		})
	}
	if lim.MaxDomains > 0 {
		var count int
		_ = h.db.QueryRow(c.Context(),
			`SELECT COUNT(*) FROM shield_domains sd
			 JOIN shield_campaigns sc ON sc.id = sd.campaign_id
			 JOIN projects pr ON pr.id = sc.project_id
			 WHERE pr.account_id = $1`, accountID,
		).Scan(&count)
		if count >= lim.MaxDomains {
			return c.Status(fiber.StatusPaymentRequired).JSON(fiber.Map{
				"error": fmt.Sprintf("limite de %d domínio(s) atingido no plano %s — faça upgrade", lim.MaxDomains, plan),
				"limit": lim.MaxDomains,
				"plan":  plan,
			})
		}
	}

	var d shield.Domain
	if err := c.BodyParser(&d); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	d.ProjectID = p.ID
	if d.CampaignID == uuid.Nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "campaign_id obrigatório"})
	}
	created, err := h.svc.CreateDomain(c.Context(), &d)
	if err == shield.ErrDomainOwnedByOther {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": err.Error()})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// DELETE /v1/dashboard/shield/domains/:id
func (h *ShieldHandler) DeleteDomain(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.DeleteDomain(c.Context(), id, p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ── DNS + SSL checker ────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/domains/:id/check
//
// Verifica em tempo real:
//  1. CNAME — o domínio resolve para o hostname esperado desta API?
//  2. SSL   — se ssl_enabled=true, o cert TLS está válido e acessível?
//
// Retorna JSON com o resultado de cada verificação e detalhes para debug.
func (h *ShieldHandler) CheckDomain(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	// Busca o domínio no banco
	domains, err := h.svc.ListDomains(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	var domain *shield.Domain
	for i := range domains {
		if domains[i].ID == id {
			domain = &domains[i]
			break
		}
	}
	if domain == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "domain not found"})
	}

	// ── 1. Verificação CNAME ──────────────────────────────────────────────────
	// Esperamos que o CNAME do cliente aponte para o hostname desta API.
	expectedHost := ""
	if h.svc.APIBase != "" {
		if u, err2 := parseHost(h.svc.APIBase); err2 == nil {
			expectedHost = u
		}
	}

	cnameOK := false
	cnameTarget := ""
	cnameMsg := ""

	resolved, err := net.LookupCNAME(domain.Domain)
	if err != nil {
		cnameMsg = "Não foi possível resolver o CNAME: " + simplifyDNSErr(err)
	} else {
		// LookupCNAME retorna com ponto final (RFC) — normaliza
		cnameTarget = strings.TrimSuffix(resolved, ".")
		if expectedHost != "" {
			cnameOK = strings.EqualFold(cnameTarget, expectedHost)
			if !cnameOK {
				cnameMsg = "CNAME aponta para " + cnameTarget + ", esperado " + expectedHost
			}
		} else {
			// APIBase não configurado — só verifica se CNAME existe (não é o próprio domínio)
			cnameOK = !strings.EqualFold(strings.TrimSuffix(cnameTarget, "."), domain.Domain)
			if !cnameOK {
				cnameMsg = "CNAME não configurado (resolve para o próprio domínio)"
			}
		}
	}

	// ── 2. Verificação SSL ────────────────────────────────────────────────────
	sslOK := false
	sslMsg := ""
	sslExpiry := ""

	if domain.SSLEnabled {
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		conn, tlsErr := tls.DialWithDialer(dialer, "tcp", domain.Domain+":443", &tls.Config{
			ServerName: domain.Domain,
		})
		if tlsErr != nil {
			sslMsg = "TLS falhou: " + simplifyTLSErr(tlsErr)
		} else {
			state := conn.ConnectionState()
			_ = conn.Close()
			if len(state.PeerCertificates) > 0 {
				cert := state.PeerCertificates[0]
				sslExpiry = cert.NotAfter.Format("02/01/2006")
				if time.Now().Before(cert.NotAfter) {
					sslOK = true
				} else {
					sslMsg = "Certificado expirado em " + sslExpiry
				}
			} else {
				sslOK = true
			}
		}
	}

	return c.JSON(fiber.Map{
		"domain":       domain.Domain,
		"cname_ok":     cnameOK,
		"cname_target": cnameTarget,
		"cname_msg":    cnameMsg,
		"ssl_enabled":  domain.SSLEnabled,
		"ssl_ok":       sslOK,
		"ssl_msg":      sslMsg,
		"ssl_expiry":   sslExpiry,
		"expected_host": expectedHost,
	})
}

// parseHost extrai o hostname de uma URL (sem porta, sem scheme).
func parseHost(rawURL string) (string, error) {
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	host := rawURL
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if idx := strings.IndexByte(host, '/'); idx != -1 {
		host = host[:idx]
	}
	if idx := strings.LastIndexByte(host, ':'); idx != -1 {
		host = host[:idx]
	}
	if host == "" {
		return "", fmt.Errorf("empty host")
	}
	return host, nil
}

// simplifyDNSErr reduz mensagens de erro DNS para algo legível.
func simplifyDNSErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "no such host") {
		return "domínio não existe no DNS"
	}
	if strings.Contains(s, "timeout") {
		return "timeout ao consultar DNS"
	}
	return s
}

// simplifyTLSErr reduz mensagens TLS para algo legível.
func simplifyTLSErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "connection refused") {
		return "porta 443 recusada (cert ainda não emitido?)"
	}
	if strings.Contains(s, "timeout") {
		return "timeout — domínio ainda não aponta para este servidor"
	}
	if strings.Contains(s, "certificate") {
		return "certificado inválido"
	}
	return s
}

// ── Caddy On-Demand TLS — ask endpoint ──────────────────────────────────────

// GET /v1/shield/domain-ask?domain=oferta.seusite.com
//
// Caddy chama este endpoint antes de emitir qualquer certificado TLS sob demanda.
// Retorna 200 se o domínio está cadastrado com ssl_enabled=true, 403 caso contrário.
// Isso impede que Caddy emita certs para domínios desconhecidos (ACME abuse prevention).
//
// Segurança: o endpoint não expõe informações — apenas aceita ou rejeita.
// Em produção, o Caddy acessa via rede interna Docker (não exposto publicamente).
func (h *ShieldHandler) DomainAsk(c *fiber.Ctx) error {
	domain := c.Query("domain")
	if domain == "" {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	// Rejeita IPs diretamente (Caddy nunca deve perguntar por IPs)
	if net.ParseIP(domain) != nil {
		return c.SendStatus(fiber.StatusForbidden)
	}
	if h.svc.IsDomainSSLEnabled(c.Context(), domain) {
		return c.SendStatus(fiber.StatusOK)
	}
	return c.SendStatus(fiber.StatusForbidden)
}

// ── Webhooks ──────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/webhooks
func (h *ShieldHandler) ListWebhooks(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	list, err := h.svc.ListWebhooks(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": list})
}

// POST /v1/dashboard/shield/webhooks
func (h *ShieldHandler) CreateWebhook(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var w shield.WebhookConfig
	if err := c.BodyParser(&w); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	w.ProjectID = p.ID
	created, err := h.svc.CreateWebhook(c.Context(), &w)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusCreated).JSON(created)
}

// DELETE /v1/dashboard/shield/webhooks/:id
func (h *ShieldHandler) DeleteWebhook(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.DeleteWebhook(c.Context(), id, p.ID); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// POST /v1/dashboard/shield/webhooks/:id/test
// Dispara uma notificação de teste para o webhook especificado.
func (h *ShieldHandler) TestWebhook(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid id"})
	}

	// Carrega todos os webhooks do projeto e localiza o alvo
	whs, err := h.svc.ListWebhooks(c.Context(), p.ID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	var target *shield.WebhookConfig
	for i, w := range whs {
		if w.ID == id {
			target = &whs[i]
			break
		}
	}
	if target == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "webhook não encontrado"})
	}

	// Dispara direto para este webhook (ignora enabled/events — é um teste)
	testPayload := map[string]interface{}{
		"ip":         "192.0.2.1",
		"reason":     "test",
		"action":     "test",
		"device":     "desktop",
		"user_agent": "ConvTrack Shield Test/1.0",
		"score":      0.97,
		"signals":    []string{"webdriver", "headless"},
		"note":       "Esta é uma notificação de teste do ConvTrack Shield",
	}

	if err := h.svc.FireSingleWebhook(c.Context(), *target, shield.EventBotDetected, testPayload); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": "falha ao enviar: " + err.Error(),
		})
	}

	return c.JSON(fiber.Map{"ok": true, "webhook": target.Name, "type": target.Type})
}

// ── Fingerprint (público — chamado pelo shield-fp.js) ─────────────────────

// POST /v1/shield/fingerprint
func (h *ShieldHandler) Fingerprint(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	var fp shield.FingerprintData
	if err := c.BodyParser(&fp); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}

	ip := middleware.ClientIP(c)
	ua := c.Get("User-Agent")

	result, err := h.svc.ProcessFingerprint(c.Context(), p.ID, ip, ua, &fp)
	if err != nil {
		return c.JSON(&shield.FingerprintResult{Allowed: true, Action: "money"})
	}

	// Se existe uma campanha associada, escolhe URL com A/B split
	if result.Allowed && fp.CampaignID != "" {
		if camID, err := uuid.Parse(fp.CampaignID); err == nil {
			campaigns, _ := h.svc.ListCampaigns(c.Context(), p.ID)
			for _, cam := range campaigns {
				if cam.ID == camID {
					url, _ := shield.ChooseURL(&cam, ip)
					result.RedirectURL = url
					result.CampaignID = fp.CampaignID
					break
				}
			}
		}
	}

	return c.JSON(result)
}

// GET /shield-fp.js — serve o script de fingerprinting (sem autenticação)
func (h *ShieldHandler) ServeFPScript(c *fiber.Ctx) error {
	c.Set("Content-Type", "application/javascript; charset=utf-8")
	c.Set("Cache-Control", "public, max-age=3600")
	return c.SendFile("./public/shield-fp.js")
}

// ── Smart Redirect com fingerprinting ────────────────────────────────────

// GET /r/:projectKey — redirect tático (server-side, sem JS)
// Serve uma página de loading que fingerprinta e redireciona.
func (h *ShieldHandler) SmartRedirectAdvanced(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusNotFound).SendString("not found")
	}

	ua := c.Get("User-Agent")
	ip := middleware.ClientIP(c)

	// Verificação rápida server-side para bots óbvios.
	// SkipVisit=true porque humanos passam para ProcessFingerprint (que registra a visita completa).
	// Bots bloqueados aqui já têm a visita registrada via block() → insertVisit.
	result, _ := h.svc.Check(c.Context(), p.ID, shield.CheckRequest{
		IP:        ip,
		UserAgent: ua,
		SkipVisit: true,
		Referer:   c.Get("Referer"),
		UTMSource: c.Query("utm_source"),
	})

	if result != nil && !result.Allowed {
		if result.RedirectURL != "" {
			return c.Redirect(result.RedirectURL, fiber.StatusFound)
		}
		return c.Status(200).SendString("")
	}

	// Humano (ou incerto) → serve página de fingerprinting.
	// FALLBACK também sai do pool de rotação (usado se o fingerprint falhar).
	cfg, _ := h.svc.GetConfig(c.Context(), p.ID)
	fallbackURL := h.svc.RotationURL(cfg)

	apiBase := h.svc.APIBase
	apiKey := c.Params("projectKey")

	// Página HTML que coleta fingerprint e redireciona
	html := buildFPRedirectPage(apiBase, apiKey, fallbackURL)
	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
}

// buildFPRedirectPage gera a página HTML de loading + fingerprinting.
func buildFPRedirectPage(apiBase, apiKey, fallbackURL string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Redirecionando...</title>
<style>
body{margin:0;background:#0a0a0b;display:flex;align-items:center;justify-content:center;min-height:100vh;}
.spinner{width:32px;height:32px;border:3px solid #1e1b4b;border-top-color:#4f46e5;border-radius:50%;animation:spin .8s linear infinite;}
@keyframes spin{to{transform:rotate(360deg)}}
</style>
</head><body>
<div class="spinner"></div>
<script>
(function(){
  var API_BASE = '` + apiBase + `';
  var API_KEY  = '` + apiKey + `';
  var FALLBACK = '` + fallbackURL + `';

  function go(url){ if(url) window.location.replace(url); }

  fetch(API_BASE + '/v1/shield/fingerprint', {
    method: 'POST',
    headers: {'Content-Type':'application/json','X-API-Key':API_KEY},
    body: JSON.stringify({
      api_key: API_KEY,
      webdriver: !!navigator.webdriver,
      headless_hint: /HeadlessChrome|Puppeteer|Playwright|Selenium/i.test(navigator.userAgent),
      screen_width: screen.width, screen_height: screen.height,
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      language: navigator.language, platform: navigator.platform,
      cpu_cores: navigator.hardwareConcurrency||0,
      memory_gb: navigator.deviceMemory||0,
      touch_points: navigator.maxTouchPoints||0,
      plugins: navigator.plugins ? navigator.plugins.length : 0
    })
  })
  .then(function(r){ return r.json(); })
  .then(function(res){
    if(res.redirect_url){ go(res.redirect_url); }
    else if(FALLBACK){ go(FALLBACK); }
  })
  .catch(function(){ if(FALLBACK) go(FALLBACK); });

  // Timeout de segurança
  setTimeout(function(){ if(FALLBACK) go(FALLBACK); }, 5000);
})();
</script>
</body></html>`
}

// ── Visitas ───────────────────────────────────────────────────────────────

// GET /v1/dashboard/shield/visits
func (h *ShieldHandler) ListVisits(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	type VisitRow struct {
		ID         string   `json:"id"`
		IP         string   `json:"ip"`
		Country    string   `json:"country"`
		Device     string   `json:"device"`
		IsBot      bool     `json:"is_bot"`
		BotScore   float64  `json:"bot_score"`
		Signals    []string `json:"signals"`
		Action     string   `json:"action"`
		DestURL    string   `json:"dest_url"`
		CreatedAt  string   `json:"created_at"`
	}

	rows, err := h.svc.DB().Query(c.Context(), `
		SELECT id::text, ip, country, device, is_bot, bot_score, signals, action, dest_url, created_at::text
		FROM shield_visits
		WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, p.ID, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	var visits []VisitRow
	for rows.Next() {
		var v VisitRow
		if err := rows.Scan(&v.ID, &v.IP, &v.Country, &v.Device, &v.IsBot,
			&v.BotScore, &v.Signals, &v.Action, &v.DestURL, &v.CreatedAt); err != nil {
			continue
		}
		visits = append(visits, v)
	}
	if visits == nil {
		visits = []VisitRow{}
	}

	var total int
	_ = h.svc.DB().QueryRow(c.Context(),
		`SELECT COUNT(*) FROM shield_visits WHERE project_id = $1`, p.ID,
	).Scan(&total)

	return c.JSON(fiber.Map{"data": visits, "total": total, "limit": limit, "offset": offset})
}

// GET /v1/dashboard/shield/geo?days=30
//
// Retorna top cidades de acesso para exibição no globe, agrupadas por (city, country)
// com lat/lon médio e contagem de visitas.
func (h *ShieldHandler) GeoStats(c *fiber.Ctx) error {
	p := middleware.GetProject(c)
	if p == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	days, _ := strconv.Atoi(c.Query("days", "30"))
	locations, err := h.svc.GetGeoStats(c.Context(), p.ID, days)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"data": locations})
}

// accountLimits loads the account plan and returns its limits.
func (h *ShieldHandler) accountLimits(c *fiber.Ctx, accountID uuid.UUID) (plans.Limits, string) {
	var plan string
	_ = h.db.QueryRow(c.Context(),
		`SELECT plan FROM accounts WHERE id = $1`, accountID,
	).Scan(&plan)
	if plan == "" {
		plan = "free"
	}
	return plans.Get(plan), plan
}
