package operator

import (
	"crypto/rand"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/evm/crypto/ethsecp256k1"
	"github.com/cosmos/gogoproto/proto"

	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/payload"
	"github.com/svpchain/svpchain-mcp/lib/mcp/signer"
)

func newKey(t *testing.T) *ethsecp256k1.PrivKey {
	t.Helper()
	bz := make([]byte, 32)
	if _, err := rand.Read(bz); err != nil {
		t.Fatal(err)
	}
	return &ethsecp256k1.PrivKey{Key: bz}
}

func signAndDecode(t *testing.T, priv *ethsecp256k1.PrivKey, gasFree bool) (*txtypes.TxRaw, *txtypes.AuthInfo) {
	t.Helper()
	owner := signer.DeriveAddress(priv)
	msg := &banktypes.MsgSend{
		FromAddress: owner,
		ToAddress:   owner,
		Amount:      sdk.NewCoins(),
	}
	fee := FeeSpec{Denom: "asvp", Amount: "25000000000000000", GasLimit: 1_000_000}
	raw, err := SignTx(priv, "test-chain", chain.AccountInfo{AccountNumber: 3, Sequence: 9}, []sdk.Msg{msg}, fee, gasFree)
	if err != nil {
		t.Fatal(err)
	}
	var txRaw txtypes.TxRaw
	if err := proto.Unmarshal(raw, &txRaw); err != nil {
		t.Fatal(err)
	}
	var authInfo txtypes.AuthInfo
	if err := proto.Unmarshal(txRaw.AuthInfoBytes, &authInfo); err != nil {
		t.Fatal(err)
	}
	return &txRaw, &authInfo
}

// The signature must re-verify against the exact SIGN_MODE_DIRECT sign-bytes
// the chain will recompute — the core property signer.Sign has, minus its
// fee bug.
func TestSignTxSignatureVerifies(t *testing.T) {
	priv := newKey(t)
	txRaw, _ := signAndDecode(t, priv, false)

	signBytes, err := payload.DirectSignBytes(txRaw.BodyBytes, txRaw.AuthInfoBytes, "test-chain", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(txRaw.Signatures) != 1 || !priv.PubKey().VerifySignature(signBytes, txRaw.Signatures[0]) {
		t.Fatal("signature does not verify against the direct sign-bytes")
	}
}

// The fee must be present exactly when the tx is not gas-free — the defect in
// signer.Sign this path exists to avoid.
func TestSignTxFeePresenceFollowsGasFree(t *testing.T) {
	priv := newKey(t)

	_, feePaying := signAndDecode(t, priv, false)
	if len(feePaying.Fee.Amount) != 1 || feePaying.Fee.Amount[0].Denom != "asvp" {
		t.Errorf("fee-paying tx must carry the configured fee, got %v", feePaying.Fee.Amount)
	}
	if feePaying.SignerInfos[0].Sequence != 9 {
		t.Errorf("sequence = %d", feePaying.SignerInfos[0].Sequence)
	}

	_, gasFree := signAndDecode(t, priv, true)
	if len(gasFree.Fee.Amount) != 0 {
		t.Errorf("gas-free tx must carry an empty fee, got %v", gasFree.Fee.Amount)
	}
}
