// Package delegated is the live execution layer: it verifies SVP-DT
// delegation credential chains, wraps the delegated order in
// MsgAgentExecDelegated, signs as the registered operator, and broadcasts —
// the capability the original read-layer card advertised and refused.
package delegated

import (
	"context"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	"github.com/svpchain/svpdt"

	"github.com/svpchain/svpchain-dex-agent/internal/agentchain"
)

// GRPCResolver resolves an agent id to its registered credential-signing
// keys via the chain's x/agent Query/Agent RPC — there is no dedicated
// PublicKeys RPC, so this mirrors the chain's own keeper resolver: the
// registered PublicKey is the single acceptable key, and a missing agent or
// empty key answers ErrSigInvalid rather than an empty set.
type GRPCResolver struct {
	q agentchain.AgentQuerier
}

// NewGRPCResolver wraps an x/agent query client.
func NewGRPCResolver(q agentchain.AgentQuerier) *GRPCResolver {
	return &GRPCResolver{q: q}
}

// For binds the resolver to a request context. svpdt.Resolver has no ctx
// parameter (the library does no I/O of its own), so each verification gets a
// request-scoped view instead of a background context that outlives the call.
func (r *GRPCResolver) For(ctx context.Context) svpdt.Resolver {
	return ctxResolver{ctx: ctx, q: r.q}
}

type ctxResolver struct {
	ctx context.Context
	q   agentchain.AgentQuerier
}

func (r ctxResolver) PublicKeys(agentID string) ([][]byte, error) {
	resp, err := r.q.Agent(r.ctx, &agenttypes.QueryAgent{AgentId: agentID})
	if err != nil || resp == nil || len(resp.Agent.PublicKey) == 0 {
		return nil, svpdt.ErrSigInvalid
	}
	return [][]byte{resp.Agent.PublicKey}, nil
}
