# svpchain-dex-agent

The read layer of an SVP-Chain perpetuals DEX agent: an [A2A](https://a2aproject.github.io/A2A/)
service that answers market-data questions from the public indexer.

It is a **thin shell** by design. It holds no keys, opens no chain connection,
and depends on no on-chain delegation module — so it can be deployed, and billed
per call over x402, before any execution or delegation machinery exists.

## Two skills

Its Agent Card advertises two skills, and the split is the point:

- **`svpchain-market-data`** (`required: false`) — read-only market intelligence:
  perpetual markets, live orderbooks, historical funding, and a **batch-auction
  clearing-price estimate** for a given order size. Needs no credential and no
  account.
- **`svpchain-execution`** (`required: true`) — place and cancel orders under an
  SVP-DT delegation credential, with the position landing on the *user's*
  subaccount. **Advertised but not yet served**: it is refused with a reason, so
  a caller learns the requirement rather than meeting silence.

## Running

```sh
go run ./cmd/svpchain-dex-agent --indexer-url https://indexer.example.com/v4 --listen :8081
```

The Agent Card is served at `/.well-known/agent-card.json`; requests go to
`/invoke` (JSON-RPC). A request names a skill and its parameters:

```json
{"skill":"svpchain-market-data","query":"estimate","ticker":"BTC-USD","side":"buy","size":"2.5"}
```

## The one piece of domain logic

The clearing-price estimate is the only opinionated computation here. A market
that clears by uniform-price batch auction cannot be judged from the touch, so
the estimate walks the resting book and reports the size-weighted average, the
worst price consumed, the slippage in basis points, and — most importantly —
whether the book was deep enough to fill at all. A large order in a thin book is
exactly the case a naive top-of-book read gets wrong.

## Status

The read layer is complete and tested. Not yet built: the execution layer
(inbound `VerifyChain` fail-closed, outbound `build_*` carrying a delegation
proof), on-chain identity (`RegisterAgent` + bond), and x402 payment gating —
each a documented seam rather than a stub.

## Development

`svpchain-mcp` (the indexer client) is consumed through a local `replace` while
it has no tagged release. Tag it before building this outside the workspace.
