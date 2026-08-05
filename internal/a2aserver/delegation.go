package a2aserver

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// The SVP delegation extension, as the Agent Card advertises it and as the
// SVP-DT spec (§3.7) defines the wire form: the credential chain rides
// message.metadata under DelegationMetadataKey, so it can be read uniformly
// before any skill-specific parsing of the message text.
const (
	DelegationExtensionURI = "https://svpchain.io/a2a/ext/delegation/v1"
	DelegationMetadataKey  = "svp.delegation/v1"
)

// DelegationMetadata is the value of message.metadata["svp.delegation/v1"].
type DelegationMetadata struct {
	// Tokens is the SVP-DT credential chain, base64 canonical-CBOR, ordered
	// root-issued (depth 1) first, leaf last.
	Tokens []string `json:"tokens"`
	// Settlement optionally names the settlement order the spend is bound to.
	// Parsed for forward compatibility; nothing consumes it yet.
	Settlement string `json:"settlement,omitempty"`
}

// delegationFromMessage extracts the delegation metadata from an A2A message.
// Absent key → (nil, nil). A key that is present but malformed is an error,
// never silently ignored: a caller who set it believes they attached a
// credential, and dropping it would turn their authenticated call into an
// unauthenticated one with a misleading downstream refusal.
func delegationFromMessage(msg *a2a.Message) (*DelegationMetadata, error) {
	if msg == nil || msg.Metadata == nil {
		return nil, nil
	}
	raw, ok := msg.Metadata[DelegationMetadataKey]
	if !ok {
		return nil, nil
	}
	// The transport delivers metadata values as decoded JSON (map[string]any);
	// a round-trip through encoding/json gives the typed form.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("metadata %q is not encodable: %w", DelegationMetadataKey, err)
	}
	var m DelegationMetadata
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("metadata %q is malformed: %w", DelegationMetadataKey, err)
	}
	if len(m.Tokens) == 0 {
		return nil, fmt.Errorf("metadata %q carries no tokens", DelegationMetadataKey)
	}
	for i, t := range m.Tokens {
		if t == "" {
			return nil, fmt.Errorf("metadata %q token[%d] is empty", DelegationMetadataKey, i)
		}
	}
	return &m, nil
}

// injectProof merges a metadata-carried credential chain into a tool's args as
// its "proof" field — the metadata key is the canonical carrier, the args
// field the deprecated alias. Args with no proof get the metadata chain; args
// whose proof equals the metadata chain pass unchanged; a disagreement is
// refused rather than resolved, so no handler can end up executing under a
// different chain than the one the transport layer saw.
func injectProof(args json.RawMessage, tokens []string) (json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &m); err != nil {
			return nil, fmt.Errorf("decode args: %w", err)
		}
	}
	if rawProof, ok := m["proof"]; ok && string(rawProof) != "null" {
		var existing []string
		if err := json.Unmarshal(rawProof, &existing); err != nil {
			return nil, fmt.Errorf("args proof is not a string array: %w", err)
		}
		if len(existing) > 0 {
			if !equalStrings(existing, tokens) {
				return nil, fmt.Errorf(
					"delegation proof given both in message metadata (%q) and in args, and they differ; send it in one place",
					DelegationMetadataKey)
			}
			return args, nil
		}
	}
	rawTokens, err := json.Marshal(tokens)
	if err != nil {
		return nil, fmt.Errorf("encode proof: %w", err)
	}
	m["proof"] = rawTokens
	merged, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode args: %w", err)
	}
	return merged, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
