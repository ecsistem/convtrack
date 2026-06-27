package shield

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ── CAPTCHA de clique (stateless, HMAC-SHA256) ───────────────────────────────
//
// Não é mais um desafio matemático — o usuário só precisa clicar no botão.
// O token prova que a página foi de fato renderizada e clicada dentro do TTL
// (a maioria dos scrapers/bots simples não executa JS nem dispara o clique).
//
// Token format (base64url): "<issued_at>:<campaignID>:<hmac>"
// Válido por 10 minutos. Secret = access_key da campanha (fallback: salt fixo).

const captchaTTL = 10 * time.Minute
const captchaFallbackSecret = "ct-shield-captcha-v1"

// captchaMinDelay é o tempo mínimo entre o token ser gerado e o clique chegar
// no servidor. Cliques abaixo disso são suspeitos (replay automatizado/scraper
// que já tinha o token pronto), não um humano de fato lendo a página.
const captchaMinDelay = 350 * time.Millisecond

// captchaSecret retorna o secret para HMAC — usa access_key se disponível.
func captchaSecret(accessKey string) string {
	if accessKey != "" {
		return accessKey
	}
	return captchaFallbackSecret
}

// CaptchaChallenge representa um desafio de clique gerado.
type CaptchaChallenge struct {
	Token string // token opaco para passar no form (base64url)
}

// GenerateCaptcha cria um desafio de clique (sem pergunta) e retorna o token opaco.
func GenerateCaptcha(campaignID, accessKey string) CaptchaChallenge {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	payload := fmt.Sprintf("%s:%s", ts, campaignID)
	sig := computeHMAC(payload, captchaSecret(accessKey))
	token := base64.RawURLEncoding.EncodeToString([]byte(payload + ":" + sig))

	return CaptchaChallenge{Token: token}
}

// VerifyCaptcha valida o clique contra o token. userAnswer é ignorado — mantido
// na assinatura por compatibilidade com o handler (forms antigos enviam "answer").
// Retorna true se o token é válido, não expirou e já passou o delay mínimo.
func VerifyCaptcha(token, userAnswer, campaignID, accessKey string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 {
		return false
	}
	ts, storedCampaignID, storedSig := parts[0], parts[1], parts[2]

	// Verifica campanha
	if storedCampaignID != campaignID {
		return false
	}

	// Verifica TTL + delay mínimo (clique instantâneo = suspeito)
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	elapsed := time.Since(time.Unix(tsInt, 0))
	if elapsed > captchaTTL || elapsed < captchaMinDelay {
		return false
	}

	// Verifica HMAC
	payload := fmt.Sprintf("%s:%s", ts, storedCampaignID)
	expectedSig := computeHMAC(payload, captchaSecret(accessKey))
	return hmac.Equal([]byte(storedSig), []byte(expectedSig))
}

func computeHMAC(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
