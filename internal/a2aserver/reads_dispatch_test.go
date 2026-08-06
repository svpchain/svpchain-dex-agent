package a2aserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"google.golang.org/grpc"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	"github.com/svpchain/svpdt"

	"github.com/svpchain/svpchain-dex-agent/internal/delegated"
	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-dex-agent/internal/toolbridge"
	"github.com/svpchain/svpchain-mcp/lib/mcp/auth"
	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/signer"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// readAgentQ serves one registered agent's public key to the proof resolver.
type readAgentQ struct {
	agentID string
	pub     []byte
}

func (q readAgentQ) Agent(_ context.Context, in *agenttypes.QueryAgent, _ ...grpc.CallOption) (*agenttypes.QueryAgentResponse, error) {
	if in.AgentId != q.agentID {
		return &agenttypes.QueryAgentResponse{}, nil
	}
	return &agenttypes.QueryAgentResponse{Agent: agenttypes.Agent{AgentId: q.agentID, PublicKey: q.pub}}, nil
}
func (readAgentQ) AgentByOperator(context.Context, *agenttypes.QueryAgentByOperator, ...grpc.CallOption) (*agenttypes.QueryAgentByOperatorResponse, error) {
	return &agenttypes.QueryAgentByOperatorResponse{}, nil
}
func (readAgentQ) AllAgents(context.Context, *agenttypes.QueryAllAgents, ...grpc.CallOption) (*agenttypes.QueryAllAgentsResponse, error) {
	return &agenttypes.QueryAllAgentsResponse{}, nil
}
func (readAgentQ) AgentsByOwner(context.Context, *agenttypes.QueryAgentsByOwner, ...grpc.CallOption) (*agenttypes.QueryAgentsByOwnerResponse, error) {
	return &agenttypes.QueryAgentsByOwnerResponse{}, nil
}
func (readAgentQ) AgentsByCapability(context.Context, *agenttypes.QueryAgentsByCapability, ...grpc.CallOption) (*agenttypes.QueryAgentsByCapabilityResponse, error) {
	return &agenttypes.QueryAgentsByCapabilityResponse{}, nil
}
func (readAgentQ) Params(context.Context, *agenttypes.QueryParams, ...grpc.CallOption) (*agenttypes.QueryParamsResponse, error) {
	return &agenttypes.QueryParamsResponse{}, nil
}

// readWalletQ answers with a live root at epoch 1 and the default ceilings.
type readWalletQ struct{}

func (readWalletQ) Delegation(context.Context, *wallettypes.QueryDelegation, ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error) {
	return &wallettypes.QueryDelegationResponse{}, nil
}
func (readWalletQ) DelegationsByDelegator(context.Context, *wallettypes.QueryDelegationsByDelegator, ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	return &wallettypes.QueryDelegationsByDelegatorResponse{}, nil
}
func (readWalletQ) Epoch(context.Context, *wallettypes.QueryEpoch, ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error) {
	return &wallettypes.QueryEpochResponse{Epoch: 1}, nil
}
func (readWalletQ) Spend(context.Context, *wallettypes.QuerySpend, ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error) {
	return &wallettypes.QuerySpendResponse{}, nil
}
func (readWalletQ) Params(context.Context, *wallettypes.QueryParams, ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error) {
	return &wallettypes.QueryParamsResponse{Params: wallettypes.Params{
		MaxDelegationDepth: 4,
		MaxTokenTtlSeconds: 900,
	}}, nil
}

// multiSource mirrors the wire-level fan-out: bearer tenants first, then
// proof-derived read tenants.
type multiSource []policy.DynamicSource

func (m multiSource) LookupTenantPolicy(tenantID string) (policy.TenantPolicy, bool) {
	for _, src := range m {
		if tp, ok := src.LookupTenantPolicy(tenantID); ok {
			return tp, true
		}
	}
	return policy.TenantPolicy{}, false
}

type readStack struct {
	exec      *Executor
	tenants   *auth.DynamicTenantStore
	principal string
	issue     func(t *testing.T, actions ...string) []string
}

// newDelegatedReadStack wires an executor whose account tools answer from a
// fake indexer, with delegated reads live: an operator-keyed service, the
// read-tenant source registered as a policy dynamic source, and a token
// issuer bound to the stack's agent identity.
func newDelegatedReadStack(t *testing.T) *readStack {
	t.Helper()
	const chainID = "svp-test-1"

	// Fake Comlink: any subaccount fetch answers with the requested address.
	fakeIndexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) < 4 || parts[2] != "addresses" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"address": parts[3], "subaccountNumber": 0})
	}))
	t.Cleanup(fakeIndexer.Close)

	bz := make([]byte, 32)
	if _, err := rand.Read(bz); err != nil {
		t.Fatal(err)
	}
	priv := &ethsecp256k1.PrivKey{Key: bz}
	operatorAddr := signer.DeriveAddress(priv)
	agentID := agenttypes.AgentIdFromOperator(sdk.MustAccAddressFromBech32(operatorAddr))
	dtSigner, err := svpdt.NewPrivateKeySigner(bz)
	if err != nil {
		t.Fatal(err)
	}

	userBz := make([]byte, 32)
	if _, err := rand.Read(userBz); err != nil {
		t.Fatal(err)
	}
	principal := signer.DeriveAddress(&ethsecp256k1.PrivKey{Key: userBz})

	svc := delegated.New(delegated.Config{
		Priv:     priv,
		Operator: operatorAddr,
		ChainID:  chainID,
		AgentQ:   readAgentQ{agentID: agentID, pub: priv.PubKey().Bytes()},
		WalletQ:  readWalletQ{},
	})
	readTenants := delegated.NewReadTenantSource(nil)

	tenants := auth.NewDynamicTenantStore(auth.DynamicTenantStoreConfig{
		BearerTTL:                 auth.DefaultBearerTTL,
		DefaultAllowedSubaccounts: []uint32{0, 1},
	}, nil)
	sessions := auth.NewSessionBearers(auth.DefaultBearerTTL, nil)
	engine := policy.NewEngine(nil)
	engine.SetDynamicSource(multiSource{tenantAdapter{store: tenants}, readTenants})

	h := tools.New(chainID, tools.Deps{
		Indexer:          indexer.NewClient(fakeIndexer.URL, indexer.Options{}),
		NonceStore:       auth.NewNonceStore(auth.DefaultChallengeTTL, nil),
		DynamicTenants:   tenants,
		IPChallengeLimit: auth.NewIPRateLimiter(100, time.Minute, nil),
		SessionBearers:   sessions,
		Policy:           engine,
		RateLimit:        policy.NewRateLimiter(0, 0),
		BroadcastMode:    "server",
	})
	reg := toolbridge.New(h)
	reg.RegisterExecution(nil)

	exec := NewFullExecutor(
		marketdata.NewService(fakeReader{}),
		reg,
		&AuthResolver{Tenants: tenants, Sessions: sessions},
		svc,
		readTenants,
	)

	issue := func(t *testing.T, actions ...string) []string {
		t.Helper()
		now := time.Now().Unix()
		_, encoded, err := svpdt.Issue(dtSigner, svpdt.IssueParams{
			ChainID:   chainID,
			Root:      [32]byte{0xAA},
			RootEpoch: 1,
			Issuer:    agentID,
			Audience:  agentID,
			Caveats: svpdt.Caveats{
				Principal:   principal,
				Actions:     svpdt.StringSet(actions),
				Subaccounts: svpdt.Uint32Set{0},
				MaxDepth:    1,
				NotBefore:   now - 60,
				Expires:     now + 300,
			},
			Nonce: [16]byte{0x01},
		})
		if err != nil {
			t.Fatal(err)
		}
		return []string{base64.StdEncoding.EncodeToString(encoded)}
	}

	return &readStack{exec: exec, tenants: tenants, principal: principal, issue: issue}
}

// A query.account credential authenticates a covered read with no bearer at
// all, and the answer is scoped to the credential's principal.
func TestDelegatedReadWithoutBearer(t *testing.T) {
	s := newDelegatedReadStack(t)
	meta := map[string]any{"tokens": s.issue(t, delegated.ActionQueryAccount)}

	ec := execCtxWithDelegation(`{"skill":"svpchain-account","tool":"get_subaccount","args":{}}`, meta)
	resp := dispatch(t, s.exec, ec)
	if !resp.OK {
		t.Fatalf("delegated read refused: %s", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	sub, _ := result["subaccount"].(map[string]any)
	if addr, _ := sub["address"].(string); addr != s.principal {
		t.Errorf("read answered for %q, want the credential principal %q", addr, s.principal)
	}
}

// A credential without query.account grants no reads, even a valid one.
func TestDelegatedReadRequiresQueryAction(t *testing.T) {
	s := newDelegatedReadStack(t)
	meta := map[string]any{"tokens": s.issue(t, "clob.place_order")}

	ec := execCtxWithDelegation(`{"skill":"svpchain-account","tool":"get_subaccount","args":{}}`, meta)
	if _, err := s.exec.handle(context.Background(), ec); err == nil ||
		!strings.Contains(err.Error(), "does not grant action") {
		t.Errorf("want action refusal, got %v", err)
	}
}

// When a bearer and a proof both arrive, the bearer's tenant wins.
func TestBearerTakesPrecedenceOverProof(t *testing.T) {
	s := newDelegatedReadStack(t)
	bearer, _, _, err := s.tenants.Mint("svp1headerowner")
	if err != nil {
		t.Fatal(err)
	}

	// The proof's principal differs from the bearer's owner. Reading the
	// bearer owner's subaccount succeeds only if the bearer won — under the
	// proof it would refuse with a principal mismatch.
	meta := map[string]any{"tokens": s.issue(t, delegated.ActionQueryAccount)}
	ec := execCtxWithDelegation(
		`{"skill":"svpchain-account","tool":"get_subaccount","args":{"address":"svp1headerowner"}}`, meta)
	ec.ServiceParams = a2asrv.NewServiceParams(map[string][]string{
		"Authorization": {"Bearer " + bearer},
	})
	resp := dispatch(t, s.exec, ec)
	if !resp.OK {
		t.Fatalf("bearer-authenticated read refused: %s", resp.Error)
	}
	result, _ := resp.Result.(map[string]any)
	sub, _ := result["subaccount"].(map[string]any)
	if addr, _ := sub["address"].(string); addr != "svp1headerowner" {
		t.Errorf("read answered for %q, want the bearer owner", addr)
	}
}

// A proof on a tool outside the covered read set changes nothing: the call
// still needs a bearer and refuses with the auth handshake hint.
func TestProofDoesNotAuthenticateUncoveredTools(t *testing.T) {
	s := newDelegatedReadStack(t)
	meta := map[string]any{"tokens": s.issue(t, delegated.ActionQueryAccount)}

	ec := execCtxWithDelegation(`{"skill":"svpchain-account","tool":"whoami"}`, meta)
	resp := dispatch(t, s.exec, ec)
	if resp.OK {
		t.Fatal("whoami under a read credential must still refuse")
	}
	if !strings.Contains(resp.Error, "auth_challenge") {
		t.Errorf("refusal should point at the auth flow, got: %s", resp.Error)
	}
}
