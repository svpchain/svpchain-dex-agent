// Package marketdata is the read layer of the DEX agent: everything it can
// answer from public indexer data, with no credential, no svpchain account, and
// no dependency on any on-chain delegation module.
//
// The one piece of domain logic that belongs here — and the only one the thin
// shell is allowed to carry — is a batch-auction clearing-price estimate. A
// market that clears by uniform-price batch auction rather than continuous
// matching cannot be reasoned about with a single top-of-book price, so an
// agent deciding whether to trade needs an estimate of the price a given size
// would clear at. Nothing else here is opinionated; it reshapes indexer data.
package marketdata

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
)

// Side is which way a taker order crosses the book.
type Side string

const (
	// Buy crosses the asks, cheapest first.
	Buy Side = "buy"
	// Sell crosses the bids, highest first.
	Sell Side = "sell"
)

// ClearingEstimate is what a marketable order of a given size would clear at,
// walking the resting book. It is an estimate, not a quote: it reads a snapshot
// of the book and assumes it does not move, which is exactly wrong in the case
// that matters — a large order in a thin book — so the fields that expose that
// thinness are as important as the price.
type ClearingEstimate struct {
	Ticker string `json:"ticker"`
	Side   Side   `json:"side"`

	// RequestedSize is the base size asked about.
	RequestedSize string `json:"requested_size"`

	// FilledSize is how much the visible book could absorb. When it is less
	// than RequestedSize the book is too thin to fill the order, and the
	// clearing price describes only the portion that would fill.
	FilledSize string `json:"filled_size"`

	// FullyFilled is false when the book ran out before the size did. A caller
	// that ignores this and reads only the price will badly misjudge a large
	// order in a thin market — the estimate would look good precisely because
	// only the cheap top of book was counted.
	FullyFilled bool `json:"fully_filled"`

	// AveragePrice is the size-weighted average across the levels consumed:
	// what the whole filled portion costs per unit, which is the number a
	// trader actually pays, not the touch.
	AveragePrice string `json:"average_price"`

	// WorstPrice is the price of the last level consumed — the far edge of the
	// walk, and the honest measure of how far the order pushed into the book.
	WorstPrice string `json:"worst_price"`

	// SlippageBps is the average price's distance from the touch, in basis
	// points. Zero when the order fits entirely at the best level.
	SlippageBps string `json:"slippage_bps"`
}

// EstimateClearing walks an orderbook snapshot to price a marketable order.
//
// Prices come off the indexer as decimal strings, so the arithmetic is done in
// big.Rat rather than float64: a basis-point slippage figure a trader relies on
// must not carry binary-float rounding, and sizes here can span many orders of
// magnitude.
func EstimateClearing(ticker string, side Side, ob *indexer.Orderbook, size string) (*ClearingEstimate, error) {
	want, ok := new(big.Rat).SetString(size)
	if !ok || want.Sign() <= 0 {
		return nil, fmt.Errorf("size %q is not a positive decimal", size)
	}

	levels, err := restingLevels(side, ob)
	if err != nil {
		return nil, err
	}
	if len(levels) == 0 {
		return nil, fmt.Errorf("no resting %s liquidity for %s", oppositeName(side), ticker)
	}

	touch := levels[0].price
	remaining := new(big.Rat).Set(want)
	cost := new(big.Rat)   // Σ price·size consumed
	filled := new(big.Rat) // Σ size consumed
	worst := levels[0].price

	for _, lvl := range levels {
		if remaining.Sign() <= 0 {
			break
		}
		take := lvl.size
		if take.Cmp(remaining) > 0 {
			take = new(big.Rat).Set(remaining)
		}
		cost.Add(cost, new(big.Rat).Mul(lvl.price, take))
		filled.Add(filled, take)
		remaining.Sub(remaining, take)
		worst = lvl.price
	}

	if filled.Sign() == 0 {
		return nil, fmt.Errorf("no resting %s liquidity for %s", oppositeName(side), ticker)
	}

	avg := new(big.Rat).Quo(cost, filled)

	// Slippage is signed away from the touch in the direction that costs the
	// taker: a buy that clears above the touch, a sell that clears below.
	diff := new(big.Rat).Sub(avg, touch)
	if side == Sell {
		diff.Neg(diff)
	}
	slippageBps := new(big.Rat).Quo(new(big.Rat).Mul(diff, big.NewRat(10_000, 1)), touch)

	return &ClearingEstimate{
		Ticker:        ticker,
		Side:          side,
		RequestedSize: size,
		FilledSize:    ratString(filled),
		FullyFilled:   remaining.Sign() <= 0,
		AveragePrice:  ratString(avg),
		WorstPrice:    ratString(worst),
		SlippageBps:   ratString(slippageBps),
	}, nil
}

type level struct {
	price *big.Rat
	size  *big.Rat
}

// restingLevels returns the side of the book a taker crosses, sorted in the
// order it would consume them: asks ascending for a buy, bids descending for a
// sell. The indexer does not promise an order, so this sorts rather than trusts
// the snapshot — an unsorted walk would price the order against the wrong
// levels and quietly report a better fill than exists.
func restingLevels(side Side, ob *indexer.Orderbook) ([]level, error) {
	var raw []indexer.OrderbookPriceLevel
	switch side {
	case Buy:
		raw = ob.Asks
	case Sell:
		raw = ob.Bids
	default:
		return nil, fmt.Errorf("unknown side %q", side)
	}

	out := make([]level, 0, len(raw))
	for _, r := range raw {
		p, okP := new(big.Rat).SetString(r.Price)
		s, okS := new(big.Rat).SetString(r.Size)
		if !okP || !okS {
			return nil, fmt.Errorf("malformed price level %q@%q", r.Size, r.Price)
		}
		// A zero or negative level is not liquidity; skipping beats letting it
		// distort the weighted average.
		if p.Sign() <= 0 || s.Sign() <= 0 {
			continue
		}
		out = append(out, level{price: p, size: s})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if side == Buy {
			return out[i].price.Cmp(out[j].price) < 0
		}
		return out[i].price.Cmp(out[j].price) > 0
	})
	return out, nil
}

func oppositeName(side Side) string {
	if side == Buy {
		return "ask"
	}
	return "bid"
}

// ratString renders a rational as a plain decimal, trimming trailing zeros so
// the output reads like a price rather than a fixed-precision artifact.
func ratString(r *big.Rat) string {
	s := r.FloatString(18)
	// FloatString always emits 18 fractional digits; trim to something a human
	// and a JSON consumer both accept, without dropping significant figures.
	return trimDecimal(s)
}

func trimDecimal(s string) string {
	if !containsDot(s) {
		return s
	}
	end := len(s)
	for end > 0 && s[end-1] == '0' {
		end--
	}
	if end > 0 && s[end-1] == '.' {
		end--
	}
	return s[:end]
}

func containsDot(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return true
		}
	}
	return false
}
