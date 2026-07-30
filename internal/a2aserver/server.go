package a2aserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
)

// Config is everything the read-layer server needs.
//
// Note what is absent: no signing key, no chain RPC, no LLM key. The read layer
// reads one public endpoint, so its configuration surface is the endpoint and
// where to listen — and that small surface is itself the argument that this
// service is safe to run and expose without the trust machinery execution
// needs.
type Config struct {
	// ListenAddr is the bind address, e.g. ":8081".
	ListenAddr string

	// PublicURL is how callers reach this agent, advertised in the card.
	PublicURL string

	// IndexerURL is the base URL of the DEX indexer (Comlink) REST API.
	IndexerURL string
}

// StartServer serves the Agent Card and the JSON-RPC read-layer endpoint until
// the context is cancelled.
func StartServer(ctx context.Context, cfg Config) error {
	idx := indexer.NewClient(cfg.IndexerURL, indexer.Options{
		Timeout: 15 * time.Second,
	})
	executor := NewExecutor(marketdata.NewService(idx))

	handler := a2asrv.NewHandler(executor)
	mux := http.NewServeMux()
	mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(handler))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(BuildAgentCard(cfg.PublicURL)))

	// A plain liveness endpoint, so the read layer can sit behind a load
	// balancer without the balancer needing to understand A2A.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.ListenAddr, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	fmt.Fprintf(os.Stderr, "svpchain-dex-agent: listening on %s\n", cfg.ListenAddr)
	fmt.Fprintf(os.Stderr, "svpchain-dex-agent: agent card at %s%s\n", cfg.PublicURL, a2asrv.WellKnownAgentCardPath)
	fmt.Fprintf(os.Stderr, "svpchain-dex-agent: reading indexer at %s\n", cfg.IndexerURL)

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
