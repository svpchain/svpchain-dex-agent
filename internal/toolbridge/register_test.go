package toolbridge

import (
	"strings"
	"testing"

	"github.com/svpchain/svpchain-dex-agent/internal/agentchain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// expectedOps pins the full skill → tool table. A tool added to (or removed
// from) the bridge without updating this table fails the test — the table is
// the completeness contract for "the A2A surface covers every MCP tool".
var expectedOps = map[string][]string{
	SkillMarketData: {
		"list_markets", "get_market", "get_orderbook", "get_oracle_price",
		"get_candles", "get_trades", "get_sparklines", "get_historical_funding",
		"get_height", "get_time",
	},
	SkillAccount: {
		"get_subaccount", "get_live_subaccount", "get_balance", "whoami",
		"get_orders", "get_order", "get_fills", "get_transfers", "get_pnl",
		"get_historical_pnl", "get_funding_payments",
	},
	SkillTrading: {
		"build_place_limit_order", "build_place_market_order",
		"build_place_conditional_order", "build_cancel_order",
		"build_batch_cancel_orders",
	},
	SkillFunds: {
		"build_deposit_to_subaccount", "build_withdraw_from_subaccount",
		"build_transfer_between_subaccounts", "build_bank_send",
		"get_transfer_out_cap", "set_transfer_out_cap",
	},
	SkillBroadcast: {"broadcast_signed_tx", "get_tx_status"},
	SkillAuth:      {"auth_challenge", "auth_verify"},
	SkillFaucet:    {"list_faucet_tokens", "faucet_claim"},
	SkillEVM: {
		"broadcast_evm_tx", "evm_tx_status", "quote_swap", "build_token_approval",
		"build_swap", "build_bridge_deposit", "build_bridge_deposit_inbound",
		"build_erc20_transfer", "build_erc20_approve", "build_erc20_transfer_from",
		"build_erc721_transfer_from", "build_erc721_safe_transfer_from",
		"build_erc721_approve", "build_erc721_set_approval_for_all",
	},
	SkillLendora: {
		"lendora_get_all_markets", "lendora_get_market_details",
		"lendora_get_protocol_dashboard", "lendora_get_account_summary",
		"lendora_get_account_positions", "lendora_get_balances",
		"lendora_assess_risk", "lendora_build_supply_tx",
		"lendora_build_withdraw_tx", "lendora_build_borrow_tx",
		"lendora_build_repay_tx", "lendora_build_collateral_tx",
	},
}

// expectedChainOps pins the x/agent + x/agentwallet operation table.
var expectedChainOps = map[string][]string{
	SkillAgentRegistry: {
		"get_agent", "get_agent_by_operator", "list_agents", "get_agents_by_owner",
		"get_agents_by_capability", "get_agent_params",
		"build_register_agent", "build_update_agent", "build_deposit_bond",
		"build_withdraw_bond", "build_deregister_agent",
	},
	SkillDelegation: {
		"get_delegation", "get_delegations_by_delegator", "get_delegation_epoch",
		"get_delegation_spend", "get_agentwallet_params",
		"build_create_delegation", "build_update_delegation", "build_pause_delegation",
		"build_resume_delegation", "build_revoke_delegation", "build_revoke_token",
	},
}

func TestChainRegistrationCoversEveryExpectedOp(t *testing.T) {
	r := New(&tools.Handlers{})
	r.RegisterAgentChain(agentchain.New(nil, nil, nil, nil, nil))

	for skill, toolNames := range expectedChainOps {
		for _, tool := range toolNames {
			op, ok := r.Lookup(tool)
			if !ok {
				t.Errorf("tool %q missing from registry", tool)
				continue
			}
			if op.Skill != skill {
				t.Errorf("tool %q registered under skill %q, expected %q", tool, op.Skill, skill)
			}
		}
	}
	for skill, want := range expectedChainOps {
		got := r.BySkill()[skill]
		if len(got) != len(want) {
			t.Errorf("skill %q has %d tools registered, expected %d: %v", skill, len(got), len(want), got)
		}
	}
}

// Keyless deployments still advertise the execution surface — every op
// registered, every op refusing with the operator-key requirement.
func TestExecutionRegistrationWithoutAKeyRefusesInformatively(t *testing.T) {
	r := New(&tools.Handlers{})
	r.RegisterExecution(nil)

	for _, tool := range executionTools {
		op, ok := r.Lookup(tool)
		if !ok {
			t.Errorf("tool %q missing from registry", tool)
			continue
		}
		if op.Skill != SkillExecution {
			t.Errorf("tool %q under skill %q", tool, op.Skill)
		}
		if _, err := op.Call(nil, nil); err == nil || !strings.Contains(err.Error(), "operator key") {
			t.Errorf("keyless %q must refuse naming the operator-key requirement, got %v", tool, err)
		}
	}
}

func TestRegistryCoversEveryExpectedTool(t *testing.T) {
	r := New(&tools.Handlers{})

	total := 0
	for skill, toolNames := range expectedOps {
		for _, tool := range toolNames {
			total++
			op, ok := r.Lookup(tool)
			if !ok {
				t.Errorf("tool %q missing from registry", tool)
				continue
			}
			if op.Skill != skill {
				t.Errorf("tool %q registered under skill %q, expected %q", tool, op.Skill, skill)
			}
		}
	}
	if total != 64 {
		t.Fatalf("expected table lists %d tools; the MCP surface is 64 — fix the table", total)
	}

	// The reverse direction: nothing extra is registered under these skills.
	for skill, got := range r.BySkill() {
		want, ok := expectedOps[skill]
		if !ok {
			// Skills registered by other milestones (agent-registry,
			// delegation, execution) have their own completeness tables.
			continue
		}
		if len(got) != len(want) {
			t.Errorf("skill %q has %d tools registered, expected %d: %v", skill, len(got), len(want), got)
		}
	}
}
