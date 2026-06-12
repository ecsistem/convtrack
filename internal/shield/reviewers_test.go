package shield

import "testing"

func TestIsReviewerIP(t *testing.T) {
	reviewers := []string{
		"157.240.1.35",   // Meta (157.240.0.0/16)
		"31.13.64.1",     // Meta (31.13.64.0/18)
		"66.249.66.1",    // Googlebot (66.249.64.0/19)
		"142.250.1.1",    // Google (142.250.0.0/15)
	}
	for _, ip := range reviewers {
		if !isReviewerIP(ip) {
			t.Errorf("esperava revisor: %s", ip)
		}
	}

	normal := []string{
		"8.8.8.8",        // fora das faixas listadas
		"189.40.1.1",     // ISP residencial BR
		"203.0.113.5",    // TEST-NET
		"",               // vazio
		"not-an-ip",      // inválido
	}
	for _, ip := range normal {
		if isReviewerIP(ip) {
			t.Errorf("não esperava revisor: %q", ip)
		}
	}
}

func TestIsDatacenterIP(t *testing.T) {
	if !isDatacenterIP("167.99.1.1") { // DigitalOcean 167.99.0.0/16
		t.Error("esperava datacenter: 167.99.1.1")
	}
	if !isDatacenterIP("18.200.1.1") { // AWS 18.0.0.0/8
		t.Error("esperava datacenter: 18.200.1.1")
	}
	// Faixas residenciais/CGNAT excluídas de propósito:
	if isDatacenterIP("100.64.1.1") { // CGNAT móvel
		t.Error("NÃO devia bloquear CGNAT 100.64.1.1 (usuário móvel)")
	}
	if isDatacenterIP("99.1.1.1") { // residencial US
		t.Error("NÃO devia bloquear residencial 99.1.1.1")
	}
	if isDatacenterIP("189.40.1.1") { // ISP residencial BR
		t.Error("NÃO devia bloquear residencial BR")
	}
}

func TestIsVPNIP(t *testing.T) {
	if !isVPNIP("146.70.1.1") { // 146.70.0.0/16
		t.Error("esperava VPN: 146.70.1.1")
	}
	if isVPNIP("8.8.8.8") {
		t.Error("não esperava VPN: 8.8.8.8")
	}
}

func TestASNHelpers(t *testing.T) {
	if asNumber("AS15169 Google LLC") != "AS15169" {
		t.Errorf("asNumber falhou: %q", asNumber("AS15169 Google LLC"))
	}
	if !isDatacenterASN("AS16509 Amazon") {
		t.Error("AS16509 deveria ser datacenter")
	}
	if !isVPNASN("AS9009 M247") {
		t.Error("AS9009 deveria ser VPN")
	}
	if isDatacenterASN("AS28573 ISP residencial") {
		t.Error("AS28573 não deveria ser datacenter")
	}
}

func TestIsSoftwareRenderer(t *testing.T) {
	software := []string{
		"Google SwiftShader",
		"llvmpipe (LLVM 12.0.0, 256 bits)",
		"Mesa OffScreen",
		"Software Rasterizer",
	}
	for _, r := range software {
		if !isSoftwareRenderer(r) {
			t.Errorf("esperava software renderer: %q", r)
		}
	}
	// GPUs reais (incl. ANGLE sobre hardware) NÃO podem dar falso positivo:
	hardware := []string{
		"ANGLE (Apple, Apple M1, OpenGL 4.1)",
		"ANGLE (NVIDIA GeForce RTX 3080 Direct3D11)",
		"Apple GPU",
		"Adreno (TM) 640",
		"Mali-G78",
		"",
	}
	for _, r := range hardware {
		if isSoftwareRenderer(r) {
			t.Errorf("NÃO esperava software renderer: %q", r)
		}
	}
}

func TestCampaignClickIDParams(t *testing.T) {
	// Múltiplas plataformas → um param por plataforma, sem duplicatas.
	c := &Campaign{Platforms: []string{"meta", "tiktok", "google"}}
	got := c.ClickIDParams()
	want := map[string]bool{"fbclid": true, "ttclid": true, "gclid": true}
	if len(got) != 3 {
		t.Fatalf("esperava 3 params, got %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("param inesperado: %s", p)
		}
	}

	// Fallback para platform singular quando platforms vazio.
	c2 := &Campaign{Platform: "meta"}
	if g := c2.ClickIDParams(); len(g) != 1 || g[0] != "fbclid" {
		t.Errorf("fallback singular falhou: %v", g)
	}

	// Plataforma sem click-id (manual) não gera param.
	c3 := &Campaign{Platforms: []string{"manual", "meta"}}
	if g := c3.ClickIDParams(); len(g) != 1 || g[0] != "fbclid" {
		t.Errorf("manual deveria ser ignorado: %v", g)
	}
}

func TestNormalizePlatforms(t *testing.T) {
	// platform singular deriva o array
	c := &Campaign{Platform: "tiktok"}
	c.normalizePlatforms()
	if len(c.Platforms) != 1 || c.Platforms[0] != "tiktok" {
		t.Errorf("deveria derivar array de platform: %v", c.Platforms)
	}

	// platform reflete a primeira do array
	c2 := &Campaign{Platforms: []string{"google", "kwai"}}
	c2.normalizePlatforms()
	if c2.Platform != "google" {
		t.Errorf("platform deveria ser google, got %q", c2.Platform)
	}
}

func TestClickIDParam(t *testing.T) {
	cases := map[string]string{
		"meta":     "fbclid",
		"facebook": "fbclid",
		"google":   "gclid",
		"tiktok":   "ttclid",
		"kwai":     "clickid",
		"manual":   "",
		"":         "",
	}
	for platform, want := range cases {
		if got := ClickIDParam(platform); got != want {
			t.Errorf("ClickIDParam(%q) = %q, want %q", platform, got, want)
		}
	}
}
