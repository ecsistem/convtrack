package shield

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// ── CAPTCHA token (stateless, HMAC-SHA256) ───────────────────────────────────
//
// Token format (base64url): "<answer>:<timestamp>:<hmac>"
// Valid for 10 minutes. Secret = campaign access_key (falls back to built-in salt).

const captchaTTL = 10 * time.Minute
const captchaFallbackSecret = "ct-shield-captcha-v1"

// randInt retorna um número aleatório criptograficamente seguro no intervalo [min, max].
func randInt(min, max int) int {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	return min + int(n.Int64())
}

// captchaSecret retorna o secret para HMAC — usa access_key se disponível.
func captchaSecret(accessKey string) string {
	if accessKey != "" {
		return accessKey
	}
	return captchaFallbackSecret
}

// CaptchaChallenge representa um desafio gerado.
type CaptchaChallenge struct {
	Question string // texto da pergunta (ex: "Quanto é 14 + 7?")
	Token    string // token opaco para passar no form (base64url)
}

// GenerateCaptcha cria um desafio matemático e retorna question + token opaco.
func GenerateCaptcha(campaignID, accessKey string) CaptchaChallenge {
	a := randInt(1, 20)
	b := randInt(1, 20)
	answer := a + b
	question := fmt.Sprintf("Quanto é %d + %d?", a, b)

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	payload := fmt.Sprintf("%d:%s:%s", answer, ts, campaignID)
	sig := computeHMAC(payload, captchaSecret(accessKey))
	token := base64.RawURLEncoding.EncodeToString([]byte(payload + ":" + sig))

	return CaptchaChallenge{Question: question, Token: token}
}

// VerifyCaptcha valida a resposta do usuário contra o token.
// Retorna true se a resposta é correta e o token não expirou.
func VerifyCaptcha(token, userAnswer, campaignID, accessKey string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(raw), ":", 4)
	if len(parts) != 4 {
		return false
	}
	storedAnswer, ts, storedCampaignID, storedSig := parts[0], parts[1], parts[2], parts[3]

	// Verifica campanha
	if storedCampaignID != campaignID {
		return false
	}

	// Verifica TTL
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(tsInt, 0)) > captchaTTL {
		return false
	}

	// Verifica HMAC
	payload := fmt.Sprintf("%s:%s:%s", storedAnswer, ts, storedCampaignID)
	expectedSig := computeHMAC(payload, captchaSecret(accessKey))
	if !hmac.Equal([]byte(storedSig), []byte(expectedSig)) {
		return false
	}

	// Verifica resposta do usuário
	userInt, err := strconv.Atoi(strings.TrimSpace(userAnswer))
	if err != nil {
		return false
	}
	storedInt, err := strconv.Atoi(storedAnswer)
	if err != nil {
		return false
	}
	return userInt == storedInt
}

func computeHMAC(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
