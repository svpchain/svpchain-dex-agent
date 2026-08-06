package toolbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Value string `json:"value"`
}
type echoOut struct {
	Echoed string `json:"echoed"`
}

func TestAdaptDecodesArgsAndReturnsOutput(t *testing.T) {
	call := adapt(func(_ context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		if req != nil {
			t.Fatal("adapter must pass a nil CallToolRequest")
		}
		return nil, echoOut{Echoed: in.Value}, nil
	})

	out, err := call(context.Background(), json.RawMessage(`{"value":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(echoOut).Echoed != "hi" {
		t.Errorf("expected echoed input, got %+v", out)
	}
}

func TestAdaptEmptyArgsYieldZeroInput(t *testing.T) {
	call := adapt(func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{Echoed: in.Value}, nil
	})
	out, err := call(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.(echoOut).Echoed != "" {
		t.Errorf("expected zero-value input, got %+v", out)
	}
}

func TestAdaptReportsDecodeAndHandlerErrors(t *testing.T) {
	boom := errors.New("handler refused")
	call := adapt(func(_ context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, echoOut, error) {
		return nil, echoOut{}, boom
	})

	if _, err := call(context.Background(), json.RawMessage(`{nonsense`)); err == nil {
		t.Error("malformed args must fail to decode")
	}
	if _, err := call(context.Background(), nil); !errors.Is(err, boom) {
		t.Errorf("handler error must propagate, got %v", err)
	}
}

// A handler returning an IsError result (none do today) must not smuggle it
// through as success.
func TestAdaptTreatsIsErrorResultAsError(t *testing.T) {
	call := adapt(func(_ context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, echoOut, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "soft refusal"}},
		}, echoOut{}, nil
	})
	_, err := call(context.Background(), nil)
	if err == nil || err.Error() != "soft refusal" {
		t.Errorf("IsError result must surface as the error text, got %v", err)
	}
}

func TestAdaptNative(t *testing.T) {
	call := adaptNative(func(_ context.Context, in echoIn) (echoOut, error) {
		return echoOut{Echoed: in.Value}, nil
	})
	out, err := call(context.Background(), json.RawMessage(`{"value":"native"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(echoOut).Echoed != "native" {
		t.Errorf("expected echoed input, got %+v", out)
	}
}

// The strict adapter turns the flat-args trap into an actionable refusal: an
// execution input's parameters nest under a wrapper key, and a flat field
// would silently decode to zero values (a deposit targeting subaccount 0).
func TestAdaptStrictNativeRefusesUnknownTopLevelKeys(t *testing.T) {
	type depositParams struct {
		SubaccountNumber uint32 `json:"subaccount_number"`
	}
	type execIn struct {
		Proof   []string      `json:"proof"`
		Deposit depositParams `json:"deposit"`
	}
	called := false
	call := adaptStrictNative(func(_ context.Context, in execIn) (uint32, error) {
		called = true
		return in.Deposit.SubaccountNumber, nil
	})

	// Flat args — the read tools' shape — refuse and name the real keys.
	_, err := call(context.Background(), json.RawMessage(`{"subaccount_number":1,"human_usdc":"10"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown args key") ||
		!strings.Contains(err.Error(), `"deposit"`) {
		t.Fatalf("flat args must refuse naming the wrapper key, got %v", err)
	}
	if called {
		t.Fatal("the handler must not run on refused args")
	}

	// Properly nested args — with the metadata-injected proof key — pass.
	out, err := call(context.Background(), json.RawMessage(`{"proof":["tok"],"deposit":{"subaccount_number":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(uint32) != 1 {
		t.Errorf("nested subaccount_number lost: got %v", out)
	}
}

func TestRegistryRejectsDuplicatesAndUnknownLookups(t *testing.T) {
	r := newRegistry()
	r.add("skill-a", "tool-1", func(context.Context, json.RawMessage) (any, error) { return nil, nil })

	if _, ok := r.Lookup("tool-1"); !ok {
		t.Error("registered tool must resolve")
	}
	if _, ok := r.Lookup("tool-2"); ok {
		t.Error("unregistered tool must not resolve")
	}

	defer func() {
		if recover() == nil {
			t.Error("duplicate registration must panic — it is a programming error")
		}
	}()
	r.add("skill-b", "tool-1", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
}
