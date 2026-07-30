package agentchain

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/builder"
	"github.com/svpchain/svpchain-mcp/lib/mcp/payload"
)

// Every build operation shares one contract, mirroring the MCP builders: the
// authenticated tenant's owner address is the msg's signer — a caller cannot
// build a tx that names someone else as owner/delegator — the msg passes its
// own ValidateBasic before assembly, and the result is an unsigned
// payload.TxPayload the caller signs and lands via broadcast_signed_tx.

// PayloadOutput wraps the unsigned tx payload every build operation returns.
type PayloadOutput struct {
	Payload payload.TxPayload `json:"payload"`
}

// assemble validates msg and wraps it in a TxPayload for signer to sign.
func (s *Service) assemble(ctx context.Context, tool, signer, clientID string, msg sdk.Msg, summary payload.Summary) (PayloadOutput, error) {
	type validator interface{ ValidateBasic() error }
	if v, ok := msg.(validator); ok {
		if err := v.ValidateBasic(); err != nil {
			return PayloadOutput{}, err
		}
	}
	acc, err := s.account.Account(ctx, signer)
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("read account state: %w", err)
	}
	summary.ToolName = tool
	summary.MsgTypeURL = sdk.MsgTypeURL(msg)
	p, err := s.asm.Assemble(builder.Args{
		Msgs:          []sdk.Msg{msg},
		SignerAddress: signer,
		AccountNumber: acc.AccountNumber,
		Sequence:      acc.Sequence,
		ClientID:      clientID,
		Summary:       summary,
	})
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("assemble: %w", err)
	}
	return PayloadOutput{Payload: *p}, nil
}

// Pricing is the JSON shape for an agent's advertised per-call pricing.
type Pricing struct {
	PerCall []Coin `json:"per_call,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

func (p *Pricing) toProto() (*agenttypes.Pricing, error) {
	if p == nil {
		return nil, nil
	}
	coins, err := coinsToSDK(p.PerCall)
	if err != nil {
		return nil, fmt.Errorf("pricing: %w", err)
	}
	return &agenttypes.Pricing{PerCall: coins, Unit: p.Unit}, nil
}

// -- x/agent lifecycle --------------------------------------------------

type BuildRegisterAgentInput struct {
	// Operator is the agent's operator address; the agent id is derived from
	// it (did:svp:<operator>), and PublicKeyB64 must be the operator's own
	// 33-byte compressed secp256k1 key.
	Operator        string   `json:"operator"`
	PublicKeyB64    string   `json:"public_key_b64"`
	Endpoint        string   `json:"endpoint"`
	CapabilityHash  string   `json:"capability_hash_hex,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Pricing         *Pricing `json:"pricing,omitempty"`
	InitialBond     Coin     `json:"initial_bond"`
	Metadata        string   `json:"metadata,omitempty"`
	PayloadClientID string   `json:"payload_client_id"`
}

func (s *Service) BuildRegisterAgent(ctx context.Context, in BuildRegisterAgentInput) (PayloadOutput, error) {
	owner, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	operator, err := sdk.AccAddressFromBech32(in.Operator)
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("operator: %w", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(in.PublicKeyB64)
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("public_key_b64: %w", err)
	}
	var capHash []byte
	if in.CapabilityHash != "" {
		if capHash, err = hex.DecodeString(in.CapabilityHash); err != nil {
			return PayloadOutput{}, fmt.Errorf("capability_hash_hex: %w", err)
		}
	}
	pricing, err := in.Pricing.toProto()
	if err != nil {
		return PayloadOutput{}, err
	}
	bond, err := in.InitialBond.ToSDK()
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("initial_bond: %w", err)
	}
	msg := &agenttypes.MsgRegisterAgent{
		Owner:          owner,
		AgentId:        agenttypes.AgentIdFromOperator(operator),
		Operator:       in.Operator,
		PublicKey:      pubKey,
		Endpoint:       in.Endpoint,
		CapabilityHash: capHash,
		Capabilities:   in.Capabilities,
		Pricing:        pricing,
		InitialBond:    bond,
		Metadata:       in.Metadata,
	}
	return s.assemble(ctx, "build_register_agent", owner, in.PayloadClientID, msg, payload.Summary{})
}

type BuildUpdateAgentInput struct {
	AgentID         string   `json:"agent_id"`
	Endpoint        string   `json:"endpoint,omitempty"`
	CapabilityHash  string   `json:"capability_hash_hex,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Pricing         *Pricing `json:"pricing,omitempty"`
	Metadata        string   `json:"metadata,omitempty"`
	PayloadClientID string   `json:"payload_client_id"`
}

func (s *Service) BuildUpdateAgent(ctx context.Context, in BuildUpdateAgentInput) (PayloadOutput, error) {
	owner, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	var capHash []byte
	if in.CapabilityHash != "" {
		if capHash, err = hex.DecodeString(in.CapabilityHash); err != nil {
			return PayloadOutput{}, fmt.Errorf("capability_hash_hex: %w", err)
		}
	}
	pricing, err := in.Pricing.toProto()
	if err != nil {
		return PayloadOutput{}, err
	}
	msg := &agenttypes.MsgUpdateAgent{
		Owner:          owner,
		AgentId:        in.AgentID,
		Endpoint:       in.Endpoint,
		CapabilityHash: capHash,
		Capabilities:   in.Capabilities,
		Pricing:        pricing,
		Metadata:       in.Metadata,
		// PublicKey deliberately left empty: key rotation was removed on
		// chain, and the msg rejects a non-empty key with a reason.
	}
	return s.assemble(ctx, "build_update_agent", owner, in.PayloadClientID, msg, payload.Summary{})
}

type BuildBondInput struct {
	AgentID         string `json:"agent_id"`
	Amount          Coin   `json:"amount"`
	PayloadClientID string `json:"payload_client_id"`
}

func (s *Service) BuildDepositBond(ctx context.Context, in BuildBondInput) (PayloadOutput, error) {
	owner, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	amount, err := in.Amount.ToSDK()
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("amount: %w", err)
	}
	msg := &agenttypes.MsgDepositBond{Owner: owner, AgentId: in.AgentID, Amount: amount}
	return s.assemble(ctx, "build_deposit_bond", owner, in.PayloadClientID, msg,
		payload.Summary{AmountHuman: in.Amount.Amount, Denom: in.Amount.Denom})
}

func (s *Service) BuildWithdrawBond(ctx context.Context, in BuildBondInput) (PayloadOutput, error) {
	owner, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	amount, err := in.Amount.ToSDK()
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("amount: %w", err)
	}
	msg := &agenttypes.MsgWithdrawBond{Owner: owner, AgentId: in.AgentID, Amount: amount}
	return s.assemble(ctx, "build_withdraw_bond", owner, in.PayloadClientID, msg,
		payload.Summary{AmountHuman: in.Amount.Amount, Denom: in.Amount.Denom})
}

type BuildDeregisterAgentInput struct {
	AgentID         string `json:"agent_id"`
	PayloadClientID string `json:"payload_client_id"`
}

func (s *Service) BuildDeregisterAgent(ctx context.Context, in BuildDeregisterAgentInput) (PayloadOutput, error) {
	owner, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	msg := &agenttypes.MsgDeregisterAgent{Owner: owner, AgentId: in.AgentID}
	return s.assemble(ctx, "build_deregister_agent", owner, in.PayloadClientID, msg, payload.Summary{})
}

// -- x/agentwallet delegation lifecycle ---------------------------------

// Limits is the JSON shape of a delegation's on-chain grant. Every set left
// empty grants nothing — the chain's own semantics.
type Limits struct {
	SpendLimitTotal    []Coin   `json:"spend_limit_total,omitempty"`
	SpendLimitDaily    []Coin   `json:"spend_limit_daily,omitempty"`
	SvcSpendLimitTotal []Coin   `json:"svc_spend_limit_total,omitempty"`
	SvcSpendLimitDaily []Coin   `json:"svc_spend_limit_daily,omitempty"`
	Denoms             []string `json:"denoms,omitempty"`
	Actions            []string `json:"actions,omitempty"`
	Skills             []string `json:"skills,omitempty"`
	Contracts          []string `json:"contracts,omitempty"`
	Subaccounts        []uint32 `json:"subaccounts,omitempty"`
	MaxDepth           uint32   `json:"max_depth,omitempty"`
	MaxTokenTTLSeconds uint32   `json:"max_token_ttl_seconds,omitempty"`
}

func (l Limits) toProto() (wallettypes.Limits, error) {
	total, err := coinsToSDK(l.SpendLimitTotal)
	if err != nil {
		return wallettypes.Limits{}, fmt.Errorf("spend_limit_total: %w", err)
	}
	daily, err := coinsToSDK(l.SpendLimitDaily)
	if err != nil {
		return wallettypes.Limits{}, fmt.Errorf("spend_limit_daily: %w", err)
	}
	svcTotal, err := coinsToSDK(l.SvcSpendLimitTotal)
	if err != nil {
		return wallettypes.Limits{}, fmt.Errorf("svc_spend_limit_total: %w", err)
	}
	svcDaily, err := coinsToSDK(l.SvcSpendLimitDaily)
	if err != nil {
		return wallettypes.Limits{}, fmt.Errorf("svc_spend_limit_daily: %w", err)
	}
	return wallettypes.Limits{
		SpendLimitTotal:    total,
		SpendLimitDaily:    daily,
		SvcSpendLimitTotal: svcTotal,
		SvcSpendLimitDaily: svcDaily,
		Denoms:             l.Denoms,
		Actions:            l.Actions,
		Skills:             l.Skills,
		Contracts:          l.Contracts,
		Subaccounts:        l.Subaccounts,
		MaxDepth:           l.MaxDepth,
		MaxTokenTtlSeconds: l.MaxTokenTTLSeconds,
	}, nil
}

type BuildCreateDelegationInput struct {
	AgentID         string `json:"agent_id"`
	Limits          Limits `json:"limits"`
	ExpiresAt       int64  `json:"expires_at"`
	PayloadClientID string `json:"payload_client_id"`
}

func (s *Service) BuildCreateDelegation(ctx context.Context, in BuildCreateDelegationInput) (PayloadOutput, error) {
	delegator, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	limits, err := in.Limits.toProto()
	if err != nil {
		return PayloadOutput{}, err
	}
	msg := &wallettypes.MsgCreateDelegation{
		Delegator: delegator,
		AgentId:   in.AgentID,
		Limits:    limits,
		ExpiresAt: in.ExpiresAt,
	}
	return s.assemble(ctx, "build_create_delegation", delegator, in.PayloadClientID, msg, payload.Summary{})
}

type BuildUpdateDelegationInput struct {
	RootID          string `json:"root_id"`
	Limits          Limits `json:"limits"`
	ExpiresAt       int64  `json:"expires_at"`
	PayloadClientID string `json:"payload_client_id"`
}

func (s *Service) BuildUpdateDelegation(ctx context.Context, in BuildUpdateDelegationInput) (PayloadOutput, error) {
	delegator, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	rootID, err := rootIDFromHex(in.RootID)
	if err != nil {
		return PayloadOutput{}, err
	}
	limits, err := in.Limits.toProto()
	if err != nil {
		return PayloadOutput{}, err
	}
	msg := &wallettypes.MsgUpdateDelegation{
		Delegator: delegator,
		RootId:    rootID,
		Limits:    limits,
		ExpiresAt: in.ExpiresAt,
	}
	return s.assemble(ctx, "build_update_delegation", delegator, in.PayloadClientID, msg, payload.Summary{})
}

type BuildDelegationRefInput struct {
	RootID          string `json:"root_id"`
	PayloadClientID string `json:"payload_client_id"`
}

func (s *Service) buildDelegationLifecycle(ctx context.Context, tool string, in BuildDelegationRefInput,
	mk func(delegator string, rootID []byte) sdk.Msg) (PayloadOutput, error) {
	delegator, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	rootID, err := rootIDFromHex(in.RootID)
	if err != nil {
		return PayloadOutput{}, err
	}
	return s.assemble(ctx, tool, delegator, in.PayloadClientID, mk(delegator, rootID), payload.Summary{})
}

func (s *Service) BuildPauseDelegation(ctx context.Context, in BuildDelegationRefInput) (PayloadOutput, error) {
	return s.buildDelegationLifecycle(ctx, "build_pause_delegation", in, func(d string, r []byte) sdk.Msg {
		return &wallettypes.MsgPauseDelegation{Delegator: d, RootId: r}
	})
}

func (s *Service) BuildResumeDelegation(ctx context.Context, in BuildDelegationRefInput) (PayloadOutput, error) {
	return s.buildDelegationLifecycle(ctx, "build_resume_delegation", in, func(d string, r []byte) sdk.Msg {
		return &wallettypes.MsgResumeDelegation{Delegator: d, RootId: r}
	})
}

func (s *Service) BuildRevokeDelegation(ctx context.Context, in BuildDelegationRefInput) (PayloadOutput, error) {
	return s.buildDelegationLifecycle(ctx, "build_revoke_delegation", in, func(d string, r []byte) sdk.Msg {
		return &wallettypes.MsgRevokeDelegation{Delegator: d, RootId: r}
	})
}

type BuildRevokeTokenInput struct {
	RootID string `json:"root_id"`
	// TokenHash is sha256 of the canonical token bytes, hex-encoded.
	TokenHash       string `json:"token_hash"`
	PayloadClientID string `json:"payload_client_id"`
}

func (s *Service) BuildRevokeToken(ctx context.Context, in BuildRevokeTokenInput) (PayloadOutput, error) {
	delegator, err := s.authorize(ctx)
	if err != nil {
		return PayloadOutput{}, err
	}
	rootID, err := rootIDFromHex(in.RootID)
	if err != nil {
		return PayloadOutput{}, err
	}
	tokenHash, err := hex.DecodeString(in.TokenHash)
	if err != nil {
		return PayloadOutput{}, fmt.Errorf("token_hash must be hex: %w", err)
	}
	msg := &wallettypes.MsgRevokeToken{Delegator: delegator, RootId: rootID, TokenHash: tokenHash}
	return s.assemble(ctx, "build_revoke_token", delegator, in.PayloadClientID, msg, payload.Summary{})
}
