package a2aserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/cosmos/evm/crypto/ethsecp256k1"

	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-dex-agent/internal/toolbridge"
	"github.com/svpchain/svpchain-mcp/lib/mcp/auth"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/signer"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// tenantAdapter mirrors the wire-level adapter: auto-issued tenants resolve
// through the dynamic store.
type tenantAdapter struct{ store *auth.DynamicTenantStore }

func (a tenantAdapter) LookupTenantPolicy(tenantID string) (policy.TenantPolicy, bool) {
	rec, err := a.store.LookupByTenantID(tenantID)
	if err != nil {
		return policy.TenantPolicy{}, false
	}
	return policy.TenantPolicy{
		TenantID:           rec.TenantID,
		Owner:              rec.Owner,
		AllowedSubaccounts: rec.AllowedSubaccounts,
		KillSwitch:         rec.KillSwitch,
	}, true
}

// newAuthedStack wires just enough of the full executor to exercise the
// dispatch + auth path: the auth tools, whoami, and the resolver — no chain,
// no indexer beyond the fake reader.
func newAuthedStack(t *testing.T) (*Executor, *auth.DynamicTenantStore, *auth.SessionBearers) {
	t.Helper()
	tenants := auth.NewDynamicTenantStore(auth.DynamicTenantStoreConfig{
		BearerTTL:                 auth.DefaultBearerTTL,
		DefaultAllowedSubaccounts: []uint32{0, 1},
	}, nil)
	sessions := auth.NewSessionBearers(auth.DefaultBearerTTL, nil)
	engine := policy.NewEngine(nil)
	engine.SetDynamicSource(tenantAdapter{store: tenants})

	h := tools.New("svp-test-1", tools.Deps{
		NonceStore:       auth.NewNonceStore(auth.DefaultChallengeTTL, nil),
		DynamicTenants:   tenants,
		IPChallengeLimit: auth.NewIPRateLimiter(100, time.Minute, nil),
		SessionBearers:   sessions,
		Policy:           engine,
		RateLimit:        policy.NewRateLimiter(0, 0),
		BroadcastMode:    "server",
	})
	reg := toolbridge.New(h)
	// Production wiring always registers execution — as refusals when no
	// operator key is configured — so the test surface matches the card.
	reg.RegisterExecution(nil)
	exec := NewFullExecutor(
		marketdata.NewService(fakeReader{}),
		reg,
		&AuthResolver{Tenants: tenants, Sessions: sessions},
		nil, nil,
	)
	return exec, tenants, sessions
}

func execCtxWithContextID(raw, contextID string) *a2asrv.ExecutorContext {
	ec := execCtxFor(raw)
	ec.ContextID = contextID
	return ec
}

// dispatch runs one envelope through the executor and decodes the Response.
func dispatch(t *testing.T, e *Executor, ec *a2asrv.ExecutorContext) Response {
	t.Helper()
	out, err := e.handle(context.Background(), ec)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("response is not a Response envelope: %v (%s)", err, out)
	}
	return resp
}

func resultField(t *testing.T, resp Response, key string) string {
	t.Helper()
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %+v", resp.Result)
	}
	v, _ := m[key].(string)
	return v
}

// The critical M1 path: auth_challenge → wallet-sign → auth_verify over the
// A2A envelope, then a gated tool (whoami) authenticated three ways — session
// binding, envelope bearer — and refused when unauthenticated.
func TestEnvelopeAuthFlowEndToEnd(t *testing.T) {
	e, _, _ := newAuthedStack(t)

	bz := make([]byte, 32)
	if _, err := rand.Read(bz); err != nil {
		t.Fatal(err)
	}
	priv := &ethsecp256k1.PrivKey{Key: bz}
	owner := signer.DeriveAddress(priv)

	// 1. Challenge.
	ch := dispatch(t, e, execCtxFor(
		`{"skill":"svpchain-auth","tool":"auth_challenge","args":{"owner":"`+owner+`"}}`))
	if !ch.OK {
		t.Fatalf("auth_challenge refused: %s", ch.Error)
	}
	challenge, nonce := resultField(t, ch, "challenge"), resultField(t, ch, "nonce")

	// 2. Sign + verify on A2A context "conv-1" — the bearer binds to it.
	sig, err := priv.Sign([]byte(challenge))
	if err != nil {
		t.Fatal(err)
	}
	vf := dispatch(t, e, execCtxWithContextID(
		`{"skill":"svpchain-auth","tool":"auth_verify","args":{"nonce":"`+nonce+
			`","signature":"`+base64.StdEncoding.EncodeToString(sig)+`"}}`, "conv-1"))
	if !vf.OK {
		t.Fatalf("auth_verify refused: %s", vf.Error)
	}
	bearer := resultField(t, vf, "bearer_token")
	if bearer == "" {
		t.Fatal("auth_verify minted no bearer")
	}

	// 3a. whoami via the session-bound bearer (same context id, no header).
	who := dispatch(t, e, execCtxWithContextID(
		`{"skill":"svpchain-account","tool":"whoami"}`, "conv-1"))
	if !who.OK {
		t.Fatalf("whoami via session binding refused: %s", who.Error)
	}
	if got := resultField(t, who, "owner"); got != owner {
		t.Errorf("whoami owner = %q, want %q", got, owner)
	}

	// 3b. whoami via the envelope bearer on a fresh context.
	who2 := dispatch(t, e, execCtxFor(
		`{"skill":"svpchain-account","tool":"whoami","bearer":"`+bearer+`"}`))
	if !who2.OK {
		t.Fatalf("whoami via envelope bearer refused: %s", who2.Error)
	}

	// 3c. Unauthenticated whoami refuses with the handshake hint.
	who3 := dispatch(t, e, execCtxFor(`{"skill":"svpchain-account","tool":"whoami"}`))
	if who3.OK {
		t.Fatal("unauthenticated whoami must refuse")
	}
	if !strings.Contains(who3.Error, "auth_challenge") {
		t.Errorf("refusal should point at the auth flow, got: %s", who3.Error)
	}
}

func TestBearerRidesTheAuthorizationHeader(t *testing.T) {
	e, tenants, _ := newAuthedStack(t)
	bearer, _, _, err := tenants.Mint("svp1headerowner")
	if err != nil {
		t.Fatal(err)
	}

	ec := execCtxFor(`{"skill":"svpchain-account","tool":"whoami"}`)
	ec.ServiceParams = a2asrv.NewServiceParams(map[string][]string{
		"Authorization": {"Bearer " + bearer},
	})
	resp := dispatch(t, e, ec)
	if !resp.OK {
		t.Fatalf("whoami via Authorization header refused: %s", resp.Error)
	}
	if got := resultField(t, resp, "owner"); got != "svp1headerowner" {
		t.Errorf("owner = %q", got)
	}
}

func TestDispatchRejectsSkillToolMismatch(t *testing.T) {
	e, _, _ := newAuthedStack(t)
	if _, err := e.handle(context.Background(), execCtxFor(
		`{"skill":"svpchain-market-data","tool":"whoami"}`)); err == nil ||
		!strings.Contains(err.Error(), "belongs to skill") {
		t.Errorf("skill/tool mismatch must be named, got %v", err)
	}
}

func TestDispatchRejectsUnknownTool(t *testing.T) {
	e, _, _ := newAuthedStack(t)
	if _, err := e.handle(context.Background(), execCtxFor(
		`{"skill":"svpchain-account","tool":"nope"}`)); err == nil ||
		!strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("unknown tool must be named, got %v", err)
	}
}

// The card and the registry cannot drift: every skill the card advertises in
// full mode carries exactly the registry's tools, and the legacy card still
// advertises market-data + execution only.
func TestCardMatchesRegistry(t *testing.T) {
	e, _, _ := newAuthedStack(t)
	card := BuildAgentCard("http://example.test", e.registry)

	bySkill := e.registry.BySkill()
	seen := map[string]bool{}
	for _, sk := range card.Skills {
		seen[sk.ID] = true
		for _, tool := range bySkill[sk.ID] {
			if !strings.Contains(sk.Description, tool) {
				t.Errorf("skill %s description does not name tool %s", sk.ID, tool)
			}
		}
	}
	for id := range bySkill {
		if !seen[id] {
			t.Errorf("registry skill %s missing from card", id)
		}
	}

	legacy := BuildAgentCard("http://example.test", nil)
	if len(legacy.Skills) != 2 {
		t.Errorf("legacy card must advertise exactly market-data + execution, got %d skills", len(legacy.Skills))
	}
}

var _ = a2a.Message{} // keep the a2a import when helpers move
