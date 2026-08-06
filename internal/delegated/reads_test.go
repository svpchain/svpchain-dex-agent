package delegated

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	"github.com/svpchain/svpdt"
)

// stubWalletQ answers the epoch query with a configurable state and counts
// calls, so the cache TTL is observable. Params serves the same ceilings as
// the shared fixture's fakeWalletQ.
type stubWalletQ struct {
	epoch      uint64
	paused     bool
	err        error
	epochCalls int
}

func (s *stubWalletQ) Delegation(context.Context, *wallettypes.QueryDelegation, ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error) {
	return &wallettypes.QueryDelegationResponse{}, nil
}
func (s *stubWalletQ) DelegationsByDelegator(context.Context, *wallettypes.QueryDelegationsByDelegator, ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	return &wallettypes.QueryDelegationsByDelegatorResponse{}, nil
}
func (s *stubWalletQ) Epoch(context.Context, *wallettypes.QueryEpoch, ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error) {
	s.epochCalls++
	if s.err != nil {
		return nil, s.err
	}
	return &wallettypes.QueryEpochResponse{Epoch: s.epoch, Paused: s.paused}, nil
}
func (s *stubWalletQ) Spend(context.Context, *wallettypes.QuerySpend, ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error) {
	return &wallettypes.QuerySpendResponse{}, nil
}
func (s *stubWalletQ) Params(context.Context, *wallettypes.QueryParams, ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error) {
	return &wallettypes.QueryParamsResponse{Params: wallettypes.Params{
		MaxDelegationDepth: 4,
		MaxTokenTtlSeconds: 900,
	}}, nil
}

// readFixture is the shared fixture with a controllable wallet querier, and
// tokens granting query.account instead of the execute actions.
func readFixture(t *testing.T) (*fixture, *stubWalletQ) {
	t.Helper()
	f := newFixture(t)
	wq := &stubWalletQ{epoch: 1} // matches the fixture tokens' RootEpoch
	f.svc.cfg.WalletQ = wq
	return f, wq
}

func (f *fixture) issueRead(t *testing.T, mutate func(*svpdt.IssueParams)) []string {
	t.Helper()
	return f.issue(t, func(p *svpdt.IssueParams) {
		p.Caveats.Actions = svpdt.StringSet{ActionQueryAccount}
		p.Caveats.Budget = nil
		if mutate != nil {
			mutate(p)
		}
	})
}

func TestAuthorizeReadPinsArgsToPrincipal(t *testing.T) {
	f, _ := readFixture(t)
	proof := f.issueRead(t, nil)

	// Absent owner defaults to the principal.
	verified, args, err := f.svc.AuthorizeRead(context.Background(), "get_subaccount", proof, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Address          string `json:"address"`
		SubaccountNumber uint32 `json:"subaccount_number"`
	}
	if err := json.Unmarshal(args, &got); err != nil {
		t.Fatal(err)
	}
	if got.Address != testDelegator || got.SubaccountNumber != 0 {
		t.Errorf("args = %+v, want address defaulted to the principal", got)
	}
	if verified.Principal != testDelegator {
		t.Errorf("principal = %q", verified.Principal)
	}

	// An explicit owner naming the principal passes unchanged.
	_, args, err = f.svc.AuthorizeRead(context.Background(), "get_balance", proof,
		json.RawMessage(`{"owner":"`+testDelegator+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), testDelegator) {
		t.Errorf("owner dropped: %s", args)
	}

	// The live-chain read is covered under the same credential, pinned the
	// same way through its "owner" field.
	_, args, err = f.svc.AuthorizeRead(context.Background(), "get_live_subaccount", proof,
		json.RawMessage(`{"subaccount_number":0}`))
	if err != nil {
		t.Fatal(err)
	}
	var live struct {
		Owner            string `json:"owner"`
		SubaccountNumber uint32 `json:"subaccount_number"`
	}
	if err := json.Unmarshal(args, &live); err != nil {
		t.Fatal(err)
	}
	if live.Owner != testDelegator || live.SubaccountNumber != 0 {
		t.Errorf("live args = %+v, want owner defaulted to the principal", live)
	}

	// The verified grant admits as a tenant the policy engine resolves.
	src := NewReadTenantSource(func() int64 { return testNow })
	tc := src.Admit(verified)
	if tc.Owner != testDelegator || !strings.HasPrefix(tc.TenantID, "svpdt-") {
		t.Errorf("tenant context = %+v", tc)
	}
	tp, ok := src.LookupTenantPolicy(tc.TenantID)
	if !ok || tp.Owner != testDelegator {
		t.Fatalf("tenant policy = %+v, ok=%v", tp, ok)
	}
	if _, allowed := tp.AllowedSubaccounts[0]; !allowed {
		t.Error("subaccount 0 missing from the synthesized tenant")
	}
	if _, allowed := tp.AllowedSubaccounts[3]; allowed {
		t.Error("subaccount 3 must not be granted")
	}
	// Re-admitting the same credential reuses the tenant id.
	if tc2 := src.Admit(verified); tc2.TenantID != tc.TenantID {
		t.Errorf("re-admission changed tenant id: %s vs %s", tc2.TenantID, tc.TenantID)
	}
	// The tenant dies with the leaf token.
	late := NewReadTenantSource(func() int64 { return verified.LeafExp })
	tcLate := late.Admit(verified)
	if _, ok := late.LookupTenantPolicy(tcLate.TenantID); ok {
		t.Error("expired read tenant must not resolve")
	}
}

func TestAuthorizeReadRefusals(t *testing.T) {
	f, wq := readFixture(t)

	cases := map[string]struct {
		tool    string
		proof   func(t *testing.T) []string
		args    json.RawMessage
		mutate  func()
		wantErr string
	}{
		"uncovered tool": {
			tool:    "get_order",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			wantErr: "does not accept delegated-read credentials",
		},
		"orders subaccount not granted": {
			tool:    "get_orders",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			args:    json.RawMessage(`{"subaccount_number":3}`),
			wantErr: "does not grant subaccount",
		},
		"action not granted": {
			tool:    "get_balance",
			proof:   func(t *testing.T) []string { return f.issue(t, nil) }, // execute actions only
			wantErr: "does not grant action",
		},
		"wrong audience": {
			tool: "get_balance",
			proof: func(t *testing.T) []string {
				return f.issueRead(t, func(p *svpdt.IssueParams) { p.Audience = "did:svp:someoneelse" })
			},
			wantErr: "credential chain rejected",
		},
		"expired": {
			tool: "get_balance",
			proof: func(t *testing.T) []string {
				return f.issueRead(t, func(p *svpdt.IssueParams) {
					p.Caveats.NotBefore = testNow - 600
					p.Caveats.Expires = testNow - 300
				})
			},
			wantErr: "credential chain rejected",
		},
		"owner mismatch": {
			tool:    "get_balance",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			args:    json.RawMessage(`{"owner":"svp1someoneelse"}`),
			wantErr: "cannot read",
		},
		"subaccount not granted": {
			tool:    "get_subaccount",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			args:    json.RawMessage(`{"subaccount_number":3}`),
			wantErr: "does not grant subaccount",
		},
		"live subaccount not granted": {
			tool:    "get_live_subaccount",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			args:    json.RawMessage(`{"subaccount_number":3}`),
			wantErr: "does not grant subaccount",
		},
		"paused root": {
			tool:    "get_balance",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			mutate:  func() { wq.paused = true },
			wantErr: "paused",
		},
		"stale epoch": {
			tool:    "get_balance",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			mutate:  func() { wq.epoch = 2 },
			wantErr: "epoch",
		},
		"unknown root": {
			tool:    "get_balance",
			proof:   func(t *testing.T) []string { return f.issueRead(t, nil) },
			mutate:  func() { wq.err = fmt.Errorf("delegation not found") },
			wantErr: "root delegation state unavailable",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wq.epoch, wq.paused, wq.err = 1, false, nil
			f.svc.epochCache = map[[32]byte]epochEntry{} // isolate the chain-state cases
			if tc.mutate != nil {
				tc.mutate()
			}
			_, _, err := f.svc.AuthorizeRead(context.Background(), tc.tool, tc.proof(t), tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestAuthorizeReadCachesRootState(t *testing.T) {
	f, wq := readFixture(t)
	proof := f.issueRead(t, nil)

	call := func() {
		t.Helper()
		if _, _, err := f.svc.AuthorizeRead(context.Background(), "get_balance", proof, nil); err != nil {
			t.Fatal(err)
		}
	}

	call()
	call()
	if wq.epochCalls != 1 {
		t.Fatalf("second read within the TTL re-queried the chain: %d calls", wq.epochCalls)
	}

	// Past the TTL the heartbeat re-queries — this is the revocation bound.
	now := testNow
	f.svc.now = func() int64 { return now }
	now += f.svc.epochCacheTTL + 1
	call()
	if wq.epochCalls != 2 {
		t.Fatalf("read past the TTL served stale root state: %d calls", wq.epochCalls)
	}
}

// multiAgentQ resolves several DIDs, so chains with an intermediary issuer
// verify. Only Agent is consulted by the resolver's registry path.
type multiAgentQ struct {
	fakeAgentQ
	keys map[string][]byte
}

func (m *multiAgentQ) Agent(_ context.Context, in *agenttypes.QueryAgent, _ ...grpc.CallOption) (*agenttypes.QueryAgentResponse, error) {
	pk, ok := m.keys[in.AgentId]
	if !ok {
		return &agenttypes.QueryAgentResponse{}, nil
	}
	return &agenttypes.QueryAgentResponse{Agent: agenttypes.Agent{AgentId: in.AgentId, PublicKey: pk}}, nil
}

// attenuateTo hand-builds a depth-2 child of parent addressed to audience —
// deliberately NOT via svpdt.Attenuate, which is the cooperative path a
// hostile intermediary skips.
func attenuateTo(t *testing.T, parentB64 string, signer *svpdt.PrivateKeySigner, issuer, audience string, caveats svpdt.Caveats) string {
	t.Helper()
	parentRaw, err := base64.StdEncoding.DecodeString(parentB64)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := svpdt.UnmarshalToken(parentRaw)
	if err != nil {
		t.Fatal(err)
	}
	p := svpdt.Payload{
		Version:   svpdt.Version,
		ChainID:   parent.Payload.ChainID,
		Root:      parent.Payload.Root,
		RootEpoch: parent.Payload.RootEpoch,
		Issuer:    issuer,
		Audience:  audience,
		Proof:     sha256.Sum256(parentRaw),
		Depth:     2,
		Caveats:   caveats,
		Nonce:     [16]byte{0x02},
	}
	digest, err := p.SigningPreimage()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.Sign(digest)
	if err != nil {
		t.Fatal(err)
	}
	tok := svpdt.Token{Payload: p, Sig: sig}
	raw, err := tok.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// verifyProof must refuse a chain whose leaf audience is outside the parent's
// redelegate_to — svpdt.VerifyChain alone accepts it (only the cooperative
// Attenuate helper enforces the target list), which is exactly the gap
// checkRedelegationTargets closes.
func TestVerifyProofEnforcesRedelegationTargets(t *testing.T) {
	f, _ := readFixture(t)

	interKey := make([]byte, 32)
	if _, err := rand.Read(interKey); err != nil {
		t.Fatal(err)
	}
	interSigner, err := svpdt.NewPrivateKeySigner(interKey)
	if err != nil {
		t.Fatal(err)
	}
	const interDID = "did:svp:intermediary"
	f.svc.resolver = NewGRPCResolver(&multiAgentQ{keys: map[string][]byte{
		f.agentID: f.svc.cfg.Priv.PubKey().Bytes(),
		interDID:  interSigner.PublicKey(),
	}}, nil)

	childCaveats := svpdt.Caveats{
		Principal:   testDelegator,
		Actions:     svpdt.StringSet{ActionQueryAccount},
		Subaccounts: svpdt.Uint32Set{0},
		MaxDepth:    2,
		NotBefore:   testNow - 30,
		Expires:     testNow + 200,
	}
	childCaveats.RedelegateTo, err = svpdt.ConstrainedTo()
	if err != nil {
		t.Fatal(err)
	}

	parentFor := func(t *testing.T, redelegateTo ...string) []string {
		t.Helper()
		return f.issueRead(t, func(p *svpdt.IssueParams) {
			p.Audience = interDID
			p.Caveats.Redelegable = true
			rt, err := svpdt.ConstrainedTo(redelegateTo...)
			if err != nil {
				t.Fatal(err)
			}
			p.Caveats.RedelegateTo = rt
		})
	}

	t.Run("out-of-list leaf audience refused", func(t *testing.T) {
		parent := parentFor(t, "did:svp:someoneelse")
		chain := []string{parent[0], attenuateTo(t, parent[0], interSigner, interDID, f.agentID, childCaveats)}

		// Document the library gap: VerifyChain alone accepts this chain.
		raw := make([][]byte, len(chain))
		for i, tk := range chain {
			raw[i], _ = base64.StdEncoding.DecodeString(tk)
		}
		if _, err := svpdt.VerifyChain(raw, svpdt.SingleKeyResolver(map[string][]byte{
			f.agentID: f.svc.cfg.Priv.PubKey().Bytes(),
			interDID:  interSigner.PublicKey(),
		}), svpdt.VerifyOpts{ChainID: testChainID, Now: testNow, MaxDepth: 4, MaxTTLSeconds: 900, Audience: f.agentID}); err != nil {
			t.Fatalf("VerifyChain was expected to accept (documenting the gap), got %v", err)
		}

		if _, _, err := f.svc.verifyProof(context.Background(), chain); err == nil ||
			!strings.Contains(err.Error(), "redelegate_to") {
			t.Errorf("want redelegate_to refusal, got %v", err)
		}
	})

	t.Run("in-list chain authorizes a read end to end", func(t *testing.T) {
		parent := parentFor(t, f.agentID)
		chain := []string{parent[0], attenuateTo(t, parent[0], interSigner, interDID, f.agentID, childCaveats)}
		verified, args, err := f.svc.AuthorizeRead(context.Background(), "get_balance", chain, nil)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Principal != testDelegator || !strings.Contains(string(args), testDelegator) {
			t.Errorf("principal = %q, args = %s", verified.Principal, args)
		}
	})
}
