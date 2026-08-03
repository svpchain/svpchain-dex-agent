// Package config is the full-agent configuration, loaded from a TOML file.
//
// The schema is adapted from svpchain-mcp's cmd/mcp-server config: the same
// network endpoints, optional EVM/faucet/bridge/lendora families with the same
// all-or-nothing rules, and the same graceful degradation — an unset optional
// family means those operations refuse at call time with a reason, and the
// agent still boots. On top of that the agent adds its A2A identity
// (public_url) and an optional [operator] section holding the delegated
// signer's key reference; without it the execution skill refuses.
package config

import (
	"fmt"
	"path/filepath"
	"time"

	"cosmossdk.io/math"
	"github.com/BurntSushi/toml"
	"github.com/ethereum/go-ethereum/common"
)

// Config is the agent's configuration.
type Config struct {
	DEXChain   DEXChainConfig   `toml:"dex_chain"`
	AgentChain AgentChainConfig `toml:"agent_chain"`
	ListenAddr string           `toml:"listen_addr"`

	// PublicURL is how callers reach this agent, advertised in the Agent Card.
	// Defaults to "http://localhost"+ListenAddr when empty.
	PublicURL string `toml:"public_url"`

	// EVM binds the optional EVM operation families to their contract
	// deployments on the DEX chain. Every configured family requires
	// dex_chain.evm_rpc_url.
	EVM EVMConfig `toml:"evm"`

	// FaucetBaseURL is the faucet backend's HTTP base URL. Optional: when
	// empty the faucet operations refuse.
	FaucetBaseURL string `toml:"faucet_base_url"`

	// TransferOutCapPath persists per-symbol daily transfer-out caps and the
	// running tally to a JSON file. Optional: when empty the state is
	// in-memory only and resets on restart. Relative paths resolve against
	// the config file's directory.
	TransferOutCapPath string `toml:"transfer_out_cap_path"`

	// BroadcastMode is informational for whoami; the agent always broadcasts
	// the signed tx a caller lands via broadcast_signed_tx.
	BroadcastMode string `toml:"broadcast_mode"`

	Cache    CacheConfig  `toml:"cache"`
	Limits   LimitsConfig `toml:"limits"`
	Fee      FeeConfig    `toml:"fee"`
	Operator Operator     `toml:"operator"`
}

// DEXChainConfig points the agent at the DEX chain (an EVM-compatible
// cosmos-sdk chain): its chain id and the endpoints the chain-facing families
// share — queries and broadcast over gRPC, tx status over CometBFT RPC, reads
// over the Comlink indexer, and the chain's EVM JSON-RPC. All but the EVM
// endpoint are required.
type DEXChainConfig struct {
	ID             string `toml:"id"`
	GrpcAddr       string `toml:"grpc_addr"`
	CometRPCURL    string `toml:"comet_rpc_url"`
	IndexerBaseURL string `toml:"indexer_base_url"`

	// EVMRPCURL is the chain's EVM JSON-RPC endpoint. Optional: when empty
	// the EVM operation family refuses and non-EVM deployments keep booting.
	EVMRPCURL string `toml:"evm_rpc_url"`
}

// AgentChainConfig points the agent-identity families — x/agent registry,
// x/agentwallet delegation, and delegated execution — at the chain carrying
// those modules when it is not the DEX chain itself. The agent chain is
// reached over its Cosmos REST API (the gRPC-gateway, typically :1317), not
// gRPC. Optional: unset, those families run against the DEX chain connection
// (the single-chain default). Note delegated orders execute on whichever
// chain verifies the delegation, so a split deployment trades against the
// agent chain's CLOB.
type AgentChainConfig struct {
	ID      string `toml:"id"`
	RestURL string `toml:"rest_url"`
}

// Enabled reports whether a separate agent chain is configured.
func (a AgentChainConfig) Enabled() bool { return a.RestURL != "" }

// EVMConfig holds the per-protocol contract bindings on the DEX chain's EVM
// side. Each subtable is an independent optional family: left empty, its
// operations refuse at call time with a reason and the agent still boots.
type EVMConfig struct {
	Swap    SwapConfig    `toml:"swap"`
	Oracle  OracleConfig  `toml:"oracle"`
	Lendora LendoraConfig `toml:"lendora"`
	Bridge  BridgeConfig  `toml:"bridge"`
}

// SwapConfig binds the swap operations to a UniswapV2Router02 deployment and
// its wrapped-native token. Both-or-neither.
type SwapConfig struct {
	UniswapRouterAddr string `toml:"uniswap_router_addr"`
	WSVPAddr          string `toml:"wsvp_addr"`
}

// OracleConfig binds get_oracle_price to an AggregatorV3-style feed.
type OracleConfig struct {
	FeedAddr string `toml:"feed_addr"`
}

// LendoraConfig binds the lendora_* operations to a Lendora (Compound V2
// fork) Comptroller.
type LendoraConfig struct {
	ComptrollerAddr string `toml:"comptroller_addr"`
}

// BridgeConfig binds build_bridge_deposit to an SVPBridge deployment: the
// contract address, the route whitelist, and the DEX chain's own EVM chain
// id — all three together. ForeignChains declares the foreign EVM chains
// that can bridge INTO svpchain, backing build_bridge_deposit_inbound; they
// require the home bridge.
type BridgeConfig struct {
	Addr          string            `toml:"addr"`
	RoutesPath    string            `toml:"routes_path"`
	SourceChainID uint64            `toml:"source_chain_id"`
	ForeignChains []EVMForeignChain `toml:"foreign_chain"`
}

// Operator configures the agent's own on-chain identity: the eth_secp256k1
// key it signs delegated executions (and its own registration) with, and the
// registration metadata it advertises. Optional — without a key the
// svpchain-execution skill refuses with a reason, exactly like the other
// unconfigured families.
type Operator struct {
	// KeyFile is a file holding the operator private key as hex. The
	// DEX_AGENT_OPERATOR_KEY env var takes precedence when set, so
	// deployments can inject the key without touching disk.
	KeyFile string `toml:"key_file"`

	// Capabilities are the capability strings registered on chain.
	Capabilities []string `toml:"capabilities"`

	// Metadata is the free-form metadata registered on chain.
	Metadata string `toml:"metadata"`
}

// EVMForeignChain is one inbound source chain: its EVM chain id, its own
// JSON-RPC endpoint, and the SVPBridge address deployed on it.
type EVMForeignChain struct {
	ChainID    uint64 `toml:"chain_id"`
	RPCURL     string `toml:"rpc_url"`
	BridgeAddr string `toml:"bridge_addr"`
}

// FeeConfig sets the gas fee stamped onto non-CLOB txs. Short-term CLOB
// orders are gas-free on svpchain and always ship with an empty fee.
type FeeConfig struct {
	Denom    string `toml:"denom"`
	Amount   string `toml:"amount"`
	GasLimit uint64 `toml:"gas_limit"`
}

// LimitsConfig caps the size of funds movements, in human USDC. Zero
// disables the corresponding check.
type LimitsConfig struct {
	DepositMaxUSDC       uint64 `toml:"deposit_max_usdc"`
	WithdrawMaxUSDC      uint64 `toml:"withdraw_max_usdc"`
	TransferMaxUSDC      uint64 `toml:"transfer_max_usdc"`
	DailyWithdrawCapUSDC uint64 `toml:"daily_withdraw_cap_usdc"`
}

type CacheConfig struct {
	// MarketsRefresh is parsed as a Go duration string ("60s", "2m"…).
	// Zero means the package default in markets.NewCache.
	MarketsRefresh Duration `toml:"markets_refresh"`
}

// Duration parses TOML strings like "60s" into a time.Duration.
type Duration time.Duration

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", string(b), err)
	}
	*d = Duration(v)
	return nil
}

// Load reads and validates a TOML config file.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("decode TOML %s: %w", path, err)
	}
	c.ApplyDefaults()
	// Relative paths resolve against the config file's own directory, so the
	// "routes.json next to agent.toml" layout works regardless of where the
	// agent is launched from.
	if c.EVM.Bridge.RoutesPath != "" && !filepath.IsAbs(c.EVM.Bridge.RoutesPath) {
		c.EVM.Bridge.RoutesPath = filepath.Join(filepath.Dir(path), c.EVM.Bridge.RoutesPath)
	}
	if c.TransferOutCapPath != "" && !filepath.IsAbs(c.TransferOutCapPath) {
		c.TransferOutCapPath = filepath.Join(filepath.Dir(path), c.TransferOutCapPath)
	}
	if c.Operator.KeyFile != "" && !filepath.IsAbs(c.Operator.KeyFile) {
		c.Operator.KeyFile = filepath.Join(filepath.Dir(path), c.Operator.KeyFile)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// ApplyDefaults fills the fields a config may omit.
func (c *Config) ApplyDefaults() {
	if c.BroadcastMode == "" {
		c.BroadcastMode = "server"
	}
	if c.PublicURL == "" && c.ListenAddr != "" {
		c.PublicURL = "http://localhost" + c.ListenAddr
	}
	c.Fee.applyDefaults()
}

// Validate enforces the required network-level fields and the optional
// families' invariants.
func (c *Config) Validate() error {
	if c.DEXChain.ID == "" {
		return fmt.Errorf("dex_chain.id is required")
	}
	if c.DEXChain.GrpcAddr == "" {
		return fmt.Errorf("dex_chain.grpc_addr is required")
	}
	if c.DEXChain.CometRPCURL == "" {
		return fmt.Errorf("dex_chain.comet_rpc_url is required")
	}
	if c.DEXChain.IndexerBaseURL == "" {
		return fmt.Errorf("dex_chain.indexer_base_url is required")
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("listen_addr is required")
	}
	if (c.AgentChain.ID == "") != (c.AgentChain.RestURL == "") {
		return fmt.Errorf("agent_chain.id and agent_chain.rest_url must be set together")
	}
	if err := c.Fee.validate(); err != nil {
		return err
	}
	if err := c.validateSwap(); err != nil {
		return err
	}
	if err := c.validateOracle(); err != nil {
		return err
	}
	if err := c.validateLendora(); err != nil {
		return err
	}
	if err := c.validateBridge(); err != nil {
		return err
	}
	if err := c.validateForeignChains(); err != nil {
		return err
	}
	return nil
}

// validateBridge: contract address, routes path, and source chain id are
// all-or-nothing; the family requires an EVM RPC endpoint.
func (c *Config) validateBridge() error {
	set := 0
	if c.EVM.Bridge.Addr != "" {
		set++
	}
	if c.EVM.Bridge.RoutesPath != "" {
		set++
	}
	if c.EVM.Bridge.SourceChainID != 0 {
		set++
	}
	if set == 0 {
		return nil
	}
	if set != 3 {
		return fmt.Errorf("evm.bridge.addr, evm.bridge.routes_path and evm.bridge.source_chain_id must be set together")
	}
	if !common.IsHexAddress(c.EVM.Bridge.Addr) {
		return fmt.Errorf("evm.bridge.addr %q is not a valid 0x address", c.EVM.Bridge.Addr)
	}
	if c.DEXChain.EVMRPCURL == "" {
		return fmt.Errorf("dex_chain.evm_rpc_url is required when the bridge is configured")
	}
	return nil
}

// validateForeignChains: each entry needs a non-zero chain id, an RPC url,
// and a valid bridge address; ids must be unique and distinct from the home
// chain; any foreign chain requires the home bridge.
func (c *Config) validateForeignChains() error {
	if len(c.EVM.Bridge.ForeignChains) == 0 {
		return nil
	}
	if c.EVM.Bridge.Addr == "" {
		return fmt.Errorf("evm.bridge.foreign_chain requires the bridge to be configured (evm.bridge.addr, evm.bridge.routes_path, evm.bridge.source_chain_id)")
	}
	seen := map[uint64]bool{}
	for i, fc := range c.EVM.Bridge.ForeignChains {
		if fc.ChainID == 0 {
			return fmt.Errorf("evm.bridge.foreign_chain[%d] chain_id is required", i)
		}
		if fc.ChainID == c.EVM.Bridge.SourceChainID {
			return fmt.Errorf("evm.bridge.foreign_chain[%d] chain_id %d is the home chain (evm.bridge.source_chain_id)", i, fc.ChainID)
		}
		if seen[fc.ChainID] {
			return fmt.Errorf("evm.bridge.foreign_chain[%d] chain_id %d is declared more than once", i, fc.ChainID)
		}
		seen[fc.ChainID] = true
		if fc.RPCURL == "" {
			return fmt.Errorf("evm.bridge.foreign_chain[%d] (chain_id %d) rpc_url is required", i, fc.ChainID)
		}
		if !common.IsHexAddress(fc.BridgeAddr) {
			return fmt.Errorf("evm.bridge.foreign_chain[%d] (chain_id %d) bridge_addr %q is not a valid 0x address", i, fc.ChainID, fc.BridgeAddr)
		}
	}
	return nil
}

// validateOracle: valid 0x address when set; requires an EVM RPC endpoint.
func (c *Config) validateOracle() error {
	if c.EVM.Oracle.FeedAddr == "" {
		return nil
	}
	if !common.IsHexAddress(c.EVM.Oracle.FeedAddr) {
		return fmt.Errorf("evm.oracle.feed_addr %q is not a valid 0x address", c.EVM.Oracle.FeedAddr)
	}
	if c.DEXChain.EVMRPCURL == "" {
		return fmt.Errorf("dex_chain.evm_rpc_url is required when evm.oracle.feed_addr is set")
	}
	return nil
}

// validateLendora: valid 0x address when set; requires an EVM RPC endpoint.
func (c *Config) validateLendora() error {
	if c.EVM.Lendora.ComptrollerAddr == "" {
		return nil
	}
	if !common.IsHexAddress(c.EVM.Lendora.ComptrollerAddr) {
		return fmt.Errorf("evm.lendora.comptroller_addr %q is not a valid 0x address", c.EVM.Lendora.ComptrollerAddr)
	}
	if c.DEXChain.EVMRPCURL == "" {
		return fmt.Errorf("dex_chain.evm_rpc_url is required when evm.lendora.comptroller_addr is set")
	}
	return nil
}

// validateSwap: router + WSVP both-or-neither, valid 0x addresses, require
// an EVM RPC endpoint.
func (c *Config) validateSwap() error {
	if c.EVM.Swap.UniswapRouterAddr == "" && c.EVM.Swap.WSVPAddr == "" {
		return nil
	}
	if c.EVM.Swap.UniswapRouterAddr == "" || c.EVM.Swap.WSVPAddr == "" {
		return fmt.Errorf("evm.swap.uniswap_router_addr and evm.swap.wsvp_addr must be set together")
	}
	if !common.IsHexAddress(c.EVM.Swap.UniswapRouterAddr) {
		return fmt.Errorf("evm.swap.uniswap_router_addr %q is not a valid 0x address", c.EVM.Swap.UniswapRouterAddr)
	}
	if !common.IsHexAddress(c.EVM.Swap.WSVPAddr) {
		return fmt.Errorf("evm.swap.wsvp_addr %q is not a valid 0x address", c.EVM.Swap.WSVPAddr)
	}
	if c.DEXChain.EVMRPCURL == "" {
		return fmt.Errorf("dex_chain.evm_rpc_url is required when evm.swap addresses are set")
	}
	return nil
}

// Default fee applied to non-CLOB txs when the [fee] section is absent.
// Matches a chain whose minimum-gas-prices is 25000000000asvp at a
// 1,000,000 gas limit (≈0.025 SVP total).
const (
	DefaultFeeDenom    = "asvp"
	DefaultFeeAmount   = "25000000000000000"
	DefaultFeeGasLimit = uint64(1_000_000)
)

func (f *FeeConfig) applyDefaults() {
	if f.Denom == "" {
		f.Denom = DefaultFeeDenom
	}
	if f.Amount == "" {
		f.Amount = DefaultFeeAmount
	}
	if f.GasLimit == 0 {
		f.GasLimit = DefaultFeeGasLimit
	}
}

func (f *FeeConfig) validate() error {
	if f.Denom == "" {
		return fmt.Errorf("fee.denom is required")
	}
	amt, ok := math.NewIntFromString(f.Amount)
	if !ok {
		return fmt.Errorf("fee.amount %q is not a valid integer", f.Amount)
	}
	if amt.IsNegative() {
		return fmt.Errorf("fee.amount %q must be non-negative", f.Amount)
	}
	return nil
}
