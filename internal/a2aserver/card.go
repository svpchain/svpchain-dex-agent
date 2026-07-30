package a2aserver

import "github.com/a2aproject/a2a-go/v2/a2a"

// Agent identity. A DEX agent is a third party in the SVP architecture — no
// more privileged than any other service behind an agent — so its card claims
// nothing about the chain it reads from beyond being able to read it.
const (
	AgentName        = "svpchain-dex-agent"
	AgentVersion     = "0.1.0"
	AgentDescription = "Read-layer market intelligence for an SVP-Chain perpetuals DEX: " +
		"markets, orderbooks, funding, and batch-auction clearing-price estimates. " +
		"Execution is delegated and gated separately."
)

// Skill IDs. The market-data skill is answerable by anyone; the execution skill
// requires a delegation credential and is not yet served.
const (
	SkillMarketData = "svpchain-market-data"
	SkillExecution  = "svpchain-execution"
)

// BuildAgentCard returns the public Agent Card.
//
// Two skills, and the split is the point. The market-data skill is
// `required: false` — usable with no credential, no svpchain account, paid for
// per call over x402 — so the read layer can go live and earn before any of the
// delegation machinery exists. The execution skill is `required: true`: it
// needs a delegation proof, and until the execution layer is built it is
// advertised but refused, so a caller learns the requirement rather than
// discovering a silent gap.
func BuildAgentCard(publicURL string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        AgentName,
		Description: AgentDescription,
		Version:     AgentVersion,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(publicURL+"/invoke", a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json"},
		Capabilities: a2a.AgentCapabilities{
			Streaming: true,
		},
		Provider: &a2a.AgentProvider{
			Org: "svpchain",
			URL: "https://www.svpchain.org",
		},
		Skills: []a2a.AgentSkill{
			{
				ID:   SkillMarketData,
				Name: "SVP-Chain Market Data",
				Description: "Read-only market intelligence from the public indexer: perpetual " +
					"markets, live orderbooks, historical funding, and a batch-auction " +
					"clearing-price estimate for a given order size. Needs no credential and " +
					"no account; billed per call via x402.",
				Tags: []string{"market-data", "orderbook", "funding", "perpetuals", "read-only", "x402"},
				Examples: []string{
					`{"skill":"svpchain-market-data","query":"markets"}`,
					`{"skill":"svpchain-market-data","query":"orderbook","ticker":"BTC-USD"}`,
					`{"skill":"svpchain-market-data","query":"estimate","ticker":"BTC-USD","side":"buy","size":"2.5"}`,
				},
			},
			{
				ID:   SkillExecution,
				Name: "SVP-Chain Delegated Execution",
				Description: "Place and cancel orders on behalf of a user under an SVP-DT " +
					"delegation credential. Requires a verified delegation proof; the position " +
					"lands on the user's subaccount, never the agent's. Not yet available.",
				Tags: []string{"execution", "trading", "delegation", "svp-dt"},
				Examples: []string{
					`{"skill":"svpchain-execution","action":"place_order","proof":"…"}`,
				},
			},
		},
	}
}
