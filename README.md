# svpchain-dex-agent

An [A2A](https://a2aproject.github.io/A2A/) agent for the SVP-Chain perpetuals
DEX. Its skills cover the full svpchain-mcp tool surface — market data,
accounts, unsigned tx building, broadcast, self-service auth, faucet, EVM
(swap/bridge/ERC), Lendora — plus the chain's `x/agent` / `x/agentwallet`
modules and **live delegated execution** under
[SVP-DT](https://github.com/svpchain/svpdt) credentials.

## Skills

The Agent Card (`/.well-known/agent-card.json`) advertises twelve skills;
each skill's description enumerates its tools. Requests go to `/invoke`
(JSON-RPC) with an envelope naming a skill, a tool, and the tool's arguments
(exactly the MCP input schema):

```json
{"skill":"svpchain-trading","tool":"build_place_limit_order","args":{"subaccount_number":0,"ticker":"BTC-USD","side":"BUY","size":"0.1","price":"60000"},"bearer":"…"}
```

| Skill | What it serves |
|---|---|
| `svpchain-market-data` | markets, orderbooks, candles, trades, funding, oracle price, plus the legacy `query` form and the batch-auction clearing estimate |
| `svpchain-account` | subaccounts, balances, orders, fills, transfers, PnL, funding payments |
| `svpchain-trading` | unsigned order txs (limit/market/conditional/cancel/batch) |
| `svpchain-funds` | unsigned deposits/withdrawals/transfers + transfer-out caps |
| `svpchain-broadcast` | `broadcast_signed_tx`, `get_tx_status` |
| `svpchain-auth` | `auth_challenge` → wallet-sign → `auth_verify` → bearer token |
| `svpchain-faucet` | testnet faucet |
| `svpchain-evm` | raw EVM broadcast, swaps, bridge deposits, ERC-20/721 |
| `svpchain-lendora` | money-market reads + unsigned supply/borrow/repay txs |
| `svpchain-agent-registry` | `x/agent` queries + unsigned register/bond/deregister txs |
| `svpchain-delegation` | `x/agentwallet` queries + unsigned delegation-lifecycle txs |
| `svpchain-execution` | delegated execution (below) + `agent_identity` / `agent_self_register` |

**Auth.** Most tools require a bearer minted by the self-service flow: call
`auth_challenge` with your owner address, sign the challenge with your wallet
key, call `auth_verify`. The bearer rides the `Authorization: Bearer` header,
the envelope's `bearer` field, or is bound to the A2A `contextId` by
`auth_verify` automatically. Build tools always pin the tx signer to the
authenticated owner — a caller cannot build a tx for someone else's account.

**Write flow.** The agent holds no caller keys: `build_*` tools return an
unsigned `TxPayload` the caller signs with its own signer (e.g.
svpchain-signer-mcp) and lands via `broadcast_signed_tx`.

## Delegated execution

The one place the agent signs on its own: `execute_place_order`,
`execute_cancel_order`, `execute_batch_cancel`, and
`execute_deposit_to_subaccount` take an SVP-DT delegation proof (the base64
token chain, root first) plus the operation's parameters. The agent

1. verifies the chain with `svpdt.VerifyChain` — signatures against the
   registered keys in `x/agent`, linkage, monotonicity, expiry, depth, and
   that the leaf is addressed to *this* agent's DID — with ceilings read from
   the chain's own `x/agentwallet` params;
2. pre-flights the caveats (action granted, subaccount inside the grant);
3. builds the inner message **for the credential's principal** (never a
   caller-chosen owner), wraps it in `MsgAgentExecDelegated`, signs as the
   registered operator, and broadcasts.

The position lands on the delegator's subaccount; the chain re-verifies
everything against live state (epoch, revocation, nonce, budget) in its
AnteHandler. Wrapped short-term orders ride the chain's gas-free route;
deposits pay gas from the operator's account.

Delegated deposits require the `sending.deposit_to_subaccount` action in the
credential and the on-chain delegation. They can only move the **delegator's
own wallet USDC into the delegator's own subaccount** (the chain refuses any
other sender), the amount debits the delegation budget exactly like an
order's notional, and the agent additionally applies its local
`deposit_max_usdc` per-tx cap.

Requires an `[operator]` key. `agent_self_register` (gated to the operator
itself) registers the agent on chain with `agent_id = did:svp:<operator>`.
After any deploy that changes the served card (adding an execute op does),
run `agent_self_update` so the on-chain capability hash matches the card.

**Agent chain.** By default the DEX chain itself carries `x/agent` +
`x/agentwallet`. When it doesn't, an optional `[agent_chain]` section
(`id`, `rest_url`) points the agent-identity families — registry and
delegation queries/builds, self-registration, and delegated execution — at
the chain that does, over its Cosmos REST API (the gRPC-gateway, typically
`:1317`). Caller-signed registry/delegation txs then land via
`broadcast_agent_chain_tx` (registered under the agent-registry skill), since
`broadcast_signed_tx` targets the DEX chain. Note delegated orders execute on
whichever chain verifies the delegation, so a split deployment trades against
the agent chain's CLOB.

## Running

Full mode:

```sh
go run ./cmd/svpchain-dex-agent -config agent.toml   # see agent.toml.example
```

Read-only mode (the original thin shell — no chain connection, no keys):

```sh
go run ./cmd/svpchain-dex-agent --indexer-url https://indexer.example.com --listen :8081
```

`/healthz` answers load-balancer liveness checks.

## Deployment

`scripts/dex-agent-deploy.sh` installs the agent onto a remote SSH host as a
docker container, mirroring svpchain-mcp's deploy script: build (vendored, so
the `../svpagent/protocol` replace never leaves the operator) → `docker save`
(cached by image id) → rsync `agent.toml` + compose file (+ `operator.key`
when given, mode 600) → `docker load` → `docker compose up -d` → smoke test
(`/healthz` + agent card over loopback via ssh).

```sh
# Keyless (execution advertised but refused)
./scripts/dex-agent-deploy.sh --host www@svpdev1.example.com

# With delegated execution enabled
./scripts/dex-agent-deploy.sh --host www@svpdev1.example.com \
  --operator-key-file ./operator.key \
  --public-url https://dex-agent.svpchain.org

# Inspect / tear down
./scripts/dex-agent-deploy.sh --print-config --host …
./scripts/dex-agent-deploy.sh --dry-run --host … 
./scripts/dex-agent-deploy.sh --uninstall --host …
```

The remote needs only docker + the compose v2 plugin reachable by the ssh
user without sudo. Run `--help` for the full flag list (chain endpoints, EVM
family, faucet, limits, bridge routes — same knobs and defaults as the MCP
deploy). A config-schema test (`internal/config/deploy_script_test.go`) pins
the script's rendered `agent.toml` to what `internal/config` actually parses.

## Development

Dependencies with sharp edges:

- `github.com/dydxprotocol/v4-chain/protocol` is consumed from a sibling
  checkout via `replace => ../svpagent/protocol` (the branch carrying
  `x/agent` + `x/agentwallet`); the fork replace blocks in `go.mod` are copied
  verbatim from `protocol/go.mod` and must stay in sync. Point the replace at
  `../svpchain-main/protocol` once that branch merges.
- `github.com/svpchain/svpdt` is a tagged module and must **never** be
  replaced (its own tests enforce this).
- `github.com/svpchain/svpchain-mcp` is consumed at a tagged release; the
  `lib/mcp/tools` handlers are bridged one-to-one into A2A operations by
  `internal/toolbridge` (a completeness test pins the 64-tool table).

`go test ./...` runs everything, including an HTTP-level end-to-end smoke and
a delegated-execution pipeline test that mints real SVP-DT credentials
against fakes.
