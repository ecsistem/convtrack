package shield

import (
	"net"
	"strings"
)

// ── Faixas de IP de crawlers/revisores de plataforma de anúncio ────────────
// Tráfego dessas faixas é o robô da plataforma (link preview, revisor de
// anúncio, scraper de OG tags) — nunca um comprador real, cujo IP é o do
// próprio ISP. Sempre avaliadas contra o IP REAL do visitante (CF-Connecting-IP).
var reviewerRanges = []string{
	// ── Meta / Facebook (AS32934) ──────────────────────────────────────────
	"31.13.24.0/21", "31.13.64.0/18", "45.64.40.0/22", "66.220.144.0/20",
	"69.63.176.0/20", "69.171.224.0/19", "74.119.76.0/22", "102.132.96.0/20",
	"103.4.96.0/22", "129.134.0.0/16", "157.240.0.0/16", "173.252.64.0/18",
	"179.60.192.0/22", "185.60.216.0/22", "185.89.218.0/23", "204.15.20.0/22",
	// ── Google / Googlebot (AS15169) ───────────────────────────────────────
	"64.233.160.0/19", "66.102.0.0/20", "66.249.64.0/19", "72.14.192.0/18",
	"74.125.0.0/16", "108.177.8.0/21", "142.250.0.0/15", "172.217.0.0/16",
	"173.194.0.0/16", "209.85.128.0/17", "216.58.192.0/19", "216.239.32.0/19",
	// ── TikTok / ByteDance crawler (Cloudflare + GCP de origem do bot) ──────
	// Seguro: o usuário in-app real conecta pelo IP da operadora, não destes.
	"104.16.0.0/13", "141.101.64.0/18", "162.158.0.0/15", "172.64.0.0/13",
	"173.245.48.0/20", "188.114.96.0/20", "190.93.240.0/20", "197.234.240.0/22",
	"198.41.128.0/17", "199.27.128.0/21",
}

// datacenterRanges — faixas de provedores de nuvem/hosting. Gated por
// block_datacenter. EXCLUI de propósito 99.0.0.0/8, 100.0.0.0/8 (CGNAT/móvel),
// 104.0.0.0/8 e 107.0.0.0/8 (mistos com residencial) para não derrubar
// usuário real de operadora móvel.
var datacenterRanges = []string{
	// Grandes blocos de nuvem (AWS / GCP / Azure)
	"3.0.0.0/8", "13.0.0.0/8", "18.0.0.0/8", "34.0.0.0/8", "35.0.0.0/8",
	"52.0.0.0/8", "54.0.0.0/8",
	// DigitalOcean / Vultr / Linode / OVH (faixas /16 precisas)
	"143.55.0.0/16", "147.75.0.0/16", "159.203.0.0/16", "162.243.0.0/16",
	"163.172.0.0/16", "167.99.0.0/16", "178.62.0.0/16", "188.166.0.0/16",
	"192.241.0.0/16", "198.199.0.0/16", "206.189.0.0/16", "207.154.0.0/16",
	"209.97.0.0/16", "45.55.0.0/16", "45.63.0.0/16", "46.101.0.0/16",
	"64.225.0.0/16", "67.205.0.0/16", "68.183.0.0/16", "95.85.0.0/16",
	"128.199.0.0/16", "134.122.0.0/16", "134.209.0.0/16", "137.184.0.0/16",
	"138.68.0.0/16", "138.197.0.0/16", "139.59.0.0/16", "142.93.0.0/16",
	"143.198.0.0/16", "144.126.0.0/16", "146.190.0.0/16", "157.230.0.0/16",
	"159.65.0.0/16", "159.89.0.0/16", "161.35.0.0/16", "164.90.0.0/16",
	"164.92.0.0/16", "165.22.0.0/16", "165.227.0.0/16", "167.71.0.0/16",
	"167.172.0.0/16", "170.64.0.0/16", "174.138.0.0/16",
}

// vpnRanges — faixas conhecidas de VPN/proxy comercial. Gated por block_vpn.
var vpnRanges = []string{
	"103.86.96.0/21", "103.86.104.0/22", "146.70.0.0/16", "185.156.46.0/24",
	"185.213.82.0/24", "193.138.218.0/24", "194.127.199.0/24", "195.181.160.0/22",
	"198.54.128.0/20", "209.127.0.0/17",
}

// datacenterASNs / vpnASNs — usados quando o ip-api retorna o campo `as`.
// Complementa as faixas estáticas (pega IPs de cloud fora dos CIDRs acima).
var datacenterASNs = map[string]bool{
	"AS14061": true, "AS63949": true, "AS16276": true, "AS24940": true,
	"AS14618": true, "AS16509": true, "AS8075": true, "AS15169": true,
	"AS396982": true, "AS13335": true, "AS20473": true, "AS40021": true,
	"AS36351": true, "AS62567": true, "AS397423": true, "AS9009": true,
	"AS51167": true, "AS197540": true, "AS60068": true, "AS202425": true,
	"AS42831": true, "AS51852": true, "AS44901": true, "AS51396": true,
	"AS61317": true, "AS64475": true, "AS208046": true, "AS400328": true,
}

var vpnASNs = map[string]bool{
	"AS9009": true, "AS60068": true, "AS202425": true, "AS212238": true,
	"AS207137": true, "AS206092": true, "AS396356": true, "AS204957": true,
	"AS62240": true, "AS24940": true, "AS20278": true, "AS51167": true,
}

// Formas compiladas (preenchidas em init).
var (
	reviewerNets   []*net.IPNet
	datacenterNets []*net.IPNet
	vpnNets        []*net.IPNet
)

func init() {
	reviewerNets = compileCIDRs(reviewerRanges)
	datacenterNets = compileCIDRs(datacenterRanges)
	vpnNets = compileCIDRs(vpnRanges)
}

func compileCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func ipInNets(ip string, nets []*net.IPNet) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// isReviewerIP: faixa de crawler/revisor de plataforma (Meta/Google/TikTok).
func isReviewerIP(ip string) bool { return ipInNets(ip, reviewerNets) }

// isDatacenterIP: faixa de provedor de nuvem/hosting.
func isDatacenterIP(ip string) bool { return ipInNets(ip, datacenterNets) }

// isVPNIP: faixa de VPN/proxy comercial.
func isVPNIP(ip string) bool { return ipInNets(ip, vpnNets) }

// asNumber extrai "AS15169" do campo `as` do ip-api ("AS15169 Google LLC").
func asNumber(asField string) string {
	f := strings.TrimSpace(asField)
	if i := strings.IndexByte(f, ' '); i > 0 {
		return f[:i]
	}
	return f
}

func isDatacenterASN(asField string) bool { return datacenterASNs[asNumber(asField)] }
func isVPNASN(asField string) bool        { return vpnASNs[asNumber(asField)] }
