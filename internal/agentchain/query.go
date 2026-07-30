package agentchain

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
)

// Query operations are thin: authorize the tenant (the same contract every
// MCP read tool honors), forward to the generated gRPC client, and return the
// generated response type verbatim — the chain's proto JSON is the schema.

func pageRequest(limit uint64) *query.PageRequest {
	if limit == 0 {
		return nil
	}
	return &query.PageRequest{Limit: limit}
}

// -- x/agent ------------------------------------------------------------

type GetAgentInput struct {
	AgentID string `json:"agent_id"`
}

func (s *Service) GetAgent(ctx context.Context, in GetAgentInput) (*agenttypes.QueryAgentResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.agentQ.Agent(ctx, &agenttypes.QueryAgent{AgentId: in.AgentID})
}

type GetAgentByOperatorInput struct {
	Operator string `json:"operator"`
}

func (s *Service) GetAgentByOperator(ctx context.Context, in GetAgentByOperatorInput) (*agenttypes.QueryAgentByOperatorResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.agentQ.AgentByOperator(ctx, &agenttypes.QueryAgentByOperator{Operator: in.Operator})
}

type ListAgentsInput struct {
	Limit uint64 `json:"limit,omitempty"`
}

func (s *Service) ListAgents(ctx context.Context, in ListAgentsInput) (*agenttypes.QueryAllAgentsResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.agentQ.AllAgents(ctx, &agenttypes.QueryAllAgents{Pagination: pageRequest(in.Limit)})
}

type GetAgentsByOwnerInput struct {
	Owner string `json:"owner"`
	Limit uint64 `json:"limit,omitempty"`
}

func (s *Service) GetAgentsByOwner(ctx context.Context, in GetAgentsByOwnerInput) (*agenttypes.QueryAgentsByOwnerResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.agentQ.AgentsByOwner(ctx, &agenttypes.QueryAgentsByOwner{Owner: in.Owner, Pagination: pageRequest(in.Limit)})
}

type GetAgentsByCapabilityInput struct {
	Capability string `json:"capability"`
	ActiveOnly bool   `json:"active_only,omitempty"`
	Limit      uint64 `json:"limit,omitempty"`
}

func (s *Service) GetAgentsByCapability(ctx context.Context, in GetAgentsByCapabilityInput) (*agenttypes.QueryAgentsByCapabilityResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.agentQ.AgentsByCapability(ctx, &agenttypes.QueryAgentsByCapability{
		Capability: in.Capability,
		ActiveOnly: in.ActiveOnly,
		Pagination: pageRequest(in.Limit),
	})
}

type EmptyInput struct{}

func (s *Service) GetAgentParams(ctx context.Context, _ EmptyInput) (*agenttypes.QueryParamsResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.agentQ.Params(ctx, &agenttypes.QueryParams{})
}

// -- x/agentwallet ------------------------------------------------------

type RootIDInput struct {
	// RootID is the 32-byte delegation root id, hex-encoded.
	RootID string `json:"root_id"`
}

func (s *Service) GetDelegation(ctx context.Context, in RootIDInput) (*wallettypes.QueryDelegationResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	rootID, err := rootIDFromHex(in.RootID)
	if err != nil {
		return nil, err
	}
	return s.walletQ.Delegation(ctx, &wallettypes.QueryDelegation{RootId: rootID})
}

type GetDelegationsByDelegatorInput struct {
	Delegator string `json:"delegator"`
	Limit     uint64 `json:"limit,omitempty"`
}

func (s *Service) GetDelegationsByDelegator(ctx context.Context, in GetDelegationsByDelegatorInput) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.walletQ.DelegationsByDelegator(ctx, &wallettypes.QueryDelegationsByDelegator{
		Delegator:  in.Delegator,
		Pagination: pageRequest(in.Limit),
	})
}

func (s *Service) GetDelegationEpoch(ctx context.Context, in RootIDInput) (*wallettypes.QueryEpochResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	rootID, err := rootIDFromHex(in.RootID)
	if err != nil {
		return nil, err
	}
	return s.walletQ.Epoch(ctx, &wallettypes.QueryEpoch{RootId: rootID})
}

func (s *Service) GetDelegationSpend(ctx context.Context, in RootIDInput) (*wallettypes.QuerySpendResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	rootID, err := rootIDFromHex(in.RootID)
	if err != nil {
		return nil, err
	}
	return s.walletQ.Spend(ctx, &wallettypes.QuerySpend{RootId: rootID})
}

func (s *Service) GetAgentwalletParams(ctx context.Context, _ EmptyInput) (*wallettypes.QueryParamsResponse, error) {
	if _, err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return s.walletQ.Params(ctx, &wallettypes.QueryParams{})
}
