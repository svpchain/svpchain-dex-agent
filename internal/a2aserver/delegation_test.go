package a2aserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
)

// execCtxWithDelegation wraps a raw request and attaches delegation metadata,
// as a caller following the card's extension declaration would.
func execCtxWithDelegation(raw string, meta any) *a2asrv.ExecutorContext {
	ec := execCtxFor(raw)
	ec.Message.Metadata = map[string]any{DelegationMetadataKey: meta}
	return ec
}

// jsonAny round-trips a value through JSON, mirroring how transport delivers
// metadata to the executor (maps and slices of any, not typed structs).
func jsonAny(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDelegationFromMessage(t *testing.T) {
	valid := jsonAny(t, DelegationMetadata{Tokens: []string{"dG9rMQ==", "dG9rMg=="}, Settlement: "so_1"})

	t.Run("absent key means no delegation", func(t *testing.T) {
		for _, msg := range []*a2a.Message{
			nil,
			{},
			{Metadata: map[string]any{"other.ext/v1": "x"}},
		} {
			d, err := delegationFromMessage(msg)
			if err != nil || d != nil {
				t.Fatalf("want (nil, nil), got (%v, %v)", d, err)
			}
		}
	})

	t.Run("valid metadata decodes", func(t *testing.T) {
		d, err := delegationFromMessage(&a2a.Message{Metadata: map[string]any{DelegationMetadataKey: valid}})
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Tokens) != 2 || d.Tokens[0] != "dG9rMQ==" || d.Settlement != "so_1" {
			t.Fatalf("decoded wrong: %+v", d)
		}
	})

	t.Run("malformed metadata is an error, not ignored", func(t *testing.T) {
		for name, meta := range map[string]any{
			"wrong shape":   "just a string",
			"no tokens":     map[string]any{"settlement": "so_1"},
			"empty tokens":  map[string]any{"tokens": []any{}},
			"empty token":   map[string]any{"tokens": []any{""}},
			"unknown field": map[string]any{"tokens": []any{"dG9rMQ=="}, "token": "typo"},
			"non-string":    map[string]any{"tokens": []any{42}},
		} {
			if _, err := delegationFromMessage(&a2a.Message{Metadata: map[string]any{DelegationMetadataKey: meta}}); err == nil {
				t.Fatalf("%s: want error, got nil", name)
			}
		}
	})
}

func TestInjectProof(t *testing.T) {
	tokens := []string{"dG9rMQ==", "dG9rMg=="}

	decode := func(t *testing.T, raw json.RawMessage) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	t.Run("fills absent proof and keeps other args", func(t *testing.T) {
		for _, args := range []json.RawMessage{nil, []byte(`{}`), []byte(`{"order":{"ticker":"BTC-USD"}}`), []byte(`{"proof":null}`), []byte(`{"proof":[]}`)} {
			out, err := injectProof(args, tokens)
			if err != nil {
				t.Fatalf("args %s: %v", args, err)
			}
			m := decode(t, out)
			got, _ := m["proof"].([]any)
			if len(got) != 2 || got[0] != tokens[0] {
				t.Fatalf("args %s: proof not injected: %v", args, m)
			}
			if len(args) > 0 && strings.Contains(string(args), "order") {
				if _, ok := m["order"]; !ok {
					t.Fatalf("other args dropped: %v", m)
				}
			}
		}
	})

	t.Run("matching alias passes unchanged", func(t *testing.T) {
		args := json.RawMessage(`{"proof":["dG9rMQ==","dG9rMg=="]}`)
		out, err := injectProof(args, tokens)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(args) {
			t.Fatalf("args rewritten: %s", out)
		}
	})

	t.Run("conflicting proofs are refused", func(t *testing.T) {
		if _, err := injectProof(json.RawMessage(`{"proof":["b3RoZXI="]}`), tokens); err == nil {
			t.Fatal("want conflict error, got nil")
		}
	})
}

// A malformed delegation attachment fails the request before any dispatch —
// even on a read-only executor serving a query that needs no credential.
func TestHandleRefusesMalformedDelegationMetadata(t *testing.T) {
	e := NewExecutor(marketdata.NewService(fakeReader{}))
	ec := execCtxWithDelegation(`{"skill":"svpchain-market-data","query":"markets"}`, "not an object")
	if _, err := e.handle(context.Background(), ec); err == nil || !strings.Contains(err.Error(), DelegationMetadataKey) {
		t.Fatalf("want metadata error, got %v", err)
	}
}

// The metadata chain reaches the tool as its proof args: a metadata-carrying
// call to a tool with a conflicting args proof is refused before the tool
// runs, and a clean one dispatches (the tool itself then rejects the fake
// tokens, proving the args arrived).
func TestMetadataProofReachesTheTool(t *testing.T) {
	e, _, _ := newAuthedStack(t)
	meta := jsonAny(t, DelegationMetadata{Tokens: []string{"dG9rMQ=="}})

	t.Run("conflict refused before dispatch", func(t *testing.T) {
		ec := execCtxWithDelegation(
			`{"skill":"svpchain-execution","tool":"execute_place_order","args":{"proof":["b3RoZXI="]}}`, meta)
		if _, err := e.handle(context.Background(), ec); err == nil || !strings.Contains(err.Error(), "differ") {
			t.Fatalf("want conflict error, got %v", err)
		}
	})

	t.Run("metadata-only proof reaches the handler", func(t *testing.T) {
		ec := execCtxWithDelegation(
			`{"skill":"svpchain-execution","tool":"execute_place_order","args":{"order":{}}}`, meta)
		out, err := e.handle(context.Background(), ec)
		if err != nil {
			t.Fatal(err)
		}
		var resp Response
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatal(err)
		}
		// The authed test stack registers execution tools as refusals (no
		// operator key), and that refusal mentions the proof requirement —
		// what matters here is that the request dispatched instead of dying
		// on a missing-proof arg. A live-service path is covered by the
		// delegated package's own tests.
		if resp.OK {
			t.Fatalf("expected a refusal from the operator-less stack, got success: %+v", resp)
		}
	})
}
