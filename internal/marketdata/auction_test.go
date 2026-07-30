package marketdata_test

import (
	"testing"

	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
)

func book(bids, asks [][2]string) *indexer.Orderbook {
	ob := &indexer.Orderbook{}
	for _, b := range bids {
		ob.Bids = append(ob.Bids, indexer.OrderbookPriceLevel{Price: b[0], Size: b[1]})
	}
	for _, a := range asks {
		ob.Asks = append(ob.Asks, indexer.OrderbookPriceLevel{Price: a[0], Size: a[1]})
	}
	return ob
}

func TestEstimateClearingBuyWalksTheAsks(t *testing.T) {
	// Asks: 5@100, 5@102. A buy for 8 takes all of the first level and 3 of the
	// second: cost = 5·100 + 3·102 = 806, avg = 806/8 = 100.75.
	ob := book(nil, [][2]string{{"100", "5"}, {"102", "5"}})

	est, err := marketdata.EstimateClearing("BTC-USD", marketdata.Buy, ob, "8")
	if err != nil {
		t.Fatal(err)
	}
	if est.AveragePrice != "100.75" {
		t.Errorf("average = %s, want 100.75", est.AveragePrice)
	}
	if est.WorstPrice != "102" {
		t.Errorf("worst = %s, want 102", est.WorstPrice)
	}
	if !est.FullyFilled {
		t.Error("order fit the book but FullyFilled is false")
	}
	if est.FilledSize != "8" {
		t.Errorf("filled = %s, want 8", est.FilledSize)
	}
	// Slippage: (100.75 - 100)/100 · 10000 = 75 bps.
	if est.SlippageBps != "75" {
		t.Errorf("slippage = %s bps, want 75", est.SlippageBps)
	}
}

func TestEstimateClearingSellWalksTheBidsHighestFirst(t *testing.T) {
	// A sell crosses the bids, best (highest) first. Bids given out of order to
	// prove the walk sorts rather than trusts the snapshot: 5@99, 5@100. A sell
	// for 8 takes 5@100 then 3@99: cost = 500 + 297 = 797, avg = 99.625.
	ob := book([][2]string{{"99", "5"}, {"100", "5"}}, nil)

	est, err := marketdata.EstimateClearing("BTC-USD", marketdata.Sell, ob, "8")
	if err != nil {
		t.Fatal(err)
	}
	if est.AveragePrice != "99.625" {
		t.Errorf("average = %s, want 99.625", est.AveragePrice)
	}
	if est.WorstPrice != "99" {
		t.Errorf("worst = %s, want 99", est.WorstPrice)
	}
	// A sell clears below the touch (100), and slippage is reported positive —
	// the cost is in the taker's direction: (100 - 99.625)/100 · 10000 = 37.5.
	if est.SlippageBps != "37.5" {
		t.Errorf("slippage = %s bps, want 37.5", est.SlippageBps)
	}
}

// ★ The case the estimate exists for: a large order in a thin book. The naive
// top-of-book read looks fine; the honest answer is "this did not fill".
func TestEstimateClearingReportsAThinBook(t *testing.T) {
	ob := book(nil, [][2]string{{"100", "2"}}) // only 2 available

	est, err := marketdata.EstimateClearing("BTC-USD", marketdata.Buy, ob, "10")
	if err != nil {
		t.Fatal(err)
	}
	if est.FullyFilled {
		t.Error("book had 2 but a size-10 order reports FullyFilled")
	}
	if est.FilledSize != "2" {
		t.Errorf("filled = %s, want 2 (all the book held)", est.FilledSize)
	}
	// The price describes only the filled portion, and a caller reading it
	// without FullyFilled would badly misjudge the order.
	if est.AveragePrice != "100" {
		t.Errorf("average = %s, want 100", est.AveragePrice)
	}
}

func TestEstimateClearingRejectsNonPositiveSize(t *testing.T) {
	ob := book(nil, [][2]string{{"100", "5"}})
	for _, bad := range []string{"0", "-1", "abc", ""} {
		if _, err := marketdata.EstimateClearing("BTC-USD", marketdata.Buy, ob, bad); err == nil {
			t.Errorf("size %q was accepted", bad)
		}
	}
}

func TestEstimateClearingRejectsAnEmptyBookSide(t *testing.T) {
	ob := book([][2]string{{"100", "5"}}, nil) // bids only
	if _, err := marketdata.EstimateClearing("BTC-USD", marketdata.Buy, ob, "1"); err == nil {
		t.Error("a buy against an empty ask side should error, not report a fill")
	}
}

// Zero and negative levels are not liquidity and must not distort the average.
func TestEstimateClearingIgnoresJunkLevels(t *testing.T) {
	ob := book(nil, [][2]string{{"0", "5"}, {"100", "5"}, {"-1", "5"}})

	est, err := marketdata.EstimateClearing("BTC-USD", marketdata.Buy, ob, "3")
	if err != nil {
		t.Fatal(err)
	}
	if est.AveragePrice != "100" {
		t.Errorf("average = %s, want 100 (junk levels ignored)", est.AveragePrice)
	}
}

// An exact-touch fill has no slippage.
func TestEstimateClearingAtTheTouchHasNoSlippage(t *testing.T) {
	ob := book(nil, [][2]string{{"100", "5"}})

	est, err := marketdata.EstimateClearing("BTC-USD", marketdata.Buy, ob, "3")
	if err != nil {
		t.Fatal(err)
	}
	if est.SlippageBps != "0" {
		t.Errorf("slippage = %s, want 0 at the touch", est.SlippageBps)
	}
}
