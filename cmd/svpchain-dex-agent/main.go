// Command svpchain-dex-agent is an A2A agent for an SVP-Chain perpetuals DEX.
//
// Two modes, chosen by flags:
//
//   - Full mode (-config agent.toml): every operation family — market data,
//     accounts, unsigned tx building, broadcast, self-service auth, faucet,
//     EVM, Lendora, the chain's agent/agentwallet modules, and delegated
//     execution when an operator key is configured.
//
//   - Read-only mode (--indexer-url, no -config): the original thin shell.
//     It holds no keys and opens no chain connection, so it can be deployed
//     against nothing but the public indexer.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/svpchain/svpchain-dex-agent/internal/a2aserver"
	"github.com/svpchain/svpchain-dex-agent/internal/config"
	"github.com/svpchain/svpchain-dex-agent/internal/wire"
)

func main() {
	configPath := flag.String("config", "", "TOML config for the full agent (see internal/config)")
	listenAddr := flag.String("listen", ":8081", "bind address for the A2A endpoint (read-only mode)")
	publicURL := flag.String("public-url", "", "public base URL advertised in the Agent Card (default http://<listen>)")
	indexerURL := flag.String("indexer-url", envOr("DEX_INDEXER_URL", ""), "base URL of the DEX indexer (Comlink) REST API (read-only mode)")
	flag.Parse()

	// Stop cleanly on SIGINT/SIGTERM so a container orchestrator gets a prompt
	// exit rather than a killed process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	if *configPath != "" {
		err = runFull(ctx, *configPath)
	} else {
		err = runReadOnly(ctx, *listenAddr, *publicURL, *indexerURL)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "svpchain-dex-agent: %v\n", err)
		os.Exit(1)
	}
}

func runFull(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	app, err := wire.Build(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()
	return a2aserver.StartFull(ctx, cfg, app)
}

func runReadOnly(ctx context.Context, listenAddr, publicURL, indexerURL string) error {
	if indexerURL == "" {
		return fmt.Errorf("either -config or --indexer-url (or DEX_INDEXER_URL) is required")
	}
	if publicURL == "" {
		publicURL = "http://localhost" + listenAddr
	}
	return a2aserver.StartServer(ctx, a2aserver.Config{
		ListenAddr: listenAddr,
		PublicURL:  publicURL,
		IndexerURL: indexerURL,
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
