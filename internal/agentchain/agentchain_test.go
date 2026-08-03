package agentchain

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/grpc"

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"

	// Sets the sdk bech32 prefix to "svp" via init() — required for
	// ValidateBasic on bech32 addresses.
	_ "github.com/dydxprotocol/v4-chain/protocol/app/config"
	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/builder"
	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// Real bech32 addresses (svp prefix) so ValidateBasic passes.
const (
	testOwner    = "svp199tqg4wdlnu4qjlxchpd7seg454937hjk505pe"
	testOperator = "svp199tqg4wdlnu4qjlxchpd7seg454937hjk505pe"
)

type fakeAgentQ struct {
	lastAgentID string
}

func (f *fakeAgentQ) Agent(_ context.Context, in *agenttypes.QueryAgent, _ ...grpc.CallOption) (*agenttypes.QueryAgentResponse, error) {
	f.lastAgentID = in.AgentId
	return &agenttypes.QueryAgentResponse{Agent: agenttypes.Agent{AgentId: in.AgentId}}, nil
}
func (f *fakeAgentQ) AgentByOperator(context.Context, *agenttypes.QueryAgentByOperator, ...grpc.CallOption) (*agenttypes.QueryAgentByOperatorResponse, error) {
	return &agenttypes.QueryAgentByOperatorResponse{}, nil
}
func (f *fakeAgentQ) AllAgents(context.Context, *agenttypes.QueryAllAgents, ...grpc.CallOption) (*agenttypes.QueryAllAgentsResponse, error) {
	return &agenttypes.QueryAllAgentsResponse{}, nil
}
func (f *fakeAgentQ) AgentsByOwner(context.Context, *agenttypes.QueryAgentsByOwner, ...grpc.CallOption) (*agenttypes.QueryAgentsByOwnerResponse, error) {
	return &agenttypes.QueryAgentsByOwnerResponse{}, nil
}
func (f *fakeAgentQ) AgentsByCapability(context.Context, *agenttypes.QueryAgentsByCapability, ...grpc.CallOption) (*agenttypes.QueryAgentsByCapabilityResponse, error) {
	return &agenttypes.QueryAgentsByCapabilityResponse{}, nil
}
func (f *fakeAgentQ) Params(context.Context, *agenttypes.QueryParams, ...grpc.CallOption) (*agenttypes.QueryParamsResponse, error) {
	return &agenttypes.QueryParamsResponse{}, nil
}

type fakeWalletQ struct {
	lastRootID []byte
}

func (f *fakeWalletQ) Delegation(_ context.Context, in *wallettypes.QueryDelegation, _ ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error) {
	f.lastRootID = in.RootId
	return &wallettypes.QueryDelegationResponse{}, nil
}
func (f *fakeWalletQ) DelegationsByDelegator(context.Context, *wallettypes.QueryDelegationsByDelegator, ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	return &wallettypes.QueryDelegationsByDelegatorResponse{}, nil
}
func (f *fakeWalletQ) Epoch(context.Context, *wallettypes.QueryEpoch, ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error) {
	return &wallettypes.QueryEpochResponse{Epoch: 7}, nil
}
func (f *fakeWalletQ) Spend(context.Context, *wallettypes.QuerySpend, ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error) {
	return &wallettypes.QuerySpendResponse{}, nil
}
func (f *fakeWalletQ) Params(context.Context, *wallettypes.QueryParams, ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error) {
	return &wallettypes.QueryParamsResponse{}, nil
}

type fakeAccount struct{}

func (fakeAccount) Account(context.Context, string) (chain.AccountInfo, error) {
	return chain.AccountInfo{AccountNumber: 7, Sequence: 42}, nil
}

func newTestService() (*Service, *fakeAgentQ, *fakeWalletQ) {
	aq := &fakeAgentQ{}
	wq := &fakeWalletQ{}
	engine := policy.NewEngine([]policy.TenantPolicy{{
		TenantID: "t1",
		Owner:    testOwner,
	}})
	asm := builder.NewAssembler("test-chain", "asvp", "25000000000000000", 1_000_000)
	return New(aq, wq, asm, fakeAccount{}, engine, nil, nil), aq, wq
}

func authedCtx() context.Context {
	return tools.WithTenant(context.Background(), tools.TenantContext{TenantID: "t1", Owner: testOwner})
}

func TestQueriesRequireATenant(t *testing.T) {
	s, _, _ := newTestService()
	if _, err := s.GetAgent(context.Background(), GetAgentInput{AgentID: "did:svp:x"}); !errors.Is(err, tools.ErrNoTenant) {
		t.Errorf("unauthenticated query must return ErrNoTenant, got %v", err)
	}
}

func TestQueriesForwardToTheChain(t *testing.T) {
	s, aq, wq := newTestService()
	ctx := authedCtx()

	if _, err := s.GetAgent(ctx, GetAgentInput{AgentID: "did:svp:abc"}); err != nil {
		t.Fatal(err)
	}
	if aq.lastAgentID != "did:svp:abc" {
		t.Errorf("agent id not forwarded, got %q", aq.lastAgentID)
	}

	rootID := strings.Repeat("ab", 32)
	if _, err := s.GetDelegation(ctx, RootIDInput{RootID: rootID}); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(wq.lastRootID) != rootID {
		t.Errorf("root id not decoded/forwarded, got %x", wq.lastRootID)
	}

	if _, err := s.GetDelegation(ctx, RootIDInput{RootID: "abcd"}); err == nil {
		t.Error("short root id must be rejected")
	}

	epoch, err := s.GetDelegationEpoch(ctx, RootIDInput{RootID: rootID})
	if err != nil {
		t.Fatal(err)
	}
	if epoch.Epoch != 7 {
		t.Errorf("epoch = %d", epoch.Epoch)
	}
}

// decodeSoleMsg pulls the single message out of a payload's TxBody.
func decodeSoleMsg(t *testing.T, b64, wantTypeURL string, msg proto.Message) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	var body txtypes.TxBody
	if err := proto.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(body.Messages))
	}
	if body.Messages[0].TypeUrl != wantTypeURL {
		t.Fatalf("type url = %s, want %s", body.Messages[0].TypeUrl, wantTypeURL)
	}
	if err := proto.Unmarshal(body.Messages[0].Value, msg); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDepositBondRoundTrips(t *testing.T) {
	s, _, _ := newTestService()
	out, err := s.BuildDepositBond(authedCtx(), BuildBondInput{
		AgentID:         "did:svp:" + testOperator,
		Amount:          Coin{Denom: "asvp", Amount: "1000"},
		PayloadClientID: "client-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg agenttypes.MsgDepositBond
	decodeSoleMsg(t, out.Payload.TxBodyBytesB64, "/dydxprotocol.agent.MsgDepositBond", &msg)
	// The signer is pinned to the tenant owner — the caller never chooses it.
	if msg.Owner != testOwner {
		t.Errorf("owner = %q, want tenant owner", msg.Owner)
	}
	if msg.Amount.Amount.String() != "1000" || msg.Amount.Denom != "asvp" {
		t.Errorf("amount = %s", msg.Amount)
	}
	if out.Payload.SignerAddress != testOwner {
		t.Errorf("payload signer = %q", out.Payload.SignerAddress)
	}
	// Non-CLOB msg: the configured fee must be stamped.
	if len(out.Payload.Fee.Amount) == 0 {
		t.Error("bond deposit must carry the configured fee")
	}
}

func TestBuildCreateDelegationRoundTrips(t *testing.T) {
	s, _, _ := newTestService()
	out, err := s.BuildCreateDelegation(authedCtx(), BuildCreateDelegationInput{
		AgentID: "did:svp:" + testOperator,
		Limits: Limits{
			SpendLimitTotal:    []Coin{{Denom: "uusdc", Amount: "1000000"}},
			Actions:            []string{"clob.place_order"},
			Subaccounts:        []uint32{0},
			MaxDepth:           2,
			MaxTokenTTLSeconds: 600,
		},
		ExpiresAt:       4102444800, // far future
		PayloadClientID: "client-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg wallettypes.MsgCreateDelegation
	decodeSoleMsg(t, out.Payload.TxBodyBytesB64, "/dydxprotocol.agentwallet.MsgCreateDelegation", &msg)
	if msg.Delegator != testOwner {
		t.Errorf("delegator = %q, want tenant owner", msg.Delegator)
	}
	if len(msg.Limits.Actions) != 1 || msg.Limits.Actions[0] != "clob.place_order" {
		t.Errorf("limits actions = %v", msg.Limits.Actions)
	}
	if msg.Limits.SpendLimitTotal[0].Amount.String() != "1000000" {
		t.Errorf("spend limit = %s", msg.Limits.SpendLimitTotal[0])
	}
}

func TestBuildRegisterAgentValidatesTheKeyOperatorBinding(t *testing.T) {
	s, _, _ := newTestService()
	// A garbage key cannot hash to the operator address, so the msg's own
	// ValidateBasic must reject it before assembly.
	junkKey := base64.StdEncoding.EncodeToString(make([]byte, 33))
	_, err := s.BuildRegisterAgent(authedCtx(), BuildRegisterAgentInput{
		Operator:        testOperator,
		PublicKeyB64:    junkKey,
		Endpoint:        "https://agent.example.com",
		InitialBond:     Coin{Denom: "asvp", Amount: "5000000000000000000000"},
		PayloadClientID: "client-3",
	})
	if err == nil {
		t.Fatal("register with a key that is not the operator's must fail ValidateBasic")
	}
}

func TestBuildRevokeTokenRoundTrips(t *testing.T) {
	s, _, _ := newTestService()
	rootID := strings.Repeat("cd", 32)
	tokenHash := strings.Repeat("ef", 32)
	out, err := s.BuildRevokeToken(authedCtx(), BuildRevokeTokenInput{
		RootID:          rootID,
		TokenHash:       tokenHash,
		PayloadClientID: "client-4",
	})
	if err != nil {
		t.Fatal(err)
	}
	var msg wallettypes.MsgRevokeToken
	decodeSoleMsg(t, out.Payload.TxBodyBytesB64, "/dydxprotocol.agentwallet.MsgRevokeToken", &msg)
	if hex.EncodeToString(msg.RootId) != rootID || hex.EncodeToString(msg.TokenHash) != tokenHash {
		t.Errorf("root/token hash mangled: %x / %x", msg.RootId, msg.TokenHash)
	}
}
