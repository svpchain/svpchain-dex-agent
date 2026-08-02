// Package delegated is the live execution layer: it verifies SVP-DT
// delegation credential chains, wraps the delegated order in
// MsgAgentExecDelegated, signs as the registered operator, and broadcasts —
// the capability the original read-layer card advertised and refused.
package delegated

import (
	"bytes"
	"context"
	"strings"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	"github.com/svpchain/svpdt"

	"github.com/svpchain/svpchain-dex-agent/internal/agentchain"
)

// AuthKeyQuerier resolves a bech32 address to the compressed secp256k1 pubkey
// its x/auth account has published, for issuers that are not registered
// agents. A missing account, an account that has never signed, or a key of an
// unexpected type all answer an error.
type AuthKeyQuerier interface {
	AccountPubKey(ctx context.Context, address string) ([]byte, error)
}

// GRPCResolver resolves an agent id to its credential-signing keys via the
// chain's x/agent Query/Agent RPC, mirroring the chain's own keeper resolver:
// the registered PublicKey is the single acceptable key, and the registry
// always wins.
//
// A DID that is not registered falls back to the pubkey its x/auth account
// has published. That is the shape a user-issued root credential takes — the
// principal signs the first hop with their own account key, unregistered by
// design. The fallback here is deliberately blanket rather than restricted to
// the chain's root position: this resolver is preflight only, and the chain's
// Authorize re-verifies with the authoritative position-restricted rule.
type GRPCResolver struct {
	q    agentchain.AgentQuerier
	auth AuthKeyQuerier
}

// NewGRPCResolver wraps an x/agent query client, with an x/auth fallback for
// unregistered issuers. auth may be nil, which disables the fallback.
func NewGRPCResolver(q agentchain.AgentQuerier, auth AuthKeyQuerier) *GRPCResolver {
	return &GRPCResolver{q: q, auth: auth}
}

// For binds the resolver to a request context. svpdt.Resolver has no ctx
// parameter (the library does no I/O of its own), so each verification gets a
// request-scoped view instead of a background context that outlives the call.
func (r *GRPCResolver) For(ctx context.Context) svpdt.Resolver {
	return ctxResolver{ctx: ctx, q: r.q, auth: r.auth}
}

type ctxResolver struct {
	ctx  context.Context
	q    agentchain.AgentQuerier
	auth AuthKeyQuerier
}

func (r ctxResolver) PublicKeys(agentID string) ([][]byte, error) {
	resp, err := r.q.Agent(r.ctx, &agenttypes.QueryAgent{AgentId: agentID})
	if err == nil && resp != nil && len(resp.Agent.PublicKey) > 0 {
		return [][]byte{resp.Agent.PublicKey}, nil
	}
	if r.auth == nil {
		return nil, svpdt.ErrSigInvalid
	}
	bech, ok := strings.CutPrefix(agentID, agenttypes.DIDPrefix)
	if !ok {
		return nil, svpdt.ErrSigInvalid
	}
	key, err := r.auth.AccountPubKey(r.ctx, bech)
	if err != nil || len(key) != svpdt.PubKeyLen {
		return nil, svpdt.ErrSigInvalid
	}
	return [][]byte{key}, nil
}

// authKeyClient implements AuthKeyQuerier over cosmos.auth.v1beta1.Query.
type authKeyClient struct {
	inner    authtypes.QueryClient
	registry codectypes.InterfaceRegistry
}

// NewAuthKeyClient wires an AuthKeyQuerier from an auth query client and the
// interface registry needed to unpack the account Any.
func NewAuthKeyClient(
	inner authtypes.QueryClient,
	registry codectypes.InterfaceRegistry,
) AuthKeyQuerier {
	return &authKeyClient{inner: inner, registry: registry}
}

func (c *authKeyClient) AccountPubKey(ctx context.Context, address string) ([]byte, error) {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return nil, err
	}
	resp, err := c.inner.Account(ctx, &authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return nil, err
	}
	var acc sdk.AccountI
	if err := c.registry.UnpackAny(resp.Account, &acc); err != nil {
		return nil, err
	}
	pub := acc.GetPubKey()
	if pub == nil {
		return nil, svpdt.ErrSigInvalid
	}
	switch pub.(type) {
	case *secp256k1.PubKey, *ethsecp256k1.PubKey:
	default:
		return nil, svpdt.ErrSigInvalid
	}
	// The ante guarantees a published key hashes to its account's address;
	// asserted here anyway so the fallback stays correct on its own terms.
	if !bytes.Equal(pub.Address().Bytes(), addr.Bytes()) {
		return nil, svpdt.ErrSigInvalid
	}
	return pub.Bytes(), nil
}
