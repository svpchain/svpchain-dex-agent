package a2aserver

import (
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/svpchain/svpchain-dex-agent/internal/toolbridge"
)

// Agent identity. A DEX agent is a third party in the SVP architecture — no
// more privileged than any other service behind an agent — so its card claims
// nothing about the chain beyond being able to reach it.
const (
	AgentName        = "svpchain-dex-agent"
	AgentVersion     = "0.2.0"
	AgentDescription = "Full-surface agent for an SVP-Chain perpetuals DEX: market data, " +
		"accounts, unsigned tx building (trading, funds, agent registry, delegations), " +
		"broadcast, self-service auth, faucet, EVM (swap/bridge/ERC), Lendora money " +
		"market, and delegated execution under SVP-DT credentials."
)

// Legacy skill-ID aliases, kept because the original two-skill card shipped
// them and callers may hold them.
const (
	SkillMarketData = toolbridge.SkillMarketData
	SkillExecution  = toolbridge.SkillExecution
)

// skillMeta is the static, human-facing half of a skill; the tool list comes
// from the registry so the card can never advertise an operation the executor
// would not dispatch (a test asserts the two agree).
type skillMeta struct {
	id       string
	name     string
	desc     string
	tags     []string
	examples []string
}

var skillMetas = []skillMeta{
	{
		id:   toolbridge.SkillMarketData,
		name: "SVP-Chain Market Data",
		desc: "Read-only market intelligence: perpetual markets, live orderbooks, candles, " +
			"trades, funding, oracle price, and a batch-auction clearing-price estimate " +
			"for a given order size. Needs no credential and no account.",
		tags: []string{"market-data", "orderbook", "funding", "perpetuals", "read-only"},
		examples: []string{
			`{"skill":"svpchain-market-data","query":"estimate","ticker":"BTC-USD","side":"buy","size":"2.5"}`,
			`{"skill":"svpchain-market-data","tool":"list_markets"}`,
			`{"skill":"svpchain-market-data","tool":"get_orderbook","args":{"ticker":"BTC-USD"}}`,
		},
	},
	{
		id:   toolbridge.SkillAccount,
		name: "SVP-Chain Account & Positions",
		desc: "Owner-scoped reads: subaccounts (indexer and live chain), wallet balances, " +
			"orders, fills, transfers, PnL, and funding payments. Requires a bearer from " +
			"the svpchain-auth skill — or, for get_subaccount and get_balance, an SVP-DT " +
			`credential granting the "query.account" action, attached as message.metadata ` +
			`"svp.delegation/v1"; the owner then defaults to the credential's principal, ` +
			"and the credential stays reusable for polling until it expires.",
		tags: []string{"account", "positions", "pnl", "orders"},
		examples: []string{
			`{"skill":"svpchain-account","tool":"get_subaccount","args":{"address":"svp1…","subaccount_number":0},"bearer":"…"}`,
			`message.metadata: {"svp.delegation/v1":{"tokens":["<base64 token>", "…"]}} · ` +
				`text: {"skill":"svpchain-account","tool":"get_balance","args":{}}`,
		},
	},
	{
		id:   toolbridge.SkillTrading,
		name: "SVP-Chain Trading (build)",
		desc: "Build unsigned order transactions — limit, market, conditional, cancel, batch " +
			"cancel — as payloads the caller signs with its own key and lands via " +
			"broadcast_signed_tx. The agent never holds the caller's key.",
		tags: []string{"trading", "orders", "unsigned-tx"},
		examples: []string{
			`{"skill":"svpchain-trading","tool":"build_place_limit_order","args":{"owner":"svp1…","subaccount_number":0,"ticker":"BTC-USD","side":"buy","size":"0.1","price":"60000"},"bearer":"…"}`,
		},
	},
	{
		id:   toolbridge.SkillFunds,
		name: "SVP-Chain Funds (build)",
		desc: "Build unsigned funds movements — deposit/withdraw/transfer between subaccounts, " +
			"bank send — plus per-symbol daily transfer-out caps. Movements are size-capped " +
			"by the operator's limits config.",
		tags: []string{"funds", "deposit", "withdraw", "unsigned-tx"},
	},
	{
		id:   toolbridge.SkillBroadcast,
		name: "SVP-Chain Broadcast",
		desc: "Land a signed transaction (the signer must be the authenticated owner) and " +
			"query transaction status by hash.",
		tags: []string{"broadcast", "tx-status"},
	},
	{
		id:   toolbridge.SkillAuth,
		name: "SVP-Chain Self-Service Auth",
		desc: "Wallet-signature authentication: auth_challenge issues a challenge bound to " +
			"an owner address, auth_verify checks the wallet's signature and mints a " +
			"bearer token that authenticates subsequent calls (Authorization header, " +
			"envelope field, or bound to this A2A context).",
		tags: []string{"auth", "bearer", "wallet-signature"},
		examples: []string{
			`{"skill":"svpchain-auth","tool":"auth_challenge","args":{"owner":"svp1…"}}`,
			`{"skill":"svpchain-auth","tool":"auth_verify","args":{"nonce":"…","signature":"…"}}`,
		},
	},
	{
		id:   toolbridge.SkillFaucet,
		name: "SVP-Chain Faucet",
		desc: "Testnet faucet: list claimable tokens and claim them to an address.",
		tags: []string{"faucet", "testnet"},
	},
	{
		id:   toolbridge.SkillEVM,
		name: "SVP-Chain EVM",
		desc: "EVM-side operations: broadcast raw txs and track status, quote and build " +
			"Uniswap-style swaps, build bridge deposits (outbound and inbound from " +
			"registered foreign chains), and build ERC-20/ERC-721 transfers and approvals. " +
			"Served only when the deployment configures the EVM endpoints.",
		tags: []string{"evm", "swap", "bridge", "erc20", "erc721"},
	},
	{
		id:   toolbridge.SkillLendora,
		name: "Lendora Money Market",
		desc: "Lendora (Compound V2 fork) reads — markets, dashboards, account positions, " +
			"risk assessment — and unsigned supply/withdraw/borrow/repay/collateral txs. " +
			"Served only when the deployment configures the Lendora comptroller.",
		tags: []string{"lendora", "lending", "money-market"},
	},
	{
		id:   toolbridge.SkillAgentRegistry,
		name: "SVP-Chain Agent Registry",
		desc: "The chain's x/agent module: query registered agents (by id, operator, owner, " +
			"capability) and build unsigned registration-lifecycle txs — register, update, " +
			"bond deposit/withdraw, deregister — for the owner to sign.",
		tags: []string{"agent-registry", "x/agent", "registration", "bond"},
	},
	{
		id:   toolbridge.SkillDelegation,
		name: "SVP-Chain Delegations",
		desc: "The chain's x/agentwallet module: query root delegations, epochs, and spend " +
			"ledgers, and build unsigned delegation-lifecycle txs — create, update, pause, " +
			"resume, revoke, revoke-token — for the delegator to sign.",
		tags: []string{"delegation", "x/agentwallet", "svp-dt"},
	},
	{
		id:   toolbridge.SkillExecution,
		name: "SVP-Chain Delegated Execution",
		desc: "Place and cancel orders — and deposit the user's own wallet USDC into " +
			"their subaccount — on behalf of a user under an SVP-DT delegation " +
			"credential: the agent verifies the credential chain, wraps the message in " +
			"MsgAgentExecDelegated, signs as the registered operator, and broadcasts. The " +
			"position lands on the user's subaccount, never the agent's; a deposit can " +
			"only move funds from the delegator's wallet into the delegator's subaccount " +
			"and debits the delegation budget. Requires a delegation proof whose leaf " +
			"token is addressed to this agent, carried in message.metadata under " +
			`"svp.delegation/v1" (or, deprecated, as the "proof" args field).`,
		tags: []string{"execution", "trading", "deposit", "delegation", "svp-dt"},
		examples: []string{
			`message.metadata: {"svp.delegation/v1":{"tokens":["<base64 token>", "…"]}} · ` +
				`text: {"skill":"svpchain-execution","tool":"execute_place_order","args":{"order":{…}}}`,
		},
	},
}

// BuildAgentCard returns the public Agent Card. The registry supplies each
// skill's tool list; reg == nil builds the legacy read-only card (market-data
// only, execution advertised-but-refused, matching the original deployment).
func BuildAgentCard(publicURL string, reg *toolbridge.Registry) *a2a.AgentCard {
	bySkill := map[string][]string{}
	if reg != nil {
		bySkill = reg.BySkill()
	}

	var skills []a2a.AgentSkill
	for _, m := range skillMetas {
		tools := bySkill[m.id]
		if reg == nil {
			// Legacy card: only market-data (served) and execution (advertised,
			// refused with a reason so callers learn the requirement).
			if m.id != toolbridge.SkillMarketData && m.id != toolbridge.SkillExecution {
				continue
			}
		} else if len(tools) == 0 {
			continue
		}
		desc := m.desc
		if len(tools) > 0 {
			desc = fmt.Sprintf("%s Tools: %s.", desc, strings.Join(tools, ", "))
		}
		skills = append(skills, a2a.AgentSkill{
			ID:          m.id,
			Name:        m.name,
			Description: desc,
			Tags:        m.tags,
			Examples:    m.examples,
		})
	}

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
			// Required is false because the read layer (market data, and the
			// whole build-and-sign-yourself surface) serves callers with no
			// credential at all; only delegated execution needs one.
			Extensions: []a2a.AgentExtension{{
				URI: DelegationExtensionURI,
				Description: "SVP-DT delegated execution and read-only account queries: attach " +
					"the credential chain as message.metadata[\"" + DelegationMetadataKey + "\"] = " +
					"{\"tokens\": [<base64 canonical-CBOR>, …]}, root-issued token first, leaf " +
					"addressed to this agent. Execution needs the matching execute action; " +
					"get_subaccount/get_balance need the \"query.account\" action (no budget).",
				Params: map[string]any{"metadataKey": DelegationMetadataKey},
			}},
		},
		Provider: &a2a.AgentProvider{
			Org: "svpchain",
			URL: "https://www.svpchain.org",
		},
		Skills: skills,
	}
}
