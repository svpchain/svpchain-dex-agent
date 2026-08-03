// Package agentrest talks to the agent chain — the chain carrying x/agent +
// x/agentwallet — over its Cosmos REST API (the gRPC-gateway, typically
// :1317) instead of gRPC. One Client covers everything the agent-identity
// families need from that chain: module queries, x/auth account lookups, and
// tx broadcast. Paths mirror the google.api.http annotations compiled into
// the modules' query.pb.gw.go.
package agentrest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/grpc"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
)

// Client implements the agent-chain surface over REST. It satisfies
// agentchain.AgentQuerier, agentchain.WalletQuerier, chain.BroadcastClient,
// and delegated.AuthAccountQuerier; AccountClient() adapts it to the
// chain.AccountClient shape the tx builders take.
type Client struct {
	base     string
	http     *http.Client
	cdc      codec.Codec
	registry codectypes.InterfaceRegistry
}

// New builds a Client for the REST base URL (scheme://host:port, no trailing
// slash needed). The codec must carry the fork's interface registry so
// account Anys unpack.
func New(baseURL string, cdc codec.Codec, registry codectypes.InterfaceRegistry) *Client {
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	return &Client{
		base:     baseURL,
		http:     &http.Client{Timeout: 15 * time.Second},
		cdc:      cdc,
		registry: registry,
	}
}

// get fetches path (already escaped) and proto-JSON-decodes the body into out.
func (c *Client) get(ctx context.Context, path string, out proto.Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out proto.Message) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent chain REST %s: %w", req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("agent chain REST %s: read body: %w", req.URL.Path, err)
	}
	if resp.StatusCode != http.StatusOK {
		// The gateway wraps gRPC errors as {"code":n,"message":"..."}.
		var ge struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &ge) == nil && ge.Message != "" {
			return fmt.Errorf("agent chain REST %s: %s (HTTP %d)", req.URL.Path, ge.Message, resp.StatusCode)
		}
		return fmt.Errorf("agent chain REST %s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	if err := c.cdc.UnmarshalJSON(body, out); err != nil {
		return fmt.Errorf("agent chain REST %s: decode response: %w", req.URL.Path, err)
	}
	return nil
}

// limitQuery renders the only pagination field the services use.
func limitQuery(p interface{ GetLimit() uint64 }) string {
	if p == nil {
		return ""
	}
	if l := p.GetLimit(); l > 0 {
		return fmt.Sprintf("?pagination.limit=%d", l)
	}
	return ""
}

// rootID renders a 32-byte delegation root id the way the gateway's bytes
// path binding expects (base64; URL-safe keeps it path-clean).
func rootID(b []byte) string {
	return base64.URLEncoding.EncodeToString(b)
}

// -- agentchain.AgentQuerier ---------------------------------------------

func (c *Client) Agent(ctx context.Context, in *agenttypes.QueryAgent, _ ...grpc.CallOption) (*agenttypes.QueryAgentResponse, error) {
	out := &agenttypes.QueryAgentResponse{}
	return out, c.get(ctx, "/dydxprotocol/agent/"+url.PathEscape(in.AgentId), out)
}

func (c *Client) AgentByOperator(ctx context.Context, in *agenttypes.QueryAgentByOperator, _ ...grpc.CallOption) (*agenttypes.QueryAgentByOperatorResponse, error) {
	out := &agenttypes.QueryAgentByOperatorResponse{}
	return out, c.get(ctx, "/dydxprotocol/agent/agent_by_operator/"+url.PathEscape(in.Operator), out)
}

func (c *Client) AllAgents(ctx context.Context, in *agenttypes.QueryAllAgents, _ ...grpc.CallOption) (*agenttypes.QueryAllAgentsResponse, error) {
	out := &agenttypes.QueryAllAgentsResponse{}
	return out, c.get(ctx, "/dydxprotocol/agent/agents"+limitQuery(in.Pagination), out)
}

func (c *Client) AgentsByOwner(ctx context.Context, in *agenttypes.QueryAgentsByOwner, _ ...grpc.CallOption) (*agenttypes.QueryAgentsByOwnerResponse, error) {
	out := &agenttypes.QueryAgentsByOwnerResponse{}
	return out, c.get(ctx, "/dydxprotocol/agent/agents_by_owner/"+url.PathEscape(in.Owner)+limitQuery(in.Pagination), out)
}

func (c *Client) AgentsByCapability(ctx context.Context, in *agenttypes.QueryAgentsByCapability, _ ...grpc.CallOption) (*agenttypes.QueryAgentsByCapabilityResponse, error) {
	out := &agenttypes.QueryAgentsByCapabilityResponse{}
	return out, c.get(ctx, "/dydxprotocol/agent/agents_by_capability/"+url.PathEscape(in.Capability)+limitQuery(in.Pagination), out)
}

func (c *Client) Params(ctx context.Context, _ *agenttypes.QueryParams, _ ...grpc.CallOption) (*agenttypes.QueryParamsResponse, error) {
	out := &agenttypes.QueryParamsResponse{}
	return out, c.get(ctx, "/dydxprotocol/agent/params", out)
}

// -- agentchain.WalletQuerier --------------------------------------------
//
// WalletParams keeps the method set disjoint from the x/agent Params above;
// Wallet() exposes it under the QueryClient-shaped name.

func (c *Client) Delegation(ctx context.Context, in *wallettypes.QueryDelegation, _ ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error) {
	out := &wallettypes.QueryDelegationResponse{}
	return out, c.get(ctx, "/dydxprotocol/agentwallet/delegation/"+rootID(in.RootId), out)
}

func (c *Client) DelegationsByDelegator(ctx context.Context, in *wallettypes.QueryDelegationsByDelegator, _ ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	out := &wallettypes.QueryDelegationsByDelegatorResponse{}
	return out, c.get(ctx, "/dydxprotocol/agentwallet/delegations_by_delegator/"+url.PathEscape(in.Delegator)+limitQuery(in.Pagination), out)
}

func (c *Client) Epoch(ctx context.Context, in *wallettypes.QueryEpoch, _ ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error) {
	out := &wallettypes.QueryEpochResponse{}
	return out, c.get(ctx, "/dydxprotocol/agentwallet/epoch/"+rootID(in.RootId), out)
}

func (c *Client) Spend(ctx context.Context, in *wallettypes.QuerySpend, _ ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error) {
	out := &wallettypes.QuerySpendResponse{}
	return out, c.get(ctx, "/dydxprotocol/agentwallet/spend/"+rootID(in.RootId), out)
}

func (c *Client) WalletParams(ctx context.Context, _ *wallettypes.QueryParams, _ ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error) {
	out := &wallettypes.QueryParamsResponse{}
	return out, c.get(ctx, "/dydxprotocol/agentwallet/params", out)
}

// walletAdapter renames WalletParams back to the Params the WalletQuerier
// interface expects (one type cannot carry both Params signatures).
type walletAdapter struct{ c *Client }

func (w walletAdapter) Delegation(ctx context.Context, in *wallettypes.QueryDelegation, opts ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error) {
	return w.c.Delegation(ctx, in, opts...)
}
func (w walletAdapter) DelegationsByDelegator(ctx context.Context, in *wallettypes.QueryDelegationsByDelegator, opts ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	return w.c.DelegationsByDelegator(ctx, in, opts...)
}
func (w walletAdapter) Epoch(ctx context.Context, in *wallettypes.QueryEpoch, opts ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error) {
	return w.c.Epoch(ctx, in, opts...)
}
func (w walletAdapter) Spend(ctx context.Context, in *wallettypes.QuerySpend, opts ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error) {
	return w.c.Spend(ctx, in, opts...)
}
func (w walletAdapter) Params(ctx context.Context, in *wallettypes.QueryParams, opts ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error) {
	return w.c.WalletParams(ctx, in, opts...)
}

// Wallet returns the x/agentwallet query surface (agentchain.WalletQuerier).
func (c *Client) Wallet() walletAdapter { return walletAdapter{c} }

// -- x/auth ---------------------------------------------------------------

// Account implements delegated.AuthAccountQuerier over
// GET /cosmos/auth/v1beta1/accounts/{address}.
func (c *Client) Account(ctx context.Context, in *authtypes.QueryAccountRequest, _ ...grpc.CallOption) (*authtypes.QueryAccountResponse, error) {
	out := &authtypes.QueryAccountResponse{}
	return out, c.get(ctx, "/cosmos/auth/v1beta1/accounts/"+url.PathEscape(in.Address), out)
}

// accountAdapter adapts Client to chain.AccountClient (whose Account method
// has a different signature than the auth querier's above).
type accountAdapter struct{ c *Client }

func (a accountAdapter) Account(ctx context.Context, address string) (chain.AccountInfo, error) {
	resp, err := a.c.Account(ctx, &authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return chain.AccountInfo{}, err
	}
	var acc sdk.AccountI
	if err := a.c.registry.UnpackAny(resp.Account, &acc); err != nil {
		return chain.AccountInfo{}, fmt.Errorf("unpack account %s: %w", address, err)
	}
	return chain.AccountInfo{AccountNumber: acc.GetAccountNumber(), Sequence: acc.GetSequence()}, nil
}

// AccountClient returns the account-info view (chain.AccountClient).
func (c *Client) AccountClient() chain.AccountClient { return accountAdapter{c} }

// -- chain.BroadcastClient --------------------------------------------------

// BroadcastSync submits pre-signed tx bytes via
// POST /cosmos/tx/v1beta1/txs with BROADCAST_MODE_SYNC, mirroring the gRPC
// BroadcastClient contract.
func (c *Client) BroadcastSync(ctx context.Context, txBytes []byte) (chain.BroadcastResult, error) {
	reqBody, err := json.Marshal(map[string]string{
		"tx_bytes": base64.StdEncoding.EncodeToString(txBytes),
		"mode":     "BROADCAST_MODE_SYNC",
	})
	if err != nil {
		return chain.BroadcastResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/cosmos/tx/v1beta1/txs", bytes.NewReader(reqBody))
	if err != nil {
		return chain.BroadcastResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	out := &sdktx.BroadcastTxResponse{}
	if err := c.do(req, out); err != nil {
		return chain.BroadcastResult{}, err
	}
	if out.TxResponse == nil {
		return chain.BroadcastResult{}, fmt.Errorf("agent chain REST broadcast: empty tx_response")
	}
	return chain.BroadcastResult{
		TxHash: out.TxResponse.TxHash,
		Code:   out.TxResponse.Code,
		RawLog: out.TxResponse.RawLog,
	}, nil
}
