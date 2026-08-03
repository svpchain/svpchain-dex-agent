package agentrest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/mcpcodec"
)

// newTestClient serves handler and returns a Client pointed at it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	enc := mcpcodec.GetEncodingConfig()
	return New(srv.URL+"/", enc.Codec, enc.InterfaceRegistry) // trailing slash must be tolerated
}

func TestAgentQueryHitsGatewayPath(t *testing.T) {
	enc := mcpcodec.GetEncodingConfig()
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, err := enc.Codec.MarshalJSON(&agenttypes.QueryAgentResponse{
			Agent: agenttypes.Agent{AgentId: "did:svp:abc"},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	})

	resp, err := c.Agent(context.Background(), &agenttypes.QueryAgent{AgentId: "did:svp:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/dydxprotocol/agent/did:svp:abc" {
		t.Errorf("unexpected path %q", gotPath)
	}
	if resp.Agent.AgentId != "did:svp:abc" {
		t.Errorf("round-trip lost the agent id: %+v", resp)
	}
}

func TestWalletDelegationEncodesRootIDAndParamsSplit(t *testing.T) {
	enc := mcpcodec.GetEncodingConfig()
	root := make([]byte, wallettypes.RootIDLen)
	for i := range root {
		root[i] = byte(i)
	}
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body []byte
		var err error
		if strings.Contains(r.URL.Path, "/agentwallet/params") {
			body, err = enc.Codec.MarshalJSON(&wallettypes.QueryParamsResponse{})
		} else {
			body, err = enc.Codec.MarshalJSON(&wallettypes.QueryDelegationResponse{})
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	})

	if _, err := c.Wallet().Delegation(context.Background(), &wallettypes.QueryDelegation{RootId: root}); err != nil {
		t.Fatal(err)
	}
	want := "/dydxprotocol/agentwallet/delegation/" + base64.URLEncoding.EncodeToString(root)
	if gotPath != want {
		t.Errorf("path %q, want %q", gotPath, want)
	}

	// The wallet adapter's Params must hit agentwallet, not x/agent.
	if _, err := c.Wallet().Params(context.Background(), &wallettypes.QueryParams{}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/dydxprotocol/agentwallet/params" {
		t.Errorf("wallet params path %q", gotPath)
	}
}

func TestAccountClientUnpacksAny(t *testing.T) {
	enc := mcpcodec.GetEncodingConfig()
	pub := secp256k1.GenPrivKey().PubKey()
	addr := sdk.AccAddress(pub.Address()).String()
	base := authtypes.NewBaseAccount(sdk.AccAddress(pub.Address()), pub, 7, 42)
	anyAcc, err := codectypes.NewAnyWithValue(base)
	if err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cosmos/auth/v1beta1/accounts/"+addr {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, err := enc.Codec.MarshalJSON(&authtypes.QueryAccountResponse{Account: anyAcc})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(body)
	})

	info, err := c.AccountClient().Account(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.AccountNumber != 7 || info.Sequence != 42 {
		t.Errorf("account info %+v", info)
	}
}

func TestBroadcastSyncPostsAndDecodes(t *testing.T) {
	var gotBody map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cosmos/tx/v1beta1/txs" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"tx_response":{"txhash":"CAFE","code":0,"raw_log":""}}`))
	})

	res, err := c.BroadcastSync(context.Background(), []byte{0xca, 0xfe})
	if err != nil {
		t.Fatal(err)
	}
	if res.TxHash != "CAFE" || res.Code != 0 {
		t.Errorf("result %+v", res)
	}
	if gotBody["tx_bytes"] != base64.StdEncoding.EncodeToString([]byte{0xca, 0xfe}) {
		t.Errorf("tx_bytes %q", gotBody["tx_bytes"])
	}
	if gotBody["mode"] != "BROADCAST_MODE_SYNC" {
		t.Errorf("mode %q", gotBody["mode"])
	}
}

func TestGatewayErrorSurfacesMessage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"agent not found"}`))
	})
	_, err := c.Agent(context.Background(), &agenttypes.QueryAgent{AgentId: "did:svp:missing"})
	if err == nil || !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected gateway message in error, got %v", err)
	}
}
