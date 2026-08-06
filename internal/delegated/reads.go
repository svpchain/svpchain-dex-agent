package delegated

import (
	"context"
	"encoding/json"
	"fmt"

	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	"github.com/svpchain/svpdt"
)

// ActionQueryAccount is the caveat action granting owner-scoped account reads.
// Unlike the four execute actions it never appears in a chain message — reads
// are answered off-chain — so it must NOT be listed in a root delegation's
// on-chain Limits.actions (the chain's registry would reject it); it belongs
// only in the token caveats.
const ActionQueryAccount = "query.account"

// readSpec maps a query tool onto its delegated-read enforcement facts.
type readSpec struct {
	// ownerField names the tool's owner argument ("address" or "owner"),
	// defaulted to the credential's principal and pinned against it.
	ownerField string
	// hasSubaccount marks tools whose args carry subaccount_number, checked
	// against the credential's subaccount caveat.
	hasSubaccount bool
}

// readSpecs lists the tools a query.account credential covers. Every entry
// must be principal-boundable (an owner argument exists to pin) — get_order,
// which takes only an order id, is excluded for exactly that reason, and
// whoami because it introspects a bearer session. Growing the set is one line
// here plus a card mention.
var readSpecs = map[string]readSpec{
	"get_subaccount":       {ownerField: "address", hasSubaccount: true},
	"get_live_subaccount":  {ownerField: "owner", hasSubaccount: true},
	"get_balance":          {ownerField: "owner"},
	"get_orders":           {ownerField: "address", hasSubaccount: true},
	"get_fills":            {ownerField: "address", hasSubaccount: true},
	"get_transfers":        {ownerField: "address", hasSubaccount: true},
	"get_pnl":              {ownerField: "address", hasSubaccount: true},
	"get_historical_pnl":   {ownerField: "address", hasSubaccount: true},
	"get_funding_payments": {ownerField: "address", hasSubaccount: true},
}

// ReadSpecFor reports whether tool accepts a delegated-read credential.
func ReadSpecFor(tool string) (readSpec, bool) {
	spec, ok := readSpecs[tool]
	return spec, ok
}

// AuthorizeRead verifies a delegation proof for a read-only tool call and
// returns the verified grant plus the args rewritten to the credential's
// principal. It performs the state checks the chain's AnteHandler would run
// for a write — epoch freshness, pause — because reads never reach the chain.
//
// Nonce replay within the token's TTL is accepted deliberately: reads are
// idempotent, a read credential's value is exactly poll-reuse, and audience
// binding already confines a lifted chain to this one agent.
func (s *Service) AuthorizeRead(ctx context.Context, tool string, proofB64 []string, args json.RawMessage) (*svpdt.Verified, json.RawMessage, error) {
	spec, ok := ReadSpecFor(tool)
	if !ok {
		return nil, nil, fmt.Errorf("tool %q does not accept delegated-read credentials", tool)
	}
	_, verified, err := s.verifyProof(ctx, proofB64)
	if err != nil {
		return nil, nil, err
	}
	if !verified.Effective.Actions.Has(ActionQueryAccount) {
		return nil, nil, fmt.Errorf(
			"credential does not grant action %q (granted: %v)", ActionQueryAccount, verified.Effective.Actions)
	}
	if err := s.checkRootState(ctx, verified); err != nil {
		return nil, nil, err
	}
	newArgs, err := pinArgsToPrincipal(args, spec, verified)
	if err != nil {
		return nil, nil, err
	}
	return verified, newArgs, nil
}

// checkRootState refuses when the credential's root delegation is unknown,
// paused, or at a different epoch than the tokens were minted for — the same
// revocation levers the chain enforces on writes. Answers are cached per root
// for epochCacheTTL seconds, which bounds both revocation blindness under
// polling and chain-query amplification.
func (s *Service) checkRootState(ctx context.Context, v *svpdt.Verified) error {
	s.epochMu.Lock()
	entry, ok := s.epochCache[v.Root]
	fresh := ok && s.now()-entry.fetched < s.epochCacheTTL
	s.epochMu.Unlock()

	if !fresh {
		resp, err := s.cfg.WalletQ.Epoch(ctx, &wallettypes.QueryEpoch{RootId: v.Root[:]})
		if err != nil {
			return fmt.Errorf("root delegation state unavailable (revoked or unknown root?): %w", err)
		}
		entry = epochEntry{epoch: resp.Epoch, paused: resp.Paused, fetched: s.now()}
		s.epochMu.Lock()
		s.epochCache[v.Root] = entry
		s.epochMu.Unlock()
	}

	if entry.paused {
		return fmt.Errorf("root delegation is paused")
	}
	if entry.epoch != v.RootEpoch {
		return fmt.Errorf(
			"credential was minted for root epoch %d but the chain is at %d; mint a fresh credential",
			v.RootEpoch, entry.epoch)
	}
	return nil
}

// pinArgsToPrincipal defaults the tool's owner argument to the credential's
// principal, refuses an explicit owner naming anyone else, and checks the
// subaccount caveat. The vendored handlers' own CheckOwner/CheckSubaccount run
// again behind this via the synthesized tenant — belt and braces, not bypass.
func pinArgsToPrincipal(args json.RawMessage, spec readSpec, v *svpdt.Verified) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &m); err != nil {
			return nil, fmt.Errorf("decode args: %w", err)
		}
	}

	var owner string
	if raw, ok := m[spec.ownerField]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &owner); err != nil {
			return nil, fmt.Errorf("args %s: %w", spec.ownerField, err)
		}
	}
	switch owner {
	case "":
		raw, err := json.Marshal(v.Principal)
		if err != nil {
			return nil, fmt.Errorf("encode principal: %w", err)
		}
		m[spec.ownerField] = raw
	case v.Principal:
		// explicit and correct
	default:
		return nil, fmt.Errorf("credential is for principal %s; cannot read %s", v.Principal, owner)
	}

	if spec.hasSubaccount {
		var sub uint32 // absent decodes to 0, matching the handlers' default
		if raw, ok := m["subaccount_number"]; ok && string(raw) != "null" {
			if err := json.Unmarshal(raw, &sub); err != nil {
				return nil, fmt.Errorf("args subaccount_number: %w", err)
			}
		}
		if !v.Effective.Subaccounts.Has(sub) {
			return nil, fmt.Errorf("credential does not grant subaccount %d (granted: %v)", sub, v.Effective.Subaccounts)
		}
	}

	merged, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode args: %w", err)
	}
	return merged, nil
}
