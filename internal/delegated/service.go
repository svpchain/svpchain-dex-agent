package delegated

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	"github.com/svpchain/svpdt"

	"github.com/svpchain/svpchain-dex-agent/internal/agentchain"
	"github.com/svpchain/svpchain-dex-agent/internal/operator"
	"github.com/svpchain/svpchain-mcp/lib/mcp/builder"
	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/markets"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"

	"github.com/cosmos/evm/crypto/ethsecp256k1"
)

// Actions in the chain's delegatable-message namespace. Only these three are
// exposed as execute operations — the chain's registry allows exactly these
// (plus update_leverage) and default-denies everything else, so refusing at
// our edge gives the caller a real reason instead of a CheckTx code.
const (
	ActionPlaceOrder  = "clob.place_order"
	ActionCancelOrder = "clob.cancel_order"
	ActionBatchCancel = "clob.batch_cancel"
)

// Config wires a Service.
type Config struct {
	Priv     *ethsecp256k1.PrivKey
	Operator string // bech32 operator address (key-derived)
	ChainID  string
	Fee      operator.FeeSpec

	AgentQ    agentchain.AgentQuerier
	AuthQ     AuthKeyQuerier
	WalletQ   agentchain.WalletQuerier
	Markets   *markets.Cache
	Account   chain.AccountClient
	Broadcast chain.BroadcastClient
	Policy    *policy.Engine

	// Registration metadata for agent_self_register.
	Endpoint     string
	Capabilities []string
	Metadata     string
}

// Service executes delegated orders under SVP-DT credentials and manages the
// agent's own registry identity.
type Service struct {
	cfg      Config
	agentID  string // did:svp:<operator>, the audience credentials must name
	resolver *GRPCResolver

	// seqMu serializes fee-paying (sequence-consuming) txs. Wrapped
	// short-term clob orders skip sequence increment on chain, so they don't
	// contend; stateful ones would race the operator's account sequence.
	seqMu sync.Mutex

	// paramsMu guards a short-TTL cache of the agentwallet params. The
	// ceilings (max depth, max TTL) change only by governance, so refetching
	// them per execution is pure latency; the TTL keeps a governance change
	// from being missed for more than a minute.
	paramsMu       sync.Mutex
	cachedParams   *wallettypes.Params
	paramsFetched  int64
	paramsCacheTTL int64

	now func() int64
}

// New builds the service. cfg.Priv must be non-nil — a keyless deployment
// registers refusing stubs instead of a Service.
func New(cfg Config) *Service {
	operatorAddr := sdk.MustAccAddressFromBech32(cfg.Operator)
	return &Service{
		cfg:            cfg,
		agentID:        agenttypes.AgentIdFromOperator(operatorAddr),
		resolver:       NewGRPCResolver(cfg.AgentQ, cfg.AuthQ),
		paramsCacheTTL: 60,
		now:            func() int64 { return time.Now().Unix() },
	}
}

// walletParams returns the agentwallet params, cached for paramsCacheTTL
// seconds.
func (s *Service) walletParams(ctx context.Context) (wallettypes.Params, error) {
	s.paramsMu.Lock()
	defer s.paramsMu.Unlock()
	if s.cachedParams != nil && s.now()-s.paramsFetched < s.paramsCacheTTL {
		return *s.cachedParams, nil
	}
	resp, err := s.cfg.WalletQ.Params(ctx, &wallettypes.QueryParams{})
	if err != nil {
		return wallettypes.Params{}, fmt.Errorf("fetch agentwallet params: %w", err)
	}
	s.cachedParams = &resp.Params
	s.paramsFetched = s.now()
	return resp.Params, nil
}

// verifyProof decodes and verifies a base64 credential chain addressed to
// this agent, returning what the chain actually grants.
func (s *Service) verifyProof(ctx context.Context, proofB64 []string) ([][]byte, *svpdt.Verified, error) {
	if len(proofB64) == 0 {
		return nil, nil, fmt.Errorf("a delegation proof is required: pass the SVP-DT token chain (base64, root first)")
	}
	tokens := make([][]byte, len(proofB64))
	for i, t := range proofB64 {
		raw, err := base64.StdEncoding.DecodeString(t)
		if err != nil {
			return nil, nil, fmt.Errorf("proof[%d] is not base64: %w", i, err)
		}
		tokens[i] = raw
	}

	// Verification ceilings come from the chain's own agentwallet params, so
	// our pre-flight and the chain's Authorize agree by construction.
	params, err := s.walletParams(ctx)
	if err != nil {
		return nil, nil, err
	}
	verified, err := svpdt.VerifyChain(tokens, s.resolver.For(ctx), svpdt.VerifyOpts{
		ChainID:       s.cfg.ChainID,
		Now:           s.now(),
		MaxDepth:      params.MaxDelegationDepth,
		MaxTTLSeconds: int64(params.MaxTokenTtlSeconds),
		Audience:      s.agentID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("credential chain rejected: %w", err)
	}
	return tokens, verified, nil
}

// preflight checks the caveat facts we can check without chain state: the
// action is granted and the subaccount is inside the blast radius. VerifyChain
// already pinned signatures, linkage, expiry, depth, and audience; the chain's
// Authorize re-checks everything against live state at execution.
func preflight(v *svpdt.Verified, action string, subaccount uint32) error {
	if !v.Effective.Actions.Has(action) {
		return fmt.Errorf("credential does not grant action %q (granted: %v)", action, v.Effective.Actions)
	}
	if !v.Effective.Subaccounts.Has(subaccount) {
		return fmt.Errorf("credential does not grant subaccount %d (granted: %v)", subaccount, v.Effective.Subaccounts)
	}
	return nil
}

// execute wraps inner in MsgAgentExecDelegated, signs as the operator, and
// broadcasts. One wrapper per tx — the chain requires the wrapper to be the
// only message.
func (s *Service) execute(ctx context.Context, tokens [][]byte, verified *svpdt.Verified, inner sdk.Msg) (ExecResult, error) {
	innerAny, err := codectypes.NewAnyWithValue(inner)
	if err != nil {
		return ExecResult{}, fmt.Errorf("pack inner msg: %w", err)
	}
	wrapper := &wallettypes.MsgAgentExecDelegated{
		Executor: s.cfg.Operator,
		AgentId:  s.agentID,
		Proof:    wallettypes.DelegationProof{Tokens: tokens},
		InnerMsg: innerAny,
	}
	if err := wrapper.ValidateBasic(); err != nil {
		return ExecResult{}, err
	}

	msgs := []sdk.Msg{wrapper}
	gasFree := operator.GasFreeFor(msgs)
	if !gasFree {
		// Stateful inner msgs consume the operator's account sequence;
		// serialize so concurrent executes don't race it.
		s.seqMu.Lock()
		defer s.seqMu.Unlock()
	}
	acct, err := s.cfg.Account.Account(ctx, s.cfg.Operator)
	if err != nil {
		return ExecResult{}, fmt.Errorf("read operator account: %w", err)
	}
	raw, err := operator.SignTx(s.cfg.Priv, s.cfg.ChainID, acct, msgs, s.cfg.Fee, gasFree)
	if err != nil {
		return ExecResult{}, err
	}
	res, err := s.cfg.Broadcast.BroadcastSync(ctx, raw)
	if err != nil {
		return ExecResult{}, fmt.Errorf("broadcast: %w", err)
	}
	return ExecResult{
		TxHash:    res.TxHash,
		Code:      res.Code,
		RawLog:    res.RawLog,
		AgentID:   s.agentID,
		Principal: verified.Principal,
	}, nil
}

// ExecResult reports a broadcast delegated execution.
type ExecResult struct {
	TxHash    string `json:"tx_hash"`
	Code      uint32 `json:"code"`
	RawLog    string `json:"raw_log,omitempty"`
	AgentID   string `json:"agent_id"`
	Principal string `json:"principal"`
}

// OrderParams mirrors the trading builder's limit-order surface; the owner is
// deliberately absent — it is always the credential's principal, so a caller
// cannot place an order on an account the delegation does not cover.
type OrderParams struct {
	SubaccountNumber uint32 `json:"subaccount_number"`
	Ticker           string `json:"ticker"`
	Side             string `json:"side"` // "BUY" | "SELL"
	Size             string `json:"size"`
	Price            string `json:"price"`
	TimeInForce      string `json:"time_in_force,omitempty"` // "", "IOC", "POST_ONLY"
	ReduceOnly       bool   `json:"reduce_only,omitempty"`
	GoodTilBlock     uint32 `json:"good_til_block"`
	OrderClientID    uint32 `json:"order_client_id"`
}

type ExecPlaceOrderInput struct {
	Proof []string    `json:"proof"` // base64 SVP-DT tokens, root first
	Order OrderParams `json:"order"`
}

// ExecutePlaceOrder verifies the proof and places a limit order for the
// credential's principal.
func (s *Service) ExecutePlaceOrder(ctx context.Context, in ExecPlaceOrderInput) (ExecResult, error) {
	tokens, verified, err := s.verifyProof(ctx, in.Proof)
	if err != nil {
		return ExecResult{}, err
	}
	if err := preflight(verified, ActionPlaceOrder, in.Order.SubaccountNumber); err != nil {
		return ExecResult{}, err
	}
	inner, _, err := builder.BuildPlaceLimitOrder(builder.PlaceLimitOrderInput{
		Owner:         verified.Principal,
		SubaccountNum: in.Order.SubaccountNumber,
		Ticker:        in.Order.Ticker,
		Side:          in.Order.Side,
		HumanSize:     in.Order.Size,
		HumanPrice:    in.Order.Price,
		TimeInForce:   in.Order.TimeInForce,
		ReduceOnly:    in.Order.ReduceOnly,
		GoodTilBlock:  in.Order.GoodTilBlock,
		OrderClientID: in.Order.OrderClientID,
	}, s.cfg.Markets, discardAssembler(s), 0, 0)
	if err != nil {
		return ExecResult{}, err
	}
	return s.execute(ctx, tokens, verified, inner)
}

type CancelParams struct {
	SubaccountNumber uint32 `json:"subaccount_number"`
	ClobPairID       uint32 `json:"clob_pair_id"`
	OrderClientID    uint32 `json:"order_client_id"`
	OrderFlags       uint32 `json:"order_flags"` // 0=short-term, 32=conditional, 64=long-term
	GoodTilBlock     uint32 `json:"good_til_block,omitempty"`
	GoodTilBlockTime uint32 `json:"good_til_block_time,omitempty"`
}

type ExecCancelOrderInput struct {
	Proof  []string     `json:"proof"`
	Cancel CancelParams `json:"cancel"`
}

// ExecuteCancelOrder verifies the proof and cancels the principal's order.
func (s *Service) ExecuteCancelOrder(ctx context.Context, in ExecCancelOrderInput) (ExecResult, error) {
	tokens, verified, err := s.verifyProof(ctx, in.Proof)
	if err != nil {
		return ExecResult{}, err
	}
	if err := preflight(verified, ActionCancelOrder, in.Cancel.SubaccountNumber); err != nil {
		return ExecResult{}, err
	}
	inner, _, err := builder.BuildCancelOrder(builder.CancelOrderInput{
		Owner:            verified.Principal,
		SubaccountNum:    in.Cancel.SubaccountNumber,
		ClobPairID:       in.Cancel.ClobPairID,
		OrderClientID:    in.Cancel.OrderClientID,
		OrderFlags:       in.Cancel.OrderFlags,
		GoodTilBlock:     in.Cancel.GoodTilBlock,
		GoodTilBlockTime: in.Cancel.GoodTilBlockTime,
	}, discardAssembler(s), 0, 0)
	if err != nil {
		return ExecResult{}, err
	}
	return s.execute(ctx, tokens, verified, inner)
}

type BatchCancelParams struct {
	SubaccountNumber uint32       `json:"subaccount_number"`
	Batches          []OrderBatch `json:"batches"`
	GoodTilBlock     uint32       `json:"good_til_block"`
}

type OrderBatch struct {
	ClobPairID uint32   `json:"clob_pair_id"`
	ClientIDs  []uint32 `json:"client_ids"`
}

type ExecBatchCancelInput struct {
	Proof  []string          `json:"proof"`
	Cancel BatchCancelParams `json:"cancel"`
}

// ExecuteBatchCancel verifies the proof and batch-cancels for the principal.
func (s *Service) ExecuteBatchCancel(ctx context.Context, in ExecBatchCancelInput) (ExecResult, error) {
	tokens, verified, err := s.verifyProof(ctx, in.Proof)
	if err != nil {
		return ExecResult{}, err
	}
	if err := preflight(verified, ActionBatchCancel, in.Cancel.SubaccountNumber); err != nil {
		return ExecResult{}, err
	}
	batches := make([]builder.OrderBatchInput, 0, len(in.Cancel.Batches))
	for _, b := range in.Cancel.Batches {
		batches = append(batches, builder.OrderBatchInput{ClobPairID: b.ClobPairID, ClientIDs: b.ClientIDs})
	}
	inner, _, err := builder.BuildBatchCancelOrders(builder.BatchCancelOrdersInput{
		Owner:         verified.Principal,
		SubaccountNum: in.Cancel.SubaccountNumber,
		Batches:       batches,
		GoodTilBlock:  in.Cancel.GoodTilBlock,
	}, discardAssembler(s), 0, 0)
	if err != nil {
		return ExecResult{}, err
	}
	return s.execute(ctx, tokens, verified, inner)
}

// discardAssembler returns a throwaway assembler for the builder functions,
// whose TxPayload output this service discards — it signs its own wrapper tx
// instead. Kept as a function so the intent reads at the call site.
func discardAssembler(s *Service) *builder.Assembler {
	return builder.NewAssembler(s.cfg.ChainID, s.cfg.Fee.Denom, s.cfg.Fee.Amount, s.cfg.Fee.GasLimit)
}

// -- identity + self-registration ---------------------------------------

type IdentityOutput struct {
	Operator   string `json:"operator"`
	AgentID    string `json:"agent_id"`
	Registered bool   `json:"registered"`
	Status     string `json:"status,omitempty"`
	Bond       string `json:"bond,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
}

// Identity reports who this agent is on chain: its operator address, derived
// DID, and current registration state.
func (s *Service) Identity(ctx context.Context, _ agentchain.EmptyInput) (IdentityOutput, error) {
	out := IdentityOutput{Operator: s.cfg.Operator, AgentID: s.agentID}
	resp, err := s.cfg.AgentQ.AgentByOperator(ctx, &agenttypes.QueryAgentByOperator{Operator: s.cfg.Operator})
	if err != nil || resp == nil || resp.Agent.AgentId == "" {
		return out, nil // unregistered is an answer, not an error
	}
	out.Registered = true
	out.Status = resp.Agent.Status.String()
	out.Bond = resp.Agent.Bond.String()
	out.Endpoint = resp.Agent.Endpoint
	return out, nil
}

type SelfRegisterInput struct {
	// Bond overrides the initial bond; empty means the module's MinBond.
	Bond *agentchain.Coin `json:"bond,omitempty"`
}

// SelfRegister signs and broadcasts this agent's own MsgRegisterAgent:
// owner = operator = the configured key, agent id derived from it, public key
// the operator's own. Gated on an authenticated tenant whose owner IS the
// operator — registration escrows the operator's funds (fee + bond), and that
// decision belongs to whoever holds the operator key, proven via the same
// auth_challenge flow as everything else.
func (s *Service) SelfRegister(ctx context.Context, in SelfRegisterInput) (ExecResult, error) {
	tc, ok := tools.TenantFrom(ctx)
	if !ok {
		return ExecResult{}, tools.ErrNoTenant
	}
	tp, err := s.cfg.Policy.Tenant(tc.TenantID)
	if err != nil {
		return ExecResult{}, err
	}
	if tp.Owner != s.cfg.Operator {
		return ExecResult{}, fmt.Errorf(
			"self-registration moves the operator's funds and may only be invoked by the operator itself (authenticated as %s, operator is %s)",
			tp.Owner, s.cfg.Operator)
	}

	var bond sdk.Coin
	if in.Bond != nil {
		if bond, err = in.Bond.ToSDK(); err != nil {
			return ExecResult{}, fmt.Errorf("bond: %w", err)
		}
	} else {
		params, err := s.cfg.AgentQ.Params(ctx, &agenttypes.QueryParams{})
		if err != nil {
			return ExecResult{}, fmt.Errorf("fetch agent params for min bond: %w", err)
		}
		bond = params.Params.MinBond
	}

	capHash := sha256.Sum256([]byte(s.cfg.Endpoint))
	msg := &agenttypes.MsgRegisterAgent{
		Owner:          s.cfg.Operator,
		AgentId:        s.agentID,
		Operator:       s.cfg.Operator,
		PublicKey:      s.cfg.Priv.PubKey().Bytes(),
		Endpoint:       s.cfg.Endpoint,
		CapabilityHash: capHash[:],
		Capabilities:   s.cfg.Capabilities,
		InitialBond:    bond,
		Metadata:       s.cfg.Metadata,
	}
	if err := msg.ValidateBasic(); err != nil {
		return ExecResult{}, err
	}

	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	acct, err := s.cfg.Account.Account(ctx, s.cfg.Operator)
	if err != nil {
		return ExecResult{}, fmt.Errorf("read operator account: %w", err)
	}
	raw, err := operator.SignTx(s.cfg.Priv, s.cfg.ChainID, acct, []sdk.Msg{msg}, s.cfg.Fee, false)
	if err != nil {
		return ExecResult{}, err
	}
	res, err := s.cfg.Broadcast.BroadcastSync(ctx, raw)
	if err != nil {
		return ExecResult{}, fmt.Errorf("broadcast: %w", err)
	}
	return ExecResult{TxHash: res.TxHash, Code: res.Code, RawLog: res.RawLog, AgentID: s.agentID, Principal: s.cfg.Operator}, nil
}
