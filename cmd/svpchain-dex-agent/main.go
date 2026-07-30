// Command svpchain-dex-agent is the read layer of an SVP-Chain DEX agent: an
// A2A service that answers market-data questions from the public indexer.
//
// It is a thin shell by design. It holds no keys, opens no chain connection,
// and depends on no on-chain delegation module — so it can be deployed, and
// billed per call over x402, before any of the execution or delegation
// machinery exists. The execution skill is advertised on its Agent Card but
// refuses until that machinery is built.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/svpchain/svpchain-dex-agent/internal/a2aserver"
)

func main() {
	listenAddr := flag.String("listen", ":8081", "bind address for the A2A endpoint")
	publicURL := flag.String("public-url", "", "public base URL advertised in the Agent Card (default http://<listen>)")
	indexerURL := flag.String("indexer-url", envOr("DEX_INDEXER_URL", ""), "base URL of the DEX indexer (Comlink) REST API")
	flag.Parse()

	if *indexerURL == "" {
		fmt.Fprintln(os.Stderr, "error: --indexer-url (or DEX_INDEXER_URL) is required")
		os.Exit(2)
	}

	public := *publicURL
	if public == "" {
		public = "http://localhost" + *listenAddr
	}

	// Stop cleanly on SIGINT/SIGTERM so a container orchestrator gets a prompt
	// exit rather than a killed process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err := a2aserver.StartServer(ctx, a2aserver.Config{
		ListenAddr: *listenAddr,
		PublicURL:  public,
		IndexerURL: *indexerURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "svpchain-dex-agent: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
