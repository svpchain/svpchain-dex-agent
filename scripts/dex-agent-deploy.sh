#!/usr/bin/env bash
#
# scripts/dex-agent-deploy.sh — install the svpchain DEX agent
# (cmd/svpchain-dex-agent) onto a remote SSH host as a docker container.
# Mirrors svpchain-mcp/scripts/mcp-server-deploy.sh.
#
# Flow:
#   1. On operator: go mod vendor + docker build svpchain-dex-agent:<tag>
#      (linux/amd64). The vendored tree makes the context self-contained —
#      the go.mod replace to ../svpagent/protocol never leaves the operator.
#   2. On operator: docker save the image to a tar (cached by image id).
#   3. On operator → remote: rsync the tar + a rendered agent.toml
#      (+ operator.key when given) + docker-compose.yml to
#      ~/svpchain-dex-agent/ (the ssh user's home — no sudo needed).
#   4. On remote: docker load the tar (skipped when image id matches).
#   5. On remote: docker compose up -d (from the shipped compose file).
#   6. On remote (via ssh): GET /healthz + the agent card on 127.0.0.1 to
#      smoke-test. Loopback so --host can be a bare ~/.ssh/config alias.
#
# The remote host needs only docker + the compose v2 plugin (reachable by the
# ssh user without sudo, e.g. via the docker group) + sshd. No Go toolchain,
# no repo checkout, no sudo. Auth state (nonces / tenants / session bearers /
# withdraw ledger) is in-memory, so a redeploy cleanly wipes it; the
# transfer-out caps persist on the data volume.
#
# Required flags (or env equivalents):
#   --host user@hostname           SSH target.            SVPCHAIN_DEPLOY_HOST
#
# Optional flags:
#   --chain-id <id>                Default svp-2517-1.    SVPCHAIN_CHAIN_ID
#   --grpc-addr <h:p>              Default 127.0.0.1:9090. SVPCHAIN_GRPC_ADDR
#   --comet-rpc <url>              Default http://127.0.0.1:26657. SVPCHAIN_COMET_RPC
#   --indexer <url>                Default http://127.0.0.1:3002.  SVPCHAIN_INDEXER
#   --listen-port <port>           Default 8081.          SVPCHAIN_AGENT_LISTEN_PORT
#   --public-url <url>             Base URL advertised in the Agent Card.
#                                  Default https://agent-testnet.svpchain.org.
#                                  Set it to the real externally reachable URL
#                                  of THIS deployment — other agents call the
#                                  card's URL, so a wrong value makes the agent
#                                  discoverable but unreachable. A trailing
#                                  slash is stripped. SVPCHAIN_AGENT_PUBLIC_URL
#   --operator-key-file <path>     LOCAL file holding the operator's hex
#                                  eth_secp256k1 key. Shipped (mode 600) next to
#                                  agent.toml and referenced as key_file, turning
#                                  the svpchain-execution skill ON. Unset → the
#                                  agent runs keyless and execution refuses with
#                                  a reason. SVPCHAIN_AGENT_OPERATOR_KEY_FILE
#   --operator-capabilities <l>    Comma-separated capability tags for
#                                  registration. Default "trading".
#   --operator-metadata <s>        Free-form registration metadata. Default "".
#   --evm-rpc <url>                EVM JSON-RPC endpoint, enabling the EVM
#                                  family. Default http://127.0.0.1:8545.
#                                  Set "" to disable. SVPCHAIN_EVM_RPC
#   --faucet-url <url>             Faucet backend base URL. Default
#                                  https://pre-faucet.svpchain.org. Set "" to
#                                  disable. SVPCHAIN_FAUCET_URL
#   --evm-uniswap-router <0xaddr>  UniswapV2 router (with --evm-wsvp, both-or-
#                                  neither). Defaults to the known deployment;
#                                  "" disables swaps. SVPCHAIN_EVM_UNISWAP_ROUTER
#   --evm-wsvp <0xaddr>            Wrapped-native token. SVPCHAIN_EVM_WSVP
#   --evm-oracle <0xaddr>          Price-feed for get_oracle_price; "" disables.
#                                  SVPCHAIN_EVM_ORACLE
#   --evm-lendora-comptroller <0xaddr>
#                                  Lendora Comptroller, enabling lendora_*;
#                                  "" disables. SVPCHAIN_EVM_LENDORA_COMPTROLLER
#   --evm-bridge-addr <0xaddr>     SVPBridge contract; bridge is ON by default.
#                                  SVPCHAIN_EVM_BRIDGE
#   --evm-bridge-routes <path>     Route-registry path written into agent.toml
#                                  (relative → resolved against the config dir).
#                                  Default "routes.json"; "" disables the bridge.
#                                  SVPCHAIN_EVM_BRIDGE_ROUTES
#   --evm-bridge-routes-src <path> Optional LOCAL registry file overriding the
#                                  generated one. SVPCHAIN_EVM_BRIDGE_ROUTES_SRC
#   --evm-bridge-source-chain-id <n>  Default 2517. SVPCHAIN_EVM_BRIDGE_SOURCE_CHAIN_ID
#   --evm-foreign-chains <list>    Inbound source chains: ";"-separated
#                                  "chainId,rpcUrl,bridgeAddr" triples. Defaults
#                                  wire arbitrum_sepolia + sepolia; "" disables
#                                  inbound. SVPCHAIN_EVM_FOREIGN_CHAINS
#   --install-dir <path>           Default ~/svpchain-dex-agent on remote.
#   --image-tag <tag>              Default <git-short-sha>.
#   --platform <p>                 Default linux/amd64. Override for ARM.
#   --container-name <name>        Default svpchain-dex-agent.
#   --deposit-max-usdc <n>         Funds caps, copied into [limits].
#   --withdraw-max-usdc <n>
#   --transfer-max-usdc <n>
#   --daily-withdraw-cap-usdc <n>
#   --markets-refresh <dur>        Default 30s (e.g. "60s", "2m").
#   --skip-build                   Reuse an existing local image.
#   --print-config                 Render agent.toml to stdout and exit.
#   --print-routes                 Render the bridge route registry and exit.
#   --dry-run                      Print every command; touch nothing.
#   --uninstall                    Remove the container + image + dir on remote.
#   -h|--help                      This help.
#
# Examples:
#   # First deploy (keyless — execution advertised but refused)
#   ./scripts/dex-agent-deploy.sh --host www@svpdev1.example.com
#
#   # With delegated execution enabled
#   ./scripts/dex-agent-deploy.sh --host www@svpdev1.example.com \
#     --operator-key-file ./operator.key \
#     --public-url https://dex-agent.svpchain.org
#
#   # Tear down
#   ./scripts/dex-agent-deploy.sh --uninstall --host www@svpdev1.example.com
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

fail() { printf "  ${C_RED}✗${C_RESET} %s\n" "$*" >&2; exit 1; }

# ---- args ------------------------------------------------------------------

mode="install"        # install | uninstall | print-config | print-routes

host=""
chain_id="${SVPCHAIN_CHAIN_ID:-svp-2517-1}"
grpc_addr="${SVPCHAIN_GRPC_ADDR:-127.0.0.1:9090}"
comet_rpc="${SVPCHAIN_COMET_RPC:-http://127.0.0.1:26657}"
indexer="${SVPCHAIN_INDEXER:-http://127.0.0.1:3002}"
listen_port="${SVPCHAIN_AGENT_LISTEN_PORT:-8081}"
public_url="${SVPCHAIN_AGENT_PUBLIC_URL:-https://agent-testnet.svpchain.org}"
operator_key_file="${SVPCHAIN_AGENT_OPERATOR_KEY_FILE:-}"
operator_capabilities="trading"
operator_metadata=""
evm_rpc="${SVPCHAIN_EVM_RPC:-http://127.0.0.1:8545}"
faucet_url="${SVPCHAIN_FAUCET_URL:-https://pre-faucet.svpchain.org}"
evm_uniswap_router="${SVPCHAIN_EVM_UNISWAP_ROUTER:-0xFe7bf2DFd5CB268C6779f1F614638a436Cb701e4}"
evm_wsvp="${SVPCHAIN_EVM_WSVP:-0x771a0a63D8198b7dbea4a16910ff68AB38006531}"
evm_oracle="${SVPCHAIN_EVM_ORACLE:-0xAE351F2dF66DF1A7d2eB0D7574BcDb909E680B56}"
evm_lendora_comptroller="${SVPCHAIN_EVM_LENDORA_COMPTROLLER:-0x0faBb2B5057b14224b04E4cbB217Dd6b275f75a7}"
evm_bridge_addr="${SVPCHAIN_EVM_BRIDGE:-0x78Aca10afd5b28E838ECf0De20c5621CE39D9F4a}"
evm_bridge_routes="${SVPCHAIN_EVM_BRIDGE_ROUTES:-routes.json}"
evm_bridge_routes_src="${SVPCHAIN_EVM_BRIDGE_ROUTES_SRC:-}"
evm_bridge_source_chain_id="${SVPCHAIN_EVM_BRIDGE_SOURCE_CHAIN_ID:-2517}"
evm_foreign_chains="${SVPCHAIN_EVM_FOREIGN_CHAINS:-421614,https://sepolia-rollup.arbitrum.io/rpc,0xB6c74A758E3fA7bf57c22037821f7cA974d0CdfD;11155111,https://ethereum-sepolia-rpc.publicnode.com,0xb9a9937006E886F0Ec145a19634426300dD20a64}"
install_dir="~/svpchain-dex-agent"
image_tag=""
platform="linux/amd64"
container_name="svpchain-dex-agent"
deposit_max=""
withdraw_max=""
transfer_max=""
daily_withdraw_cap=""
markets_refresh="30s"
skip_build="0"
dry_run="0"
bridge_routes_basename=""
bridge_routes_src_abs=""
operator_key_src_abs=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)                   host="$2";              shift 2 ;;
    --chain-id)               chain_id="$2";          shift 2 ;;
    --grpc-addr)              grpc_addr="$2";         shift 2 ;;
    --comet-rpc)              comet_rpc="$2";         shift 2 ;;
    --indexer)                indexer="$2";           shift 2 ;;
    --listen-port)            listen_port="$2";       shift 2 ;;
    --public-url)             public_url="$2";        shift 2 ;;
    --operator-key-file)      operator_key_file="$2"; shift 2 ;;
    --operator-capabilities)  operator_capabilities="$2"; shift 2 ;;
    --operator-metadata)      operator_metadata="$2"; shift 2 ;;
    --evm-rpc)                evm_rpc="$2";           shift 2 ;;
    --faucet-url)             faucet_url="$2";        shift 2 ;;
    --evm-uniswap-router)     evm_uniswap_router="$2"; shift 2 ;;
    --evm-wsvp)               evm_wsvp="$2";          shift 2 ;;
    --evm-oracle)             evm_oracle="$2";        shift 2 ;;
    --evm-lendora-comptroller) evm_lendora_comptroller="$2"; shift 2 ;;
    --evm-bridge-addr)        evm_bridge_addr="$2";   shift 2 ;;
    --evm-bridge-routes)      evm_bridge_routes="$2"; shift 2 ;;
    --evm-bridge-routes-src)  evm_bridge_routes_src="$2"; shift 2 ;;
    --evm-bridge-source-chain-id) evm_bridge_source_chain_id="$2"; shift 2 ;;
    --evm-foreign-chains)     evm_foreign_chains="$2"; shift 2 ;;
    --install-dir)            install_dir="$2";       shift 2 ;;
    --image-tag)              image_tag="$2";         shift 2 ;;
    --platform)               platform="$2";          shift 2 ;;
    --container-name)         container_name="$2";    shift 2 ;;
    --deposit-max-usdc)       deposit_max="$2";       shift 2 ;;
    --withdraw-max-usdc)      withdraw_max="$2";      shift 2 ;;
    --transfer-max-usdc)      transfer_max="$2";      shift 2 ;;
    --daily-withdraw-cap-usdc) daily_withdraw_cap="$2"; shift 2 ;;
    --markets-refresh)        markets_refresh="$2";   shift 2 ;;
    --skip-build)             skip_build="1";         shift ;;
    --print-config)           mode="print-config";    shift ;;
    --print-routes)           mode="print-routes";    shift ;;
    --dry-run)                dry_run="1";            shift ;;
    --uninstall)              mode="uninstall";       shift ;;
    -h|--help)
      sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed -n '/^#/p' | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) fail "unknown flag: $1" ;;
  esac
done

: "${host:=${SVPCHAIN_DEPLOY_HOST:-}}"

# Strip a trailing slash (from the flag or env) so the card's
# "<public_url>/invoke" join stays clean.
public_url="${public_url%/}"

# ---- shared helpers -------------------------------------------------------

# emit_foreign_chains — emit the [[evm_foreign_chain]] array-of-tables parsed
# from evm_foreign_chains (";"-separated "chainId,rpcUrl,bridgeAddr" triples).
emit_foreign_chains() {
  [[ -z "$evm_foreign_chains" ]] && return 0
  local triple cid rpc addr
  local saved_ifs="$IFS"
  IFS=';'
  for triple in $evm_foreign_chains; do
    IFS="$saved_ifs"
    [[ -z "$triple" ]] && continue
    IFS=',' read -r cid rpc addr <<<"$triple"
    if [[ -z "$cid" || -z "$rpc" || -z "$addr" ]]; then
      fail "--evm-foreign-chains: malformed triple \"$triple\" (want chainId,rpcUrl,bridgeAddr)"
    fi
    printf '\n[[evm_foreign_chain]]\n'
    printf 'chain_id    = %s\n' "$cid"
    printf 'rpc_url     = "%s"\n' "$rpc"
    printf 'bridge_addr = "%s"\n' "$addr"
    IFS=';'
  done
  IFS="$saved_ifs"
}

# emit_operator_capabilities — render the capabilities list as a TOML array.
emit_operator_capabilities() {
  local out="[" first=1 cap
  local saved_ifs="$IFS"; IFS=','
  for cap in $operator_capabilities; do
    [[ -z "$cap" ]] && continue
    [[ "$first" == "1" ]] || out+=", "
    out+="\"$cap\""
    first=0
  done
  IFS="$saved_ifs"
  out+="]"
  printf '%s' "$out"
}

# render_agent_toml — emit the operator-side agent.toml on stdout. listen_addr
# is always 0.0.0.0:<port> inside the container; --network host on the remote
# means that's also the host-bound port. Optional families mirror
# internal/config exactly: unset keys → those operations refuse at call time.
render_agent_toml() {
  cat <<EOF
# Auto-generated by scripts/dex-agent-deploy.sh — do not edit by hand.

chain_id         = "${chain_id}"
grpc_addr        = "${grpc_addr}"
comet_rpc_url    = "${comet_rpc}"
indexer_base_url = "${indexer}"
listen_addr      = "0.0.0.0:${listen_port}"
public_url       = "${public_url}"
broadcast_mode   = "server"
EOF
  [[ -n "$evm_rpc" ]]    && echo "evm_rpc_url             = \"${evm_rpc}\""
  [[ -n "$faucet_url" ]] && echo "faucet_base_url         = \"${faucet_url}\""
  # Persist per-symbol transfer-out caps on the writable data volume (the
  # config dir holds only read-only mounts) — see render_compose_yaml.
  echo "transfer_out_cap_path   = \"/var/lib/svpchain-dex-agent/transfer-out-caps.json\""
  [[ -n "$evm_uniswap_router" ]] && echo "evm_uniswap_router_addr = \"${evm_uniswap_router}\""
  [[ -n "$evm_wsvp" ]]           && echo "evm_wsvp_addr           = \"${evm_wsvp}\""
  [[ -n "$evm_oracle" ]]         && echo "evm_oracle_addr         = \"${evm_oracle}\""
  [[ -n "$evm_lendora_comptroller" ]] && echo "evm_lendora_comptroller_addr = \"${evm_lendora_comptroller}\""
  if [[ -n "$evm_bridge_addr" && -n "$evm_bridge_routes" && -n "$evm_bridge_source_chain_id" ]]; then
    echo "evm_bridge_addr             = \"${evm_bridge_addr}\""
    echo "evm_bridge_routes_path      = \"${evm_bridge_routes}\""
    echo "evm_bridge_source_chain_id  = ${evm_bridge_source_chain_id}"
    emit_foreign_chains
  elif [[ -n "$evm_bridge_routes" ]]; then
    echo "# WARNING: --evm-bridge-routes set but evm_bridge_addr / evm_bridge_source_chain_id are empty;" >&2
    echo "#          bridge omitted (config requires all three)." >&2
  fi
  cat <<EOF

[cache]
markets_refresh = "${markets_refresh}"
EOF
  if [[ -n "${deposit_max}${withdraw_max}${transfer_max}${daily_withdraw_cap}" ]]; then
    echo ""
    echo "[limits]"
    [[ -n "$deposit_max"        ]] && echo "deposit_max_usdc        = ${deposit_max}"
    [[ -n "$withdraw_max"       ]] && echo "withdraw_max_usdc       = ${withdraw_max}"
    [[ -n "$transfer_max"       ]] && echo "transfer_max_usdc       = ${transfer_max}"
    [[ -n "$daily_withdraw_cap" ]] && echo "daily_withdraw_cap_usdc = ${daily_withdraw_cap}"
  fi
  # The operator key turns delegated execution on. key_file is left relative
  # ("operator.key") on purpose — internal/config resolves it against the
  # agent.toml directory, so it points at the file mounted beside the config.
  if [[ -n "$operator_key_file" ]]; then
    cat <<EOF

[operator]
key_file     = "operator.key"
capabilities = $(emit_operator_capabilities)
metadata     = "${operator_metadata}"
EOF
  fi
}

# render_routes_json — the SVPBridge route registry, identical to the one the
# MCP deploy ships (the two services read the same whitelist). Zero addresses
# denote the native coin; decimals are the source asset's.
render_routes_json() {
  cat <<'ROUTES'
[
  {"srcChain":"arbitrum_sepolia","srcChainId":421614,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x0000000000000000000000000000000000000000","targetToken":"0x1c12dbda863900c680a3836c53d408feaf63f0ba","symbol":"WETH","decimals":18},
  {"srcChain":"arbitrum_sepolia","srcChainId":421614,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x7a8EcFa70374c1B8702CB98aaf23dE19675981d6","targetToken":"0x0000000000000000000000000000000000000000","symbol":"SVP","decimals":18},
  {"srcChain":"arbitrum_sepolia","srcChainId":421614,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0xc2bda8290a2e01984da81acf7e2d6ec9b14d7b10","targetToken":"0x8787384b8640f6e9c30e94585d3d62b03f80a5df","symbol":"WBNB","decimals":18},
  {"srcChain":"arbitrum_sepolia","srcChainId":421614,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0xd10d01ebf3cb825da77a025b1d861e7ae5370c20","targetToken":"0x6c22ceb0852bd7781b57574aaa5de0f22cd44162","symbol":"WBTC","decimals":8},
  {"srcChain":"arbitrum_sepolia","srcChainId":421614,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x75faf114eafb1bdbe2f0316df893fd58ce46aa4d","targetToken":"0x732f6ea7afd5edc02e7ba052075dd0780e285489","symbol":"USDC","decimals":6},
  {"srcChain":"arbitrum_sepolia","srcChainId":421614,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0xfa9857651febd22c0a76c958adb25b4af0370688","targetToken":"0x013a61e622e6abfcab64f52d274c3fc0aa37f951","symbol":"USDV","decimals":6},
  {"srcChain":"sepolia","srcChainId":11155111,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x0000000000000000000000000000000000000000","targetToken":"0x1c12dbda863900c680a3836c53d408feaf63f0ba","symbol":"WETH","decimals":18},
  {"srcChain":"sepolia","srcChainId":11155111,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x16B065D7519D5C1c53eff6ed5AE732E90d602A00","targetToken":"0x0000000000000000000000000000000000000000","symbol":"SVP","decimals":18},
  {"srcChain":"sepolia","srcChainId":11155111,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x1c7d4b196cb0c7b01d743fbc6116a902379c7238","targetToken":"0x732f6ea7afd5edc02e7ba052075dd0780e285489","symbol":"USDC","decimals":6},
  {"srcChain":"sepolia","srcChainId":11155111,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x93e719f5458d112804122952033103f2eb349eac","targetToken":"0x013a61e622e6abfcab64f52d274c3fc0aa37f951","symbol":"USDV","decimals":6},
  {"srcChain":"sepolia","srcChainId":11155111,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0x9d45d6a420fbaf77a46a4822ef967d62a69dc7f8","targetToken":"0x6c22ceb0852bd7781b57574aaa5de0f22cd44162","symbol":"WBTC","decimals":8},
  {"srcChain":"sepolia","srcChainId":11155111,"targetChain":"svp_chain","targetChainId":2517,"srcToken":"0xf174007a92ae5cdfecfa85c94c5105e4851734d6","targetToken":"0x8787384b8640f6e9c30e94585d3d62b03f80a5df","symbol":"WBNB","decimals":18},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"arbitrum_sepolia","targetChainId":421614,"srcToken":"0x0000000000000000000000000000000000000000","targetToken":"0x7a8EcFa70374c1B8702CB98aaf23dE19675981d6","symbol":"SVP","decimals":18},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"arbitrum_sepolia","targetChainId":421614,"srcToken":"0x013a61e622e6abfcab64f52d274c3fc0aa37f951","targetToken":"0xfa9857651febd22c0a76c958adb25b4af0370688","symbol":"USDV","decimals":6},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"arbitrum_sepolia","targetChainId":421614,"srcToken":"0x1c12dbda863900c680a3836c53d408feaf63f0ba","targetToken":"0x0000000000000000000000000000000000000000","symbol":"WETH","decimals":18},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"arbitrum_sepolia","targetChainId":421614,"srcToken":"0x6c22ceb0852bd7781b57574aaa5de0f22cd44162","targetToken":"0xd10d01ebf3cb825da77a025b1d861e7ae5370c20","symbol":"WBTC","decimals":8},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"arbitrum_sepolia","targetChainId":421614,"srcToken":"0x732f6ea7afd5edc02e7ba052075dd0780e285489","targetToken":"0x75faf114eafb1bdbe2f0316df893fd58ce46aa4d","symbol":"USDC","decimals":6},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"arbitrum_sepolia","targetChainId":421614,"srcToken":"0x8787384b8640f6e9c30e94585d3d62b03f80a5df","targetToken":"0xc2bda8290a2e01984da81acf7e2d6ec9b14d7b10","symbol":"WBNB","decimals":18},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"sepolia","targetChainId":11155111,"srcToken":"0x0000000000000000000000000000000000000000","targetToken":"0x16B065D7519D5C1c53eff6ed5AE732E90d602A00","symbol":"SVP","decimals":18},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"sepolia","targetChainId":11155111,"srcToken":"0x013a61e622e6abfcab64f52d274c3fc0aa37f951","targetToken":"0x93e719f5458d112804122952033103f2eb349eac","symbol":"USDV","decimals":6},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"sepolia","targetChainId":11155111,"srcToken":"0x1c12dbda863900c680a3836c53d408feaf63f0ba","targetToken":"0x0000000000000000000000000000000000000000","symbol":"WETH","decimals":18},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"sepolia","targetChainId":11155111,"srcToken":"0x6c22ceb0852bd7781b57574aaa5de0f22cd44162","targetToken":"0x9d45d6a420fbaf77a46a4822ef967d62a69dc7f8","symbol":"WBTC","decimals":8},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"sepolia","targetChainId":11155111,"srcToken":"0x732f6ea7afd5edc02e7ba052075dd0780e285489","targetToken":"0x1c7d4b196cb0c7b01d743fbc6116a902379c7238","symbol":"USDC","decimals":6},
  {"srcChain":"svp_chain","srcChainId":2517,"targetChain":"sepolia","targetChainId":11155111,"srcToken":"0x8787384b8640f6e9c30e94585d3d62b03f80a5df","targetToken":"0xf174007a92ae5cdfecfa85c94c5105e4851734d6","symbol":"WBNB","decimals":18}
]
ROUTES
}

# render_compose_yaml — emit the docker-compose.yml deployed alongside
# agent.toml in install_dir. Pins the exact image just built/loaded; volumes
# use absolute host paths so `docker compose up -d` works from any directory.
# The operator key (when shipped) is mounted read-only beside the config so
# the config-dir-relative key_file resolution finds it.
render_compose_yaml() {
  cat <<EOF
# Auto-generated by scripts/dex-agent-deploy.sh — do not edit by hand.
services:
  svpchain-dex-agent:
    image: ${image_ref}
    container_name: ${container_name}
    restart: unless-stopped
    # network_mode: host — listener binds to 0.0.0.0:${listen_port} (compose
    # \`ports:\` is ignored in host mode; the interface/port live in agent.toml).
    network_mode: host
    volumes:
      - ${install_dir}/agent.toml:/etc/svpchain-dex-agent/agent.toml:ro
      - ${install_dir}/data:/var/lib/svpchain-dex-agent
EOF
  # Explicit ifs, not `[[ … ]] && echo`: a false test as the function's last
  # command would make the whole function return non-zero, and under
  # `set -e` the `render_compose_yaml > file` call site exits the script
  # silently (bit us when --evm-bridge-routes "" disabled the bridge).
  if [[ -n "$operator_key_src_abs" ]]; then
    echo "      - ${install_dir}/operator.key:/etc/svpchain-dex-agent/operator.key:ro"
  fi
  if [[ -n "$bridge_routes_basename" ]]; then
    echo "      - ${install_dir}/${bridge_routes_basename}:/etc/svpchain-dex-agent/${bridge_routes_basename}:ro"
  fi
}

require_install_args() {
  [[ -n "$host" ]] || fail "--host is required (or set SVPCHAIN_DEPLOY_HOST)"
}

# resolve_remote_install_dir — expand a leading ~ in $install_dir to the
# remote $HOME (docker bind-mounts need absolute host paths).
resolve_remote_install_dir() {
  case "$install_dir" in
    "~"|"~/"*)
      [[ "$dry_run" == "1" ]] && return 0
      local home
      home="$(ssh -o BatchMode=yes "$host" 'printf %s "$HOME"')" \
        || fail "could not resolve remote \$HOME on $host"
      [[ -n "$home" ]] || fail "remote \$HOME is empty on $host"
      install_dir="${home}${install_dir#\~}"
      ;;
  esac
}

run_or_print() {
  if [[ "$dry_run" == "1" ]]; then
    printf "  [dry-run] %s\n" "$*"
  else
    eval "$@"
  fi
}

remote_exec() {
  run_or_print "ssh -o BatchMode=yes '$host' $(printf '%q ' "$@")"
}

remote_image_id() {
  local img="$1"
  if [[ "$dry_run" == "1" ]]; then
    echo ""
    return
  fi
  ssh -o BatchMode=yes "$host" "docker image inspect --format '{{.Id}}' $img 2>/dev/null || true"
}

local_image_id() {
  docker image inspect --format '{{.Id}}' "$1" 2>/dev/null || true
}

# save_if_changed IMG TAR — docker save IMG to TAR, skipped when TAR.id
# already matches the current image id.
save_if_changed() {
  local img="$1" tar="$2" id
  if [[ "$dry_run" == "1" ]]; then
    info "[dry-run] would docker save $img → $(basename "$tar") (if image id changed)"
    run_or_print "docker save -o '$tar' '$img'"
    return 0
  fi
  id="$(local_image_id "$img")"
  [[ -n "$id" ]] || fail "image $img not found locally; build failed?"
  if [[ -f "$tar" && -f "${tar}.id" && "$(cat "${tar}.id")" == "$id" ]]; then
    info "$img unchanged — skipping save"
    return 0
  fi
  info "$img → $(basename "$tar")"
  run_or_print "docker save -o '$tar' '$img'"
  echo "$id" > "${tar}.id"
}

# load_if_missing IMG REMOTE_TAR EXPECTED_ID — docker load on the remote only
# when the remote doesn't already have IMG at EXPECTED_ID.
load_if_missing() {
  local img="$1" remote_tar="$2" expected_id="$3"
  local remote_id; remote_id="$(remote_image_id "$img")"
  if [[ "$remote_id" == "$expected_id" && -n "$expected_id" ]]; then
    info "$img already loaded on remote — skipping load"
    return 0
  fi
  remote_exec "docker load < $remote_tar"
}

# ---- mode: print-config ---------------------------------------------------

if [[ "$mode" == "print-config" ]]; then
  render_agent_toml
  exit 0
fi

# ---- mode: print-routes ---------------------------------------------------

if [[ "$mode" == "print-routes" ]]; then
  render_routes_json
  exit 0
fi

# ---- mode: uninstall ------------------------------------------------------

if [[ "$mode" == "uninstall" ]]; then
  [[ -n "$host" ]] || fail "--host is required (or set SVPCHAIN_DEPLOY_HOST)"
  step "svpchain-dex-agent uninstall on $host"
  resolve_remote_install_dir
  remote_exec "docker compose -f $install_dir/docker-compose.yml down 2>/dev/null || true"
  remote_exec "docker rm -f $container_name 2>/dev/null || true"
  remote_exec "sh -c 'docker images --format \"{{.Repository}}:{{.Tag}}\" svpchain-dex-agent 2>/dev/null | xargs -r docker rmi 2>/dev/null || true'"
  remote_exec "rm -rf $install_dir"
  step "Done"
  exit 0
fi

# ---- mode: install --------------------------------------------------------

require_install_args
require_cmd docker
require_cmd rsync
require_cmd ssh
require_cmd go

# Operator key shipping: resolve the local file against the operator's CWD
# now (before any cd) and require it to exist and look like a hex key.
if [[ -n "$operator_key_file" ]]; then
  if [[ "$operator_key_file" = /* ]]; then
    operator_key_src_abs="$operator_key_file"
  else
    operator_key_src_abs="$(pwd)/$operator_key_file"
  fi
  [[ -f "$operator_key_src_abs" ]] || fail "--operator-key-file '$operator_key_src_abs' was not found"
  if ! grep -Eq '^(0x)?[0-9a-fA-F]{64}[[:space:]]*$' "$operator_key_src_abs"; then
    fail "--operator-key-file '$operator_key_src_abs' does not look like a 32-byte hex key"
  fi
fi

# Bridge route shipping — same rules as the MCP deploy: with a RELATIVE routes
# path the registry is generated (or overridden via --evm-bridge-routes-src)
# and mounted next to agent.toml; an ABSOLUTE path is operator-managed.
if [[ -n "$evm_bridge_addr" && -n "$evm_bridge_routes" && -n "$evm_bridge_source_chain_id" ]]; then
  case "$evm_bridge_routes" in
    /*)
      info "bridge: evm_bridge_routes is absolute ($evm_bridge_routes) — not auto-shipping; ensure that path exists on $host."
      ;;
    *)
      bridge_routes_basename="$(basename "$evm_bridge_routes")"
      if [[ -n "$evm_bridge_routes_src" ]]; then
        if [[ "$evm_bridge_routes_src" = /* ]]; then
          bridge_routes_src_abs="$evm_bridge_routes_src"
        else
          bridge_routes_src_abs="$(pwd)/$evm_bridge_routes_src"
        fi
        [[ -f "$bridge_routes_src_abs" ]] || fail "--evm-bridge-routes-src '$bridge_routes_src_abs' was not found"
      fi
      ;;
  esac
fi

REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "$REPO_DIR"

if [[ -z "$image_tag" ]]; then
  if image_tag="$(git rev-parse --short HEAD 2>/dev/null)"; then :
  else image_tag="dev"; fi
fi
image_ref="svpchain-dex-agent:${image_tag}"
image_tar="${REPO_DIR}/build/dex-agent.image.tar"
mkdir -p "${REPO_DIR}/build"

step "Preflight (operator + remote)"
info "host=$host image=$image_ref platform=$platform"
info "install_dir=$install_dir container=$container_name"
info "listen_port=$listen_port public_url=$public_url"
if [[ -n "$operator_key_src_abs" ]]; then
  info "operator key: $operator_key_src_abs (delegated execution ON)"
else
  info "operator key: none (keyless — svpchain-execution refuses with a reason)"
fi
if [[ "$dry_run" != "1" ]]; then
  ssh -o BatchMode=yes "$host" "docker version --format '{{.Server.Version}}'" \
    >/dev/null 2>&1 \
    || fail "remote docker not reachable at $host without sudo (ssh keys ok? docker installed? ssh user in the docker group?)"
  ssh -o BatchMode=yes "$host" "docker compose version" >/dev/null 2>&1 \
    || fail "remote 'docker compose' (v2 plugin) not available at $host"
  pass "remote docker + compose reachable"
else
  info "[dry-run] skipping ssh-to-docker reachability check"
fi

resolve_remote_install_dir
info "install_dir=$install_dir"

# Phase 1: build (On operator)
step "On operator: docker build --platform $platform"
if [[ "$skip_build" == "1" ]]; then
  info "--skip-build: reusing existing local image $image_ref"
  [[ -n "$(local_image_id "$image_ref")" ]] || fail "image $image_ref not found locally; drop --skip-build"
else
  # Vendored build (see cmd/svpchain-dex-agent/Dockerfile): the go.mod replace
  # to ../svpagent/protocol resolves on the operator, and the vendored tree
  # makes the Docker context self-contained.
  run_or_print "go mod vendor"
  build_cmd="docker build --platform $platform"
  build_cmd+=" --build-arg VERSION=$image_tag"
  build_cmd+=" --build-arg COMMIT=$(git rev-parse HEAD 2>/dev/null || echo unknown)"
  build_cmd+=" -t $image_ref"
  build_cmd+=" -t svpchain-dex-agent:latest"
  build_cmd+=" -f cmd/svpchain-dex-agent/Dockerfile ."
  run_or_print "$build_cmd"
fi

# Phase 2: save (On operator)
step "On operator: docker save (cached by image id)"
save_if_changed "$image_ref" "$image_tar"
expected_id="$(cat "${image_tar}.id" 2>/dev/null || echo "")"

# Phase 3: ship config + compose + tar (On operator → remote)
step "On operator → remote: rsync config + image tar to $install_dir"
toml_tmp="$(mktemp -t svpchain-dex-agent.toml.XXXXXX)"
compose_tmp="$(mktemp -t svpchain-dex-agent.compose.XXXXXX)"
routes_tmp=""
key_tmp=""
trap 'rm -f "$toml_tmp" "$compose_tmp" "$routes_tmp" "$key_tmp"' EXIT
render_agent_toml > "$toml_tmp"
render_compose_yaml > "$compose_tmp"
bridge_routes_ship=""
if [[ -n "$bridge_routes_basename" ]]; then
  if [[ -n "$bridge_routes_src_abs" ]]; then
    bridge_routes_ship="$bridge_routes_src_abs"
  else
    routes_tmp="$(mktemp -t svpchain-dex-agent.routes.XXXXXX)"
    render_routes_json > "$routes_tmp"
    bridge_routes_ship="$routes_tmp"
  fi
fi
remote_exec "mkdir -p $install_dir $install_dir/data"
run_or_print "rsync -avz '$toml_tmp' '$host:$install_dir/agent.toml'"
[[ -n "$bridge_routes_ship" ]] && \
  run_or_print "rsync -avz '$bridge_routes_ship' '$host:$install_dir/$bridge_routes_basename'"
# The operator key is a secret: ship a 0600 temp copy — rsync -a preserves
# permissions, which is portable where --chmod=F600 is not (macOS's bundled
# openrsync rejects the octal form) — and pin them remotely as belt-and-braces
# for a pre-existing file.
if [[ -n "$operator_key_src_abs" ]]; then
  key_tmp="$(mktemp -t svpchain-dex-agent.key.XXXXXX)"
  run_or_print "cp '$operator_key_src_abs' '$key_tmp' && chmod 600 '$key_tmp'"
  run_or_print "rsync -avz '$key_tmp' '$host:$install_dir/operator.key'"
  remote_exec "chmod 600 $install_dir/operator.key"
fi
run_or_print "rsync -avz '$compose_tmp' '$host:$install_dir/docker-compose.yml'"
run_or_print "rsync -avz '$image_tar' '$host:$install_dir/dex-agent.image.tar'"

# Phase 4: load (On remote)
step "On remote: docker load (skipped if image already loaded)"
load_if_missing "$image_ref" "$install_dir/dex-agent.image.tar" "$expected_id"
remote_exec "docker tag $image_ref svpchain-dex-agent:latest"

# Phase 5: run (On remote)
step "On remote: docker compose up -d"
remote_exec "docker rm -f $container_name 2>/dev/null || true"
remote_exec "docker compose -f $install_dir/docker-compose.yml up -d"

# Phase 6: verify (On operator)
step "On remote: smoke test (healthz + agent card over loopback via ssh)"
if [[ "$dry_run" == "1" ]]; then
  info "[dry-run] would ssh $host curl -> http://127.0.0.1:$listen_port/healthz + agent card"
else
  # The agent dials the chain gRPC and must finish the initial markets-cache
  # refresh before serving; give it a few seconds to come up.
  healthy=""
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    code=$(ssh -o BatchMode=yes "$host" \
      "curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:${listen_port}/healthz" \
      2>/dev/null || echo 000)
    if [[ "$code" == "200" ]]; then healthy="1"; break; fi
    sleep 2
  done
  if [[ -z "$healthy" ]]; then
    info "healthz did not answer 200. Check container logs with:"
    info "  ssh $host 'docker logs $container_name --tail=80'"
    info "Common causes: gRPC/RPC endpoints in agent.toml not reachable from"
    info "the container (the markets cache must refresh once before serving)."
    fail "smoke test failed"
  fi
  pass "/healthz answered 200"
  skills=$(ssh -o BatchMode=yes "$host" \
    "curl -sS --max-time 5 http://127.0.0.1:${listen_port}/.well-known/agent-card.json" \
    2>/dev/null | { command -v jq >/dev/null 2>&1 && jq -r '.skills | length' || cat; } || echo "")
  if [[ "$skills" =~ ^[0-9]+$ ]]; then
    pass "agent card served ($skills skills)"
  else
    # jq may be missing on the operator — the card body already proves the
    # endpoint answers; don't fail the deploy over the count.
    info "agent card fetched (skill count unverified — jq not available)"
  fi
fi

step "Done — svpchain-dex-agent $image_tag running on $host"
