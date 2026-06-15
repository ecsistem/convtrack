package shield

import (
	"testing"

	"github.com/ecsistem/convtrack/internal/models"
)

func TestPickRotationURL(t *testing.T) {
	// pool vazio
	if got := pickRotationURL(&models.ShieldConfig{}); got != "" {
		t.Errorf("pool vazio deveria retornar \"\", got %q", got)
	}
	// só primary
	if got := pickRotationURL(&models.ShieldConfig{PrimaryURL: "https://a.com"}); got != "https://a.com" {
		t.Errorf("um item deveria retornar ele, got %q", got)
	}
	// nil
	if got := pickRotationURL(nil); got != "" {
		t.Errorf("nil deveria retornar \"\"")
	}

	// rotação: primary + fallbacks, ignora vazios, cobre todo o pool
	cfg := &models.ShieldConfig{
		PrimaryURL:   "https://a.com",
		FallbackURLs: []string{"https://b.com", "  ", "https://c.com"},
	}
	want := map[string]bool{"https://a.com": true, "https://b.com": true, "https://c.com": true}
	seen := map[string]int{}
	for i := 0; i < 600; i++ {
		u := pickRotationURL(cfg)
		if !want[u] {
			t.Fatalf("URL fora do pool: %q", u)
		}
		seen[u]++
	}
	// com 600 sorteios e 3 itens, todos devem ter aparecido
	if len(seen) != 3 {
		t.Errorf("esperava os 3 links na rotação, vistos: %v", seen)
	}
}
