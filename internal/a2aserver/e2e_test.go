package a2aserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// TestReadOnlyServerEndToEnd boots the real HTTP server against a fake
// indexer and drives it through a real A2A client: card fetch, a legacy
// market-data query, and the read-only execution refusal. This is the
// transport-level proof the executor tests can't give — the JSON-RPC handler,
// card route, and health endpoint all in one pass.
func TestReadOnlyServerEndToEnd(t *testing.T) {
	// Fake Comlink indexer: only the endpoint the legacy "markets" query hits.
	fakeIndexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/perpetualMarkets" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"markets":{"BTC-USD":{"ticker":"BTC-USD"}}}`))
	}))
	defer fakeIndexer.Close()

	// Reserve a port, release it, and hand it to the server. Racy in theory;
	// in practice the window is microseconds and the test is hermetic.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- StartServer(ctx, Config{
			ListenAddr: addr,
			PublicURL:  "http://" + addr,
			IndexerURL: fakeIndexer.URL,
		})
	}()

	waitForHealthz(t, "http://"+addr)

	// Card is served and advertises the legacy two skills.
	resp, err := http.Get("http://" + addr + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(card.Skills) != 2 {
		t.Fatalf("read-only card must advertise 2 skills, got %d", len(card.Skills))
	}

	client, err := a2aclient.NewFromCard(ctx, &card)
	if err != nil {
		t.Fatal(err)
	}

	// Legacy market-data query round-trips through real JSON-RPC.
	text := sendText(t, ctx, client, `{"skill":"svpchain-market-data","query":"markets"}`)
	if !strings.Contains(text, "BTC-USD") {
		t.Errorf("markets query did not reach the fake indexer: %s", text)
	}

	// Execution refuses with the requirement, as an answer rather than a
	// transport error.
	refusal := sendText(t, ctx, client, `{"skill":"svpchain-execution","tool":"execute_place_order"}`)
	if !strings.Contains(refusal, "delegation credential") {
		t.Errorf("execution refusal should name the requirement: %s", refusal)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("server exited with error: %v", err)
	}
}

func waitForHealthz(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become healthy")
}

// sendText sends one user message and returns the agent's text reply,
// tolerating both message and task-shaped results.
func sendText(t *testing.T, ctx context.Context, client *a2aclient.Client, body string) string {
	t.Helper()
	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: &a2a.Message{
			ID:    a2a.NewMessageID(),
			Role:  a2a.MessageRoleUser,
			Parts: a2a.ContentParts{a2a.NewTextPart(body)},
		},
	})
	if err != nil {
		t.Fatalf("send %s: %v", body, err)
	}
	switch v := result.(type) {
	case *a2a.Message:
		return messageText(v)
	case *a2a.Task:
		var out strings.Builder
		for _, m := range v.History {
			if m != nil && m.Role == a2a.MessageRoleAgent {
				out.WriteString(messageText(m))
			}
		}
		if v.Status.Message != nil {
			out.WriteString(messageText(v.Status.Message))
		}
		return out.String()
	default:
		t.Fatalf("unexpected result type %T", result)
		return ""
	}
}
