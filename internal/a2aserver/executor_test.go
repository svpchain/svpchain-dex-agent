package a2aserver

import (
	"context"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
)

// execCtxFor wraps a raw request string in the ExecutorContext shape handle
// expects, as the JSON-RPC handler would.
func execCtxFor(raw string) *a2asrv.ExecutorContext {
	return &a2asrv.ExecutorContext{
		Message: &a2a.Message{Parts: a2a.ContentParts{a2a.NewTextPart(raw)}},
	}
}

// fakeReader serves a fixed snapshot, so the dispatch is tested without a live
// indexer — the executor's job is routing, not fetching.
type fakeReader struct{}

func (fakeReader) ListPerpetualMarkets(context.Context) (*indexer.PerpetualMarketsResponse, error) {
	return &indexer.PerpetualMarketsResponse{
		Markets: map[string]indexer.PerpetualMarket{"BTC-USD": {Ticker: "BTC-USD"}},
	}, nil
}
func (fakeReader) GetPerpetualMarket(_ context.Context, t string) (*indexer.PerpetualMarket, error) {
	return &indexer.PerpetualMarket{Ticker: t, OraclePrice: "100"}, nil
}
func (fakeReader) GetOrderbook(context.Context, string) (*indexer.Orderbook, error) {
	return &indexer.Orderbook{
		Asks: []indexer.OrderbookPriceLevel{{Price: "100", Size: "5"}},
	}, nil
}
func (fakeReader) GetHistoricalFunding(context.Context, string) (*indexer.HistoricalFundingResponse, error) {
	return &indexer.HistoricalFundingResponse{}, nil
}

func newTestExecutor() *Executor {
	return NewExecutor(marketdata.NewService(fakeReader{}))
}

func TestHandleMarketDataQueries(t *testing.T) {
	e := newTestExecutor()
	cases := map[string]string{
		"markets":   `{"skill":"svpchain-market-data","query":"markets"}`,
		"market":    `{"skill":"svpchain-market-data","query":"market","ticker":"BTC-USD"}`,
		"orderbook": `{"skill":"svpchain-market-data","query":"orderbook","ticker":"BTC-USD"}`,
		"funding":   `{"skill":"svpchain-market-data","query":"funding","ticker":"BTC-USD"}`,
		"estimate":  `{"skill":"svpchain-market-data","query":"estimate","ticker":"BTC-USD","side":"buy","size":"2"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := e.handle(context.Background(), execCtxFor(raw))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if out == "" || out[0] != '{' && out[0] != '[' {
				t.Errorf("%s: expected JSON, got %q", name, out)
			}
		})
	}
}

// ★ The execution skill is advertised on the card but must refuse, naming the
// requirement. A caller should learn what execution needs, not meet silence.
func TestExecutionSkillRefusesWithAReason(t *testing.T) {
	e := newTestExecutor()
	_, err := e.handle(context.Background(),
		execCtxFor(`{"skill":"svpchain-execution","action":"place_order"}`))
	if err == nil {
		t.Fatal("execution skill returned success; it must refuse")
	}
	if !strings.Contains(err.Error(), "delegation credential") {
		t.Errorf("refusal should name the requirement, got: %v", err)
	}
}

func TestHandleRejectsBadRequests(t *testing.T) {
	e := newTestExecutor()
	for name, raw := range map[string]string{
		"not json":      `hello`,
		"no skill":      `{"query":"markets"}`,
		"unknown skill": `{"skill":"nope"}`,
		"no query":      `{"skill":"svpchain-market-data"}`,
		"unknown query": `{"skill":"svpchain-market-data","query":"nope"}`,
		"bad side":      `{"skill":"svpchain-market-data","query":"estimate","ticker":"BTC-USD","side":"sideways","size":"1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.handle(context.Background(), execCtxFor(raw)); err == nil {
				t.Errorf("%s: expected an error", name)
			}
		})
	}
}

func TestEstimateFlowsThroughToTheAuctionMath(t *testing.T) {
	e := newTestExecutor()
	// Book is 5@100; a size-2 buy clears at exactly 100 with no slippage.
	out, err := e.handle(context.Background(),
		execCtxFor(`{"skill":"svpchain-market-data","query":"estimate","ticker":"BTC-USD","side":"buy","size":"2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"average_price":"100"`) {
		t.Errorf("estimate did not reach the auction math: %s", out)
	}
	if !strings.Contains(out, `"fully_filled":true`) {
		t.Errorf("size-2 order against a 5-deep book should fully fill: %s", out)
	}
}
