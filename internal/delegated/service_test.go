package delegated

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"github.com/cosmos/gogoproto/proto"
	"google.golang.org/grpc"

	"cosmossdk.io/log"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	clobtypes "github.com/dydxprotocol/v4-chain/protocol/x/clob/types"
	perptypes "github.com/dydxprotocol/v4-chain/protocol/x/perpetuals/types"
	"github.com/svpchain/svpdt"

	"github.com/svpchain/svpchain-dex-agent/internal/operator"
	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/markets"
	"github.com/svpchain/svpchain-mcp/lib/mcp/payload"
	"github.com/svpchain/svpchain-mcp/lib/mcp/signer"
)

const (
	testChainID   = "svp-delegated-test"
	testNow       = int64(1_700_000_000)
	testDelegator = "svp199tqg4wdlnu4qjlxchpd7seg454937hjk505pe"
)

// fakeAgentQ serves one registered agent: ours.
type fakeAgentQ struct {
	agentID string
	pubKey  []byte
}

func (f *fakeAgentQ) Agent(_ context.Context, in *agenttypes.QueryAgent, _ ...grpc.CallOption) (*agenttypes.QueryAgentResponse, error) {
	if in.AgentId != f.agentID {
		return &agenttypes.QueryAgentResponse{}, nil
	}
	return &agenttypes.QueryAgentResponse{Agent: agenttypes.Agent{AgentId: f.agentID, PublicKey: f.pubKey}}, nil
}
func (f *fakeAgentQ) AgentByOperator(context.Context, *agenttypes.QueryAgentByOperator, ...grpc.CallOption) (*agenttypes.QueryAgentByOperatorResponse, error) {
	return &agenttypes.QueryAgentByOperatorResponse{
		Agent: agenttypes.Agent{AgentId: f.agentID, Status: agenttypes.AgentStatus_AGENT_STATUS_ACTIVE},
	}, nil
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

type fakeWalletQ struct{}

func (fakeWalletQ) Delegation(context.Context, *wallettypes.QueryDelegation, ...grpc.CallOption) (*wallettypes.QueryDelegationResponse, error) {
	return &wallettypes.QueryDelegationResponse{}, nil
}
func (fakeWalletQ) DelegationsByDelegator(context.Context, *wallettypes.QueryDelegationsByDelegator, ...grpc.CallOption) (*wallettypes.QueryDelegationsByDelegatorResponse, error) {
	return &wallettypes.QueryDelegationsByDelegatorResponse{}, nil
}
func (fakeWalletQ) Epoch(context.Context, *wallettypes.QueryEpoch, ...grpc.CallOption) (*wallettypes.QueryEpochResponse, error) {
	return &wallettypes.QueryEpochResponse{}, nil
}
func (fakeWalletQ) Spend(context.Context, *wallettypes.QuerySpend, ...grpc.CallOption) (*wallettypes.QuerySpendResponse, error) {
	return &wallettypes.QuerySpendResponse{}, nil
}
func (fakeWalletQ) Params(context.Context, *wallettypes.QueryParams, ...grpc.CallOption) (*wallettypes.QueryParamsResponse, error) {
	return &wallettypes.QueryParamsResponse{Params: wallettypes.Params{
		MaxDelegationDepth: 4,
		MaxTokenTtlSeconds: 900,
	}}, nil
}

type fakeAccount struct{}

func (fakeAccount) Account(context.Context, string) (chain.AccountInfo, error) {
	return chain.AccountInfo{AccountNumber: 11, Sequence: 5}, nil
}

type captureBroadcast struct {
	txBytes []byte
}

func (c *captureBroadcast) BroadcastSync(_ context.Context, txBytes []byte) (chain.BroadcastResult, error) {
	c.txBytes = txBytes
	return chain.BroadcastResult{TxHash: "CAFEBABE", Code: 0}, nil
}

// stubMarkets implements both chain query interfaces the markets cache needs.
type stubMarkets struct{}

func (stubMarkets) ClobPairAll(context.Context) ([]clobtypes.ClobPair, error) {
	return []clobtypes.ClobPair{{
		Id: 0,
		Metadata: &clobtypes.ClobPair_PerpetualClobMetadata{
			PerpetualClobMetadata: &clobtypes.PerpetualClobMetadata{PerpetualId: 0},
		},
		StepBaseQuantums:          1_000_000,
		SubticksPerTick:           1_000,
		QuantumConversionExponent: -9,
	}}, nil
}

func (stubMarkets) AllPerpetuals(context.Context) ([]perptypes.Perpetual, error) {
	return []perptypes.Perpetual{{
		Params: perptypes.PerpetualParams{Id: 0, Ticker: "BTC-USD", AtomicResolution: -10},
	}}, nil
}

type fixture struct {
	svc       *Service
	broadcast *captureBroadcast
	signer    *svpdt.PrivateKeySigner
	agentID   string
	rootID    [32]byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

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

	mkts := markets.NewCache(stubMarkets{}, stubMarkets{}, time.Hour, log.NewNopLogger())
	if err := mkts.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	bc := &captureBroadcast{}
	svc := New(Config{
		Priv:      priv,
		Operator:  operatorAddr,
		ChainID:   testChainID,
		Fee:       operator.FeeSpec{Denom: "asvp", Amount: "25000000000000000", GasLimit: 1_000_000},
		AgentQ:    &fakeAgentQ{agentID: agentID, pubKey: priv.PubKey().Bytes()},
		WalletQ:   fakeWalletQ{},
		Markets:   mkts,
		Account:   fakeAccount{},
		Broadcast: bc,
	})
	svc.now = func() int64 { return testNow }

	return &fixture{svc: svc, broadcast: bc, signer: dtSigner, agentID: agentID, rootID: [32]byte{0xAA}}
}

// issue mints a depth-1 credential granting the given caveats to this agent.
func (f *fixture) issue(t *testing.T, mutate func(*svpdt.IssueParams)) []string {
	t.Helper()
	params := svpdt.IssueParams{
		ChainID:   testChainID,
		Root:      f.rootID,
		RootEpoch: 1,
		Issuer:    f.agentID,
		Audience:  f.agentID,
		Caveats: svpdt.Caveats{
			Principal:   testDelegator,
			Actions:     svpdt.StringSet{ActionCancelOrder, ActionPlaceOrder},
			Subaccounts: svpdt.Uint32Set{0},
			Budget:      svpdt.Coins{{Denom: "uusdc", Amount: big.NewInt(1_000_000)}},
			MaxDepth:    2,
			NotBefore:   testNow - 60,
			Expires:     testNow + 300,
		},
		Nonce: [16]byte{0x01},
	}
	if mutate != nil {
		mutate(&params)
	}
	_, encoded, err := svpdt.Issue(f.signer, params)
	if err != nil {
		t.Fatal(err)
	}
	return []string{base64.StdEncoding.EncodeToString(encoded)}
}

// The critical path: a valid credential produces a broadcast tx whose sole
// message is a correctly-formed, correctly-signed MsgAgentExecDelegated whose
// inner order belongs to the principal.
func TestExecutePlaceOrderEndToEnd(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.ExecutePlaceOrder(context.Background(), ExecPlaceOrderInput{
		Proof: f.issue(t, nil),
		Order: OrderParams{
			SubaccountNumber: 0,
			Ticker:           "BTC-USD",
			Side:             "BUY",
			Size:             "0.001",
			Price:            "60000",
			GoodTilBlock:     123,
			OrderClientID:    7,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TxHash != "CAFEBABE" || res.Principal != testDelegator || res.AgentID != f.agentID {
		t.Errorf("result = %+v", res)
	}

	// Decode what actually went out.
	var txRaw txtypes.TxRaw
	if err := proto.Unmarshal(f.broadcast.txBytes, &txRaw); err != nil {
		t.Fatal(err)
	}
	var body txtypes.TxBody
	if err := proto.Unmarshal(txRaw.BodyBytes, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("wrapper must be the only msg in its tx, got %d", len(body.Messages))
	}
	var wrapper wallettypes.MsgAgentExecDelegated
	if body.Messages[0].TypeUrl != "/dydxprotocol.agentwallet.MsgAgentExecDelegated" {
		t.Fatalf("type url = %s", body.Messages[0].TypeUrl)
	}
	if err := proto.Unmarshal(body.Messages[0].Value, &wrapper); err != nil {
		t.Fatal(err)
	}
	if wrapper.Executor != f.svc.cfg.Operator || wrapper.AgentId != f.agentID {
		t.Errorf("wrapper executor/agent = %s / %s", wrapper.Executor, wrapper.AgentId)
	}
	if len(wrapper.Proof.Tokens) != 1 {
		t.Errorf("proof tokens = %d", len(wrapper.Proof.Tokens))
	}
	var inner clobtypes.MsgPlaceOrder
	if err := proto.Unmarshal(wrapper.InnerMsg.Value, &inner); err != nil {
		t.Fatal(err)
	}
	if got := inner.Order.OrderId.SubaccountId.Owner; got != testDelegator {
		t.Errorf("inner order owner = %q, want the credential principal", got)
	}

	// A short-term inner order rides the gas-free route: empty fee.
	var authInfo txtypes.AuthInfo
	if err := proto.Unmarshal(txRaw.AuthInfoBytes, &authInfo); err != nil {
		t.Fatal(err)
	}
	if len(authInfo.Fee.Amount) != 0 {
		t.Errorf("wrapped short-term order must be gas-free, fee = %v", authInfo.Fee.Amount)
	}

	// The signature must verify for the operator key.
	signBytes, err := payload.DirectSignBytes(txRaw.BodyBytes, txRaw.AuthInfoBytes, testChainID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if !f.svc.cfg.Priv.PubKey().VerifySignature(signBytes, txRaw.Signatures[0]) {
		t.Error("operator signature does not verify")
	}
}

func TestExecuteRejectsBadCredentials(t *testing.T) {
	f := newFixture(t)
	order := OrderParams{
		SubaccountNumber: 0, Ticker: "BTC-USD", Side: "BUY",
		Size: "0.001", Price: "60000", GoodTilBlock: 123,
	}

	cases := map[string]struct {
		proof   []string
		order   OrderParams
		wantErr string
	}{
		"no proof": {proof: nil, order: order, wantErr: "delegation proof is required"},
		"wrong audience": {
			proof: f.issue(t, func(p *svpdt.IssueParams) {
				p.Audience = "did:svp:someoneelse"
			}),
			order: order, wantErr: "credential chain rejected",
		},
		"expired leaf": {
			proof: f.issue(t, func(p *svpdt.IssueParams) {
				p.Caveats.NotBefore = testNow - 600
				p.Caveats.Expires = testNow - 300
			}),
			order: order, wantErr: "credential chain rejected",
		},
		"ungranted subaccount": {
			proof:   f.issue(t, nil),
			order:   OrderParams{SubaccountNumber: 3, Ticker: "BTC-USD", Side: "BUY", Size: "0.001", Price: "60000", GoodTilBlock: 123},
			wantErr: "does not grant subaccount",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.ExecutePlaceOrder(context.Background(), ExecPlaceOrderInput{Proof: tc.proof, Order: tc.order})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestExecuteRejectsUngrantedAction(t *testing.T) {
	f := newFixture(t)
	// The credential grants place+cancel but not batch cancel.
	_, err := f.svc.ExecuteBatchCancel(context.Background(), ExecBatchCancelInput{
		Proof: f.issue(t, nil),
		Cancel: BatchCancelParams{
			SubaccountNumber: 0,
			Batches:          []OrderBatch{{ClobPairID: 0, ClientIDs: []uint32{1}}},
			GoodTilBlock:     123,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not grant action") {
		t.Errorf("ungranted action must be refused by name, got %v", err)
	}
}

func TestIdentityReportsRegistration(t *testing.T) {
	f := newFixture(t)
	out, err := f.svc.Identity(context.Background(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Registered || out.AgentID != f.agentID {
		t.Errorf("identity = %+v", out)
	}
}
