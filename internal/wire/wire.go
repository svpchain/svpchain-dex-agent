// Package wire assembles the agent's dependency graph from its config: chain
// gRPC + CometBFT + EVM clients, the indexer client, the markets caches, the
// self-service auth stores, the policy engine, and the MCP tool handlers the
// A2A tool bridge dispatches into.
//
// The body deliberately mirrors svpchain-mcp's cmd/mcp-server wiring (which is
// package main and cannot be imported): same optional families, same
// all-or-nothing rules, same graceful degradation. Drift between the two is a
// bug in whichever copied last.
package wire

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"cosmossdk.io/log"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	agenttypes "github.com/dydxprotocol/v4-chain/protocol/x/agent/types"
	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"

	"github.com/svpchain/svpchain-mcp/lib/mcp/auth"
	"github.com/svpchain/svpchain-mcp/lib/mcp/bridge"
	"github.com/svpchain/svpchain-mcp/lib/mcp/builder"
	"github.com/svpchain/svpchain-mcp/lib/mcp/chain"
	"github.com/svpchain/svpchain-mcp/lib/mcp/faucet"
	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
	"github.com/svpchain/svpchain-mcp/lib/mcp/lendora"
	"github.com/svpchain/svpchain-mcp/lib/mcp/limits"
	"github.com/svpchain/svpchain-mcp/lib/mcp/markets"
	"github.com/svpchain/svpchain-mcp/lib/mcp/mcpcodec"
	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"

	"github.com/svpchain/svpchain-dex-agent/internal/agentchain"
	"github.com/svpchain/svpchain-dex-agent/internal/config"
	"github.com/svpchain/svpchain-dex-agent/internal/delegated"
	"github.com/svpchain/svpchain-dex-agent/internal/operator"
	"github.com/svpchain/svpchain-dex-agent/internal/toolbridge"
)

// App is the wired agent: everything the A2A server needs to serve requests,
// plus the background caches it must run.
type App struct {
	Handlers *tools.Handlers      // the 64 MCP tool handlers
	Registry *toolbridge.Registry // A2A operation registry over them
	Markets  *markets.Cache       // must Run(ctx); initial refresh failure is fatal
	Lendora  *lendora.Cache       // nil unless lendora is configured
	Tenants  *auth.DynamicTenantStore
	Sessions *auth.SessionBearers
	Indexer  *indexer.Client
	GrpcConn *grpc.ClientConn
	Logger   log.Logger

	// Delegated is the execution service (nil when keyless). Exposed so the
	// server layer can hand it the served agent-card bytes — the capability
	// hash it registers on chain is sha256 of exactly those bytes.
	Delegated *delegated.Service
}

// Close releases the app's long-lived connections.
func (a *App) Close() {
	if a.GrpcConn != nil {
		_ = a.GrpcConn.Close()
	}
}

// Run starts the background cache refreshers and blocks until ctx is
// cancelled. The markets cache must complete its initial refresh before build
// operations work, and its Run fails fast when the chain is unreachable —
// surfacing a dead gRPC endpoint at boot instead of on the first trade.
func (a *App) Run(ctx context.Context) error {
	errc := make(chan error, 2)
	go func() { errc <- a.Markets.Run(ctx) }()
	if a.Lendora != nil {
		go func() { errc <- a.Lendora.Run(ctx) }()
	}
	select {
	case err := <-errc:
		if err != nil && ctx.Err() == nil {
			return err
		}
	case <-ctx.Done():
	}
	return nil
}

// dynamicTenantAdapter converts auth.TenantRecord into policy.TenantPolicy so
// the policy engine can resolve auto-issued tenants; kept here so auth never
// imports policy (mirrors the mcp-server adapter).
type dynamicTenantAdapter struct{ store *auth.DynamicTenantStore }

func (a dynamicTenantAdapter) LookupTenantPolicy(tenantID string) (policy.TenantPolicy, bool) {
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

// Build wires the configuration into a ready-to-run App.
func Build(ctx context.Context, cfg *config.Config) (*App, error) {
	logger := log.NewLogger(os.Stderr).With("module", "dex-agent")

	grpcConn, err := chain.Dial(ctx, cfg.GrpcAddr)
	if err != nil {
		return nil, fmt.Errorf("dial chain gRPC: %w", err)
	}
	encCfg := mcpcodec.GetEncodingConfig()

	chainDeps := tools.ChainDeps{
		Account:         chain.NewAccountClient(grpcConn, encCfg.InterfaceRegistry),
		Broadcast:       chain.NewBroadcastClient(grpcConn),
		ClobQuery:       chain.NewClobQueryClient(grpcConn),
		PerpetualsQuery: chain.NewPerpetualsQueryClient(grpcConn),
		SubaccountQuery: chain.NewSubaccountQueryClient(grpcConn),
		BankQuery:       chain.NewBankQueryClient(grpcConn),
	}
	cometClient, err := chain.NewCometBftClient(cfg.CometRPCURL)
	if err != nil {
		grpcConn.Close()
		return nil, fmt.Errorf("cometbft client: %w", err)
	}
	chainDeps.CometBft = cometClient

	// EVM is optional: without evm_rpc_url the EVM operations refuse at call
	// time (Deps.EVM.Assembler stays nil) and non-EVM deployments boot fine.
	var evmDeps tools.EVMDeps
	var lendoraMkts *lendora.Cache
	if cfg.EVMRPCURL != "" {
		evmClient, err := chain.NewEVMClient(ctx, cfg.EVMRPCURL)
		if err != nil {
			grpcConn.Close()
			return nil, fmt.Errorf("evm client: %w", err)
		}
		chainDeps.EVM = evmClient
		evmDeps = tools.EVMDeps{Assembler: builder.NewEVMAssembler(evmClient)}

		if cfg.EVMUniswapRouterAddr != "" {
			uni, err := builder.NewUniswapV2(
				common.HexToAddress(cfg.EVMUniswapRouterAddr),
				common.HexToAddress(cfg.EVMWSVPAddr),
			)
			if err != nil {
				grpcConn.Close()
				return nil, fmt.Errorf("uniswap binding: %w", err)
			}
			evmDeps.Uniswap = uni
		}
		if cfg.EVMOracleAddr != "" {
			oracle, err := builder.NewOracleFeed(common.HexToAddress(cfg.EVMOracleAddr))
			if err != nil {
				grpcConn.Close()
				return nil, fmt.Errorf("oracle feed binding: %w", err)
			}
			evmDeps.Oracle = oracle
		}
		if cfg.EVMLendoraComptrollerAddr != "" {
			lend, err := builder.NewLendora(common.HexToAddress(cfg.EVMLendoraComptrollerAddr))
			if err != nil {
				grpcConn.Close()
				return nil, fmt.Errorf("lendora binding: %w", err)
			}
			evmDeps.Lendora = lend
			lendoraMkts = lendora.NewCache(evmClient, lend, time.Duration(cfg.Cache.MarketsRefresh), logger)
		}
		if cfg.EVMBridgeAddr != "" {
			br, err := builder.NewBridge(common.HexToAddress(cfg.EVMBridgeAddr))
			if err != nil {
				grpcConn.Close()
				return nil, fmt.Errorf("bridge binding: %w", err)
			}
			routes, err := bridge.LoadRegistry(cfg.EVMBridgeRoutesPath)
			if err != nil {
				grpcConn.Close()
				return nil, fmt.Errorf("bridge routes: %w", err)
			}
			if !routes.HasSource(cfg.EVMBridgeSourceChainID) {
				grpcConn.Close()
				return nil, fmt.Errorf("bridge routes %s has no routes originating from evm_bridge_source_chain_id %d",
					cfg.EVMBridgeRoutesPath, cfg.EVMBridgeSourceChainID)
			}
			evmDeps.Bridge = br
			evmDeps.BridgeRoutes = routes
			evmDeps.BridgeSourceChainID = cfg.EVMBridgeSourceChainID
			evmDeps.HomeChainID = cfg.EVMBridgeSourceChainID

			if len(cfg.EVMForeignChains) > 0 {
				foreign := make(map[uint64]*tools.ForeignChain, len(cfg.EVMForeignChains))
				for _, fc := range cfg.EVMForeignChains {
					if _, err := routes.ResolveSourceChain(strconv.FormatUint(fc.ChainID, 10), cfg.EVMBridgeSourceChainID); err != nil {
						grpcConn.Close()
						return nil, fmt.Errorf("evm_foreign_chain %d: %w", fc.ChainID, err)
					}
					fbr, err := builder.NewBridge(common.HexToAddress(fc.BridgeAddr))
					if err != nil {
						grpcConn.Close()
						return nil, fmt.Errorf("foreign bridge binding (chain %d): %w", fc.ChainID, err)
					}
					fclient, err := chain.NewEVMClient(ctx, fc.RPCURL)
					if err != nil {
						grpcConn.Close()
						return nil, fmt.Errorf("foreign evm client (chain %d): %w", fc.ChainID, err)
					}
					foreign[fc.ChainID] = &tools.ForeignChain{
						Client:    fclient,
						Assembler: builder.NewEVMAssembler(fclient),
						Bridge:    fbr,
					}
				}
				evmDeps.ForeignChains = foreign
			}
		}
	}

	var faucetClient *faucet.Client
	if cfg.FaucetBaseURL != "" {
		faucetClient = faucet.NewClient(cfg.FaucetBaseURL, faucet.Options{})
	}

	idx := indexer.NewClient(cfg.IndexerBaseURL, indexer.Options{})
	mkts := markets.NewCache(chainDeps.ClobQuery, chainDeps.PerpetualsQuery, time.Duration(cfg.Cache.MarketsRefresh), logger)

	limitsCfg := limits.Config{
		DepositMaxUSDC:       cfg.Limits.DepositMaxUSDC,
		WithdrawMaxUSDC:      cfg.Limits.WithdrawMaxUSDC,
		TransferMaxUSDC:      cfg.Limits.TransferMaxUSDC,
		DailyWithdrawCapUSDC: cfg.Limits.DailyWithdrawCapUSDC,
	}
	withdrawLedger := limits.NewMemoryLedger(limitsCfg.DailyWithdrawCapUSDC, nil)
	transferOut, err := limits.LoadMemoryTransferOutStore(cfg.TransferOutCapPath, nil, func(err error) {
		logger.Error("transfer-out cap persistence failed", "error", err)
	})
	if err != nil {
		grpcConn.Close()
		return nil, fmt.Errorf("load transfer-out cap state: %w", err)
	}

	// Self-service auth state: in-memory + TTL-bounded, same defaults as the
	// MCP server (auto-issued tenants get subaccounts 0..9).
	nonceStore := auth.NewNonceStore(auth.DefaultChallengeTTL, nil)
	dynamicTenants := auth.NewDynamicTenantStore(auth.DynamicTenantStoreConfig{
		BearerTTL:                 auth.DefaultBearerTTL,
		DefaultAllowedSubaccounts: []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	}, nil)
	ipLimit := auth.NewIPRateLimiter(auth.DefaultIPChallengeRate, auth.DefaultIPChallengeWindow, nil)
	sessionBearers := auth.NewSessionBearers(auth.DefaultBearerTTL, nil)

	policyEngine := policy.NewEngine(nil)
	policyEngine.SetDynamicSource(dynamicTenantAdapter{store: dynamicTenants})

	deps := tools.Deps{
		Chain:             chainDeps,
		Indexer:           idx,
		Markets:           mkts,
		LendoraMarkets:    lendoraMkts,
		Builder:           builder.NewAssembler(cfg.ChainID, cfg.Fee.Denom, cfg.Fee.Amount, cfg.Fee.GasLimit),
		Faucet:            faucetClient,
		EVM:               evmDeps,
		Policy:            policyEngine,
		Auditor:           policy.NewStdoutAuditor(),
		Idempotency:       policy.NewIdempotency(0),
		RateLimit:         policy.NewRateLimiter(0, 0),
		Limits:            limitsCfg,
		WithdrawLedger:    withdrawLedger,
		TransferOut:       transferOut,
		NonceStore:        nonceStore,
		DynamicTenants:    dynamicTenants,
		IPChallengeLimit:  ipLimit,
		SessionBearers:    sessionBearers,
		Logger:            logger,
		InterfaceRegistry: encCfg.InterfaceRegistry,
		BroadcastMode:     cfg.BroadcastMode,
	}
	handlers := tools.New(cfg.ChainID, deps)

	registry := toolbridge.New(handlers)

	// x/agent + x/agentwallet: query clients on the same gRPC conn, tx builds
	// through the same assembler and policy engine the MCP builders use.
	agentQ := agenttypes.NewQueryClient(grpcConn)
	walletQ := wallettypes.NewQueryClient(grpcConn)
	agentSvc := agentchain.New(agentQ, walletQ, deps.Builder, chainDeps.Account, policyEngine)
	registry.RegisterAgentChain(agentSvc)

	// Delegated execution goes live only when an operator key is configured;
	// keyless deployments register the same operations as informative
	// refusals, keeping the read layer's "advertised but refused" contract.
	operatorPriv, operatorAddr, err := operator.Load(cfg.Operator)
	if err != nil {
		grpcConn.Close()
		return nil, err
	}
	var delegatedSvc *delegated.Service
	if operatorPriv != nil {
		delegatedSvc = delegated.New(delegated.Config{
			Priv:         operatorPriv,
			Operator:     operatorAddr,
			ChainID:      cfg.ChainID,
			Fee:          operator.FeeSpec{Denom: cfg.Fee.Denom, Amount: cfg.Fee.Amount, GasLimit: cfg.Fee.GasLimit},
			AgentQ:       agentQ,
			AuthQ:        delegated.NewAuthKeyClient(authtypes.NewQueryClient(grpcConn), encCfg.InterfaceRegistry),
			WalletQ:      walletQ,
			Markets:      mkts,
			Account:      chainDeps.Account,
			Broadcast:    chainDeps.Broadcast,
			Policy:       policyEngine,
			Endpoint:     cfg.Operator.Endpoint,
			Capabilities: cfg.Operator.Capabilities,
			Metadata:     cfg.Operator.Metadata,
		})
		logger.Info("delegated execution enabled", "operator", operatorAddr)
	}
	registry.RegisterExecution(delegatedSvc)

	return &App{
		Handlers:  handlers,
		Registry:  registry,
		Markets:   mkts,
		Lendora:   lendoraMkts,
		Tenants:   dynamicTenants,
		Sessions:  sessionBearers,
		Indexer:   idx,
		GrpcConn:  grpcConn,
		Delegated: delegatedSvc,
		Logger:    logger,
	}, nil
}
