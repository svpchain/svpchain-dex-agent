package toolbridge

import (
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// Skill IDs. One per operation family on the Agent Card; the registry tags
// every operation with its skill so the card and the dispatch table cannot
// drift (a test pins the mapping).
const (
	SkillMarketData    = "svpchain-market-data"
	SkillAccount       = "svpchain-account"
	SkillTrading       = "svpchain-trading"
	SkillFunds         = "svpchain-funds"
	SkillBroadcast     = "svpchain-broadcast"
	SkillAuth          = "svpchain-auth"
	SkillFaucet        = "svpchain-faucet"
	SkillEVM           = "svpchain-evm"
	SkillLendora       = "svpchain-lendora"
	SkillAgentRegistry = "svpchain-agent-registry"
	SkillDelegation    = "svpchain-delegation"
	SkillExecution     = "svpchain-execution"
)

// New builds the operation registry over the MCP tool handlers. The optional
// services (agent-registry / delegation queries, delegated execution) are
// registered by their own Register* functions so milestones land
// independently; nil-safe wiring lives there.
func New(h *tools.Handlers) *Registry {
	r := newRegistry()

	// A. Market data.
	r.add(SkillMarketData, "list_markets", adapt(h.ListMarkets))
	r.add(SkillMarketData, "get_market", adapt(h.GetMarket))
	r.add(SkillMarketData, "get_orderbook", adapt(h.GetOrderbook))
	r.add(SkillMarketData, "get_oracle_price", adapt(h.GetOraclePrice))
	r.add(SkillMarketData, "get_candles", adapt(h.GetCandles))
	r.add(SkillMarketData, "get_trades", adapt(h.GetTrades))
	r.add(SkillMarketData, "get_sparklines", adapt(h.GetSparklines))
	r.add(SkillMarketData, "get_historical_funding", adapt(h.GetHistoricalFunding))
	r.add(SkillMarketData, "get_height", adapt(h.GetHeight))
	r.add(SkillMarketData, "get_time", adapt(h.GetTime))

	// B. Account / positions.
	r.add(SkillAccount, "get_subaccount", adapt(h.GetSubaccount))
	r.add(SkillAccount, "get_live_subaccount", adapt(h.GetLiveSubaccount))
	r.add(SkillAccount, "get_balance", adapt(h.GetBalance))
	r.add(SkillAccount, "whoami", adapt(h.Whoami))
	r.add(SkillAccount, "get_orders", adapt(h.GetOrders))
	r.add(SkillAccount, "get_order", adapt(h.GetOrder))
	r.add(SkillAccount, "get_fills", adapt(h.GetFills))
	r.add(SkillAccount, "get_transfers", adapt(h.GetTransfers))
	r.add(SkillAccount, "get_pnl", adapt(h.GetPnl))
	r.add(SkillAccount, "get_historical_pnl", adapt(h.GetHistoricalPnl))
	r.add(SkillAccount, "get_funding_payments", adapt(h.GetFundingPayments))

	// C. Trading (build-only; the caller signs and lands the payload via
	// broadcast_signed_tx).
	r.add(SkillTrading, "build_place_limit_order", adapt(h.BuildPlaceLimitOrder))
	r.add(SkillTrading, "build_place_market_order", adapt(h.BuildPlaceMarketOrder))
	r.add(SkillTrading, "build_place_conditional_order", adapt(h.BuildPlaceConditionalOrder))
	r.add(SkillTrading, "build_cancel_order", adapt(h.BuildCancelOrder))
	r.add(SkillTrading, "build_batch_cancel_orders", adapt(h.BuildBatchCancelOrders))

	// D. Funds + caps.
	r.add(SkillFunds, "build_deposit_to_subaccount", adapt(h.BuildDepositToSubaccount))
	r.add(SkillFunds, "build_withdraw_from_subaccount", adapt(h.BuildWithdrawFromSubaccount))
	r.add(SkillFunds, "build_transfer_between_subaccounts", adapt(h.BuildTransferBetweenSubaccounts))
	r.add(SkillFunds, "build_bank_send", adapt(h.BuildBankSend))
	r.add(SkillFunds, "get_transfer_out_cap", adapt(h.GetTransferOutCap))
	r.add(SkillFunds, "set_transfer_out_cap", adapt(h.SetTransferOutCap))

	// E. Cosmos broadcast + status.
	r.add(SkillBroadcast, "broadcast_signed_tx", adapt(h.BroadcastSignedTx))
	r.add(SkillBroadcast, "get_tx_status", adapt(h.GetTxStatus))

	// F. Self-service auth.
	r.add(SkillAuth, "auth_challenge", adapt(h.AuthChallenge))
	r.add(SkillAuth, "auth_verify", adapt(h.AuthVerify))

	// G. Faucet.
	r.add(SkillFaucet, "list_faucet_tokens", adapt(h.ListFaucetTokens))
	r.add(SkillFaucet, "faucet_claim", adapt(h.FaucetClaim))

	// H. EVM engine + swap + bridge + ERC-20/721.
	r.add(SkillEVM, "broadcast_evm_tx", adapt(h.BroadcastEVMTx))
	r.add(SkillEVM, "evm_tx_status", adapt(h.EVMTxStatus))
	r.add(SkillEVM, "quote_swap", adapt(h.QuoteSwap))
	r.add(SkillEVM, "build_token_approval", adapt(h.BuildTokenApproval))
	r.add(SkillEVM, "build_swap", adapt(h.BuildSwap))
	r.add(SkillEVM, "build_bridge_deposit", adapt(h.BuildBridgeDeposit))
	r.add(SkillEVM, "build_bridge_deposit_inbound", adapt(h.BuildBridgeDepositInbound))
	r.add(SkillEVM, "build_erc20_transfer", adapt(h.BuildERC20Transfer))
	r.add(SkillEVM, "build_erc20_approve", adapt(h.BuildERC20Approve))
	r.add(SkillEVM, "build_erc20_transfer_from", adapt(h.BuildERC20TransferFrom))
	r.add(SkillEVM, "build_erc721_transfer_from", adapt(h.BuildERC721TransferFrom))
	r.add(SkillEVM, "build_erc721_safe_transfer_from", adapt(h.BuildERC721SafeTransferFrom))
	r.add(SkillEVM, "build_erc721_approve", adapt(h.BuildERC721Approve))
	r.add(SkillEVM, "build_erc721_set_approval_for_all", adapt(h.BuildERC721SetApprovalForAll))

	// K. Lendora money market.
	r.add(SkillLendora, "lendora_get_all_markets", adapt(h.LendoraGetAllMarkets))
	r.add(SkillLendora, "lendora_get_market_details", adapt(h.LendoraGetMarketDetails))
	r.add(SkillLendora, "lendora_get_protocol_dashboard", adapt(h.LendoraGetProtocolDashboard))
	r.add(SkillLendora, "lendora_get_account_summary", adapt(h.LendoraGetAccountSummary))
	r.add(SkillLendora, "lendora_get_account_positions", adapt(h.LendoraGetAccountPositions))
	r.add(SkillLendora, "lendora_get_balances", adapt(h.LendoraGetBalances))
	r.add(SkillLendora, "lendora_assess_risk", adapt(h.LendoraAssessRisk))
	r.add(SkillLendora, "lendora_build_supply_tx", adapt(h.LendoraBuildSupplyTx))
	r.add(SkillLendora, "lendora_build_withdraw_tx", adapt(h.LendoraBuildWithdrawTx))
	r.add(SkillLendora, "lendora_build_borrow_tx", adapt(h.LendoraBuildBorrowTx))
	r.add(SkillLendora, "lendora_build_repay_tx", adapt(h.LendoraBuildRepayTx))
	r.add(SkillLendora, "lendora_build_collateral_tx", adapt(h.LendoraBuildCollateralTx))

	return r
}
