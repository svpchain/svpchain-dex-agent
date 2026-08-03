// Package agentchain exposes the chain's x/agent and x/agentwallet modules as
// A2A operations: gRPC queries over both, and unsigned tx building for the
// registration and delegation lifecycles.
//
// The build operations follow the platform's write flow exactly: the service
// constructs the sdk.Msg, validates it, and assembles a payload.TxPayload for
// the *caller* to sign with its own key and land via broadcast_signed_tx. The
// agent's own operator key never signs here — that is the delegated-execution
// service's job.
package agentchain

import (
	"context"
	"encoding/hex"
	"fmt"

	sdkmath "cosmossdk.io/math"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/builder"
	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// AgentQuerier is the slice of the generated x/agent QueryClient the service
// needs — an interface so tests fake it without a gRPC conn.
type AgentQuerier interface {
	Agent(ctx context.Context, in *agenttypes.QueryAgent, opts ...grpc.CallOption) (*agenttypes.QueryAgentResponse, error)
	AgentByOperator(ctx context.Context, in *agenttypes.QueryAgentByOperator, opts ...grpc.CallOption) (*agenttypes.QueryAgentByOperatorResponse, error)
	AllAgents(ctx context.Context, in *agenttypes.QueryAllAgents, opts ...grpc.CallOption) (*agenttypes.QueryAllAgentsResponse, error)
	AgentsByOwner(ctx context.Context, in *agenttypes.QueryAgentsByOwner, opts ...grpc.CallOption) (*agenttypes.QueryAgentsByOwnerResponse, error)
	AgentsByCapability(ctx context.Context, in *agenttypes.QueryAgentsByCapability, opts ...grpc.CallOption) (*agenttypes.QueryAgentsByCapabilityResponse, error)
	Params(ctx context.Context, in *agenttypes.QueryParams, opts ...grpc.CallOption) (*agenttypes.QueryParamsResponse, error)
}

// WalletQuerier is the x/agentwallet twin of AgentQuerier.
type WalletQuerier interface {
	Delegation(ctx context.Context, in *wallettypes.QueryDelegation, opts ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error)
	DelegationsByDelegator(ctx context.Context, in *wallettypes.QueryDelegationsByDelegator, opts ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error)
	Epoch(ctx context.Context, in *wallettypes.QueryEpoch, opts ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error)
	Spend(ctx context.Context, in *wallettypes.QuerySpend, opts ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error)
	Params(ctx context.Context, in *wallettypes.QueryParams, opts ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error)
}

// Service answers agent-registry and delegation operations.
type Service struct {
	agentQ    AgentQuerier
	walletQ   WalletQuerier
	asm       *builder.Assembler
	account   chain.AccountClient
	policy    *policy.Engine
	broadcast chain.BroadcastClient
	registry  codectypes.InterfaceRegistry
}

// New wires the service. asm, account, broadcast, and registry may be nil in
// query-only tests. broadcast/registry back broadcast_agent_chain_tx — the
// landing rail for caller-signed registry/delegation txs, which must reach
// the agent chain rather than wherever broadcast_signed_tx points.
func New(agentQ AgentQuerier, walletQ WalletQuerier, asm *builder.Assembler, account chain.AccountClient, engine *policy.Engine, broadcast chain.BroadcastClient, registry codectypes.InterfaceRegistry) *Service {
	return &Service{agentQ: agentQ, walletQ: walletQ, asm: asm, account: account, policy: engine, broadcast: broadcast, registry: registry}
}

// authorize is the tenant prelude every operation runs, mirroring the MCP
// handlers' contract: no tenant → the ErrNoTenant handshake, kill-switched or
// unknown tenants refused. Returns the tenant's owner address — the only
// address build operations may name as signer.
func (s *Service) authorize(ctx context.Context) (string, error) {
	tc, ok := tools.TenantFrom(ctx)
	if !ok {
		return "", tools.ErrNoTenant
	}
	if err := s.policy.CheckTenant(tc.TenantID); err != nil {
		return "", err
	}
	tp, err := s.policy.Tenant(tc.TenantID)
	if err != nil {
		return "", err
	}
	return tp.Owner, nil
}

// Coin is the JSON shape for a coin amount in base units.
type Coin struct {
	Denom  string `json:"denom"`
	Amount string `json:"amount"`
}

func (c Coin) ToSDK() (sdk.Coin, error) {
	amt, ok := sdkmath.NewIntFromString(c.Amount)
	if !ok {
		return sdk.Coin{}, fmt.Errorf("amount %q is not a valid integer", c.Amount)
	}
	if c.Denom == "" {
		return sdk.Coin{}, fmt.Errorf("denom is required")
	}
	return sdk.Coin{Denom: c.Denom, Amount: amt}, nil
}

func coinsToSDK(in []Coin) ([]sdk.Coin, error) {
	out := make([]sdk.Coin, 0, len(in))
	for i, c := range in {
		sc, err := c.ToSDK()
		if err != nil {
			return nil, fmt.Errorf("coin[%d]: %w", i, err)
		}
		out = append(out, sc)
	}
	return out, nil
}

// rootIDFromHex decodes the 32-byte delegation root id callers pass as hex.
func rootIDFromHex(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("root_id must be hex: %w", err)
	}
	if len(b) != wallettypes.RootIDLen {
		return nil, fmt.Errorf("root_id must be %d bytes, got %d", wallettypes.RootIDLen, len(b))
	}
	return b, nil
}
