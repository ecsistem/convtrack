package plans

// Limits defines feature caps for a billing plan.
type Limits struct {
	MaxCampaigns int  // -1 = unlimited
	MaxDomains   int  // -1 = unlimited
	MaxSessions  int  // -1 = unlimited (monthly)
	CloneEnabled bool // offer page cloner
	VideoEnabled bool // video camouflage
}

var all = map[string]Limits{
	"free":       {MaxCampaigns: 3, MaxDomains: 0, MaxSessions: 1_000, CloneEnabled: false, VideoEnabled: false},
	"starter":    {MaxCampaigns: 10, MaxDomains: 1, MaxSessions: 50_000, CloneEnabled: false, VideoEnabled: false},
	"pro":        {MaxCampaigns: 50, MaxDomains: 5, MaxSessions: 200_000, CloneEnabled: false, VideoEnabled: false},
	"agency":     {MaxCampaigns: -1, MaxDomains: -1, MaxSessions: -1, CloneEnabled: true, VideoEnabled: true},
	"enterprise": {MaxCampaigns: -1, MaxDomains: -1, MaxSessions: -1, CloneEnabled: true, VideoEnabled: true},
}

// Get returns the limits for a given plan name. Defaults to "free" if unknown.
func Get(plan string) Limits {
	if l, ok := all[plan]; ok {
		return l
	}
	return all["free"]
}
