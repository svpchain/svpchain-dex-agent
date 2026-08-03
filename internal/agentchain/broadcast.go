package agentchain

import (
	"context"
	"encoding/base64"
	"fmt"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/gogoproto/proto"

	"github.com/svpchain/svpchain-mcp/lib/mcp/payload"
)

// BroadcastInput carries a caller-signed registry/delegation tx destined for
// the agent chain. Same SignedTx shape as broadcast_signed_tx.
type BroadcastInput struct {
	SignedTx payload.SignedTx `json:"signed_tx"`
}

// BroadcastOutput mirrors broadcast_signed_tx's result shape.
type BroadcastOutput struct {
	Result payload.BroadcastResult `json:"result"`
}

// BroadcastSignedTx lands a caller-signed tx on the agent chain — the chain
// carrying x/agent + x/agentwallet, which in a split deployment is not the
// one broadcast_signed_tx targets. In a single-chain deployment both rails
// share the connection, so either works.
func (s *Service) BroadcastSignedTx(ctx context.Context, in BroadcastInput) (BroadcastOutput, error) {
	owner, err := s.authorize(ctx)
	if err != nil {
		return BroadcastOutput{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(in.SignedTx.TxRawBytesB64)
	if err != nil {
		return BroadcastOutput{}, fmt.Errorf("decode tx_raw_bytes_b64: %w", err)
	}
	// Same signer pinning as broadcast_signed_tx: a tenant may only land txs
	// signed by its own owner key.
	signer, err := s.signerAddress(raw)
	if err != nil {
		return BroadcastOutput{}, fmt.Errorf("decode signed tx: %w", err)
	}
	if signer != owner {
		return BroadcastOutput{}, fmt.Errorf("signer address %s does not match tenant owner %s", signer, owner)
	}
	res, err := s.broadcast.BroadcastSync(ctx, raw)
	if err != nil {
		return BroadcastOutput{}, err
	}
	return BroadcastOutput{Result: payload.BroadcastResult{
		TxHash: res.TxHash,
		Code:   res.Code,
		RawLog: res.RawLog,
	}}, nil
}

// signerAddress decodes a TxRaw blob, extracts the single SignerInfo's
// public key via the InterfaceRegistry, and returns its bech32 address.
// Mirrors the MCP broadcast_signed_tx signer check.
func (s *Service) signerAddress(raw []byte) (string, error) {
	var txRaw txtypes.TxRaw
	if err := proto.Unmarshal(raw, &txRaw); err != nil {
		return "", fmt.Errorf("unmarshal TxRaw: %w", err)
	}
	var ai txtypes.AuthInfo
	if err := proto.Unmarshal(txRaw.AuthInfoBytes, &ai); err != nil {
		return "", fmt.Errorf("unmarshal AuthInfo: %w", err)
	}
	if len(ai.SignerInfos) != 1 {
		return "", fmt.Errorf("expected exactly 1 signer, got %d", len(ai.SignerInfos))
	}
	pkAny := ai.SignerInfos[0].PublicKey
	if pkAny == nil {
		return "", fmt.Errorf("signer info has no public key")
	}
	var pk cryptotypes.PubKey
	if err := s.registry.UnpackAny(pkAny, &pk); err != nil {
		return "", fmt.Errorf("unpack signer pubkey: %w", err)
	}
	return sdk.AccAddress(pk.Address()).String(), nil
}
