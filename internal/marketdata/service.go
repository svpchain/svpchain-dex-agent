package marketdata

import (
	"context"
	"fmt"

	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
)

// Service answers the read-layer questions from public indexer data.
//
// It owns an indexer client and nothing else — no keys, no chain connection, no
// delegation state. That is the whole point of the read layer: it is safe to
// run and expose without any of the trust machinery the execution layer needs,
// so it can go live, and be paid for over x402, before delegation exists.
type Service struct {
	idx MarketReader
}

// MarketReader is the slice of the indexer client the read layer needs.
//
// An interface, not the concrete client, so the service can be tested against a
// snapshot without reaching a live indexer — and so nothing here can quietly
// start depending on a heavier part of the client than the read layer should.
type MarketReader interface {
	ListPerpetualMarkets(ctx context.Context) (*indexer.PerpetualMarketsResponse, error)
	GetPerpetualMarket(ctx context.Context, ticker string) (*indexer.PerpetualMarket, error)
	GetOrderbook(ctx context.Context, ticker string) (*indexer.Orderbook, error)
	GetHistoricalFunding(ctx context.Context, ticker string) (*indexer.HistoricalFundingResponse, error)
}

// NewService wraps a market reader.
func NewService(idx MarketReader) *Service {
	return &Service{idx: idx}
}

// Markets lists the tradable perpetual markets.
func (s *Service) Markets(ctx context.Context) (*indexer.PerpetualMarketsResponse, error) {
	return s.idx.ListPerpetualMarkets(ctx)
}

// Market returns one market's summary — oracle price, funding, margin
// fractions, 24h stats.
func (s *Service) Market(ctx context.Context, ticker string) (*indexer.PerpetualMarket, error) {
	if ticker == "" {
		return nil, fmt.Errorf("ticker is required")
	}
	return s.idx.GetPerpetualMarket(ctx, ticker)
}

// Orderbook returns the resting book for a market.
func (s *Service) Orderbook(ctx context.Context, ticker string) (*indexer.Orderbook, error) {
	if ticker == "" {
		return nil, fmt.Errorf("ticker is required")
	}
	return s.idx.GetOrderbook(ctx, ticker)
}

// Funding returns the historical funding rates for a market.
func (s *Service) Funding(ctx context.Context, ticker string) (*indexer.HistoricalFundingResponse, error) {
	if ticker == "" {
		return nil, fmt.Errorf("ticker is required")
	}
	return s.idx.GetHistoricalFunding(ctx, ticker)
}

// EstimateClearing prices a marketable order against the current book.
//
// This is the read layer's one substantive computation: it fetches the book and
// walks it, so a caller weighing a trade sees the size-weighted price and how
// far it would push, not just the touch.
func (s *Service) EstimateClearing(ctx context.Context, ticker string, side Side, size string) (*ClearingEstimate, error) {
	if ticker == "" {
		return nil, fmt.Errorf("ticker is required")
	}
	ob, err := s.idx.GetOrderbook(ctx, ticker)
	if err != nil {
		return nil, fmt.Errorf("fetch orderbook for %s: %w", ticker, err)
	}
	return EstimateClearing(ticker, side, ob, size)
}
