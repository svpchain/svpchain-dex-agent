package agentchain

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/gogoproto/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/mcpcodec"
	"github.com/svpchain/svpchain-mcp/lib/mcp/payload"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

type fakeBroadcast struct {
	got []byte
}

func (f *fakeBroadcast) BroadcastSync(_ context.Context, tx []byte) (chain.BroadcastResult, error) {
	f.got = tx
	return chain.BroadcastResult{TxHash: "ABC123", Code: 0}, nil
}

// signedTxB64 assembles a minimal TxRaw whose single SignerInfo carries pub,
// base64-encoded the way sign_transaction emits it.
func signedTxB64(t *testing.T, pub *secp256k1.PubKey) string {
	t.Helper()
	pkAny, err := codectypes.NewAnyWithValue(pub)
	if err != nil {
		t.Fatal(err)
	}
	aiBytes, err := proto.Marshal(&txtypes.AuthInfo{
		SignerInfos: []*txtypes.SignerInfo{{PublicKey: pkAny}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(&txtypes.TxRaw{AuthInfoBytes: aiBytes, Signatures: [][]byte{{0x1}}})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestBroadcastPinsSignerToTenantOwner(t *testing.T) {
	ownerKey := secp256k1.GenPrivKey()
	ownerAddr := sdk.AccAddress(ownerKey.PubKey().Address()).String()
	engine := policy.NewEngine([]policy.TenantPolicy{{TenantID: "t1", Owner: ownerAddr}})
	bc := &fakeBroadcast{}
	svc := New(nil, nil, nil, nil, engine, bc, mcpcodec.GetEncodingConfig().InterfaceRegistry)
	ctx := tools.WithTenant(context.Background(), tools.TenantContext{TenantID: "t1", Owner: ownerAddr})

	// A tx signed by some other key must be refused before it hits the wire.
	stranger := secp256k1.GenPrivKey().PubKey().(*secp256k1.PubKey)
	_, err := svc.BroadcastSignedTx(ctx, BroadcastInput{
		SignedTx: payload.SignedTx{TxRawBytesB64: signedTxB64(t, stranger)},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match tenant owner") {
		t.Fatalf("foreign signer must be refused, got %v", err)
	}
	if bc.got != nil {
		t.Fatal("refused tx must not reach the broadcast client")
	}

	// The owner's own tx goes through.
	out, err := svc.BroadcastSignedTx(ctx, BroadcastInput{
		SignedTx: payload.SignedTx{TxRawBytesB64: signedTxB64(t, ownerKey.PubKey().(*secp256k1.PubKey))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.TxHash != "ABC123" || bc.got == nil {
		t.Fatalf("expected broadcast to land, got %+v", out)
	}
}
