package a2aserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/svpchain/svpchain-dex-agent/internal/config"
	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-dex-agent/internal/toolbridge"
	"github.com/svpchain/svpchain-dex-agent/internal/wire"
	"github.com/svpchain/svpchain-mcp/lib/mcp/indexer"
)

// Config is everything the legacy read-only server needs. The full agent is
// configured by internal/config and started with StartFull.
type Config struct {
	// ListenAddr is the bind address, e.g. ":8081".
	ListenAddr string

	// PublicURL is how callers reach this agent, advertised in the card.
	PublicURL string

	// IndexerURL is the base URL of the DEX indexer (Comlink) REST API.
	IndexerURL string
}

// StartServer serves the read-only layer: the legacy market-data skill backed
// by nothing but the public indexer. Kept because the thin shell is
// deployable without any chain access, and the original container entrypoint
// (flags only, no config file) must keep working.
func StartServer(ctx context.Context, cfg Config) error {
	idx := indexer.NewClient(cfg.IndexerURL, indexer.Options{
		Timeout: 15 * time.Second,
	})
	executor := NewExecutor(marketdata.NewService(idx))
	return serve(ctx, cfg.ListenAddr, cfg.PublicURL, cfg.IndexerURL, executor, nil)
}

// StartFull serves the whole agent: every operation in the wired registry,
// with the auth resolver mapping A2A callers onto tool tenants. It runs the
// app's background caches alongside the HTTP server and stops both when ctx
// is cancelled or either fails.
func StartFull(ctx context.Context, cfg *config.Config, app *wire.App) error {
	executor := NewFullExecutor(
		marketdata.NewService(app.Indexer),
		app.Registry,
		&AuthResolver{Tenants: app.Tenants, Sessions: app.Sessions},
	)

	// Hand the delegated service the exact bytes the card route serves
	// (NewStaticAgentCardHandler marshals the card the same way), so the
	// capability hash it registers on chain verifies against a fetch of
	// /.well-known/agent-card.json.
	if app.Delegated != nil {
		cardJSON, err := json.Marshal(BuildAgentCard(cfg.PublicURL, app.Registry))
		if err != nil {
			return fmt.Errorf("marshal agent card: %w", err)
		}
		app.Delegated.SetCapabilityCard(cardJSON)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cacheErr := make(chan error, 1)
	go func() { cacheErr <- app.Run(ctx) }()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serve(ctx, cfg.ListenAddr, cfg.PublicURL, cfg.DEXChain.IndexerBaseURL, executor, app.Registry)
	}()

	// Either half failing takes the whole agent down: a dead markets cache
	// means build operations silently price against stale metadata, and a
	// dead HTTP server means nothing answers — neither is a state to limp in.
	select {
	case err := <-cacheErr:
		cancel()
		<-serveErr
		if err != nil {
			return fmt.Errorf("markets cache: %w", err)
		}
		return nil
	case err := <-serveErr:
		cancel()
		return err
	}
}

func serve(ctx context.Context, listenAddr, publicURL, indexerURL string, executor *Executor, reg *toolbridge.Registry) error {
	handler := a2asrv.NewHandler(executor)
	mux := http.NewServeMux()
	mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(handler))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(BuildAgentCard(publicURL, reg)))

	// A plain liveness endpoint, so the agent can sit behind a load balancer
	// without the balancer needing to understand A2A.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	fmt.Fprintf(os.Stderr, "svpchain-dex-agent: listening on %s\n", listenAddr)
	fmt.Fprintf(os.Stderr, "svpchain-dex-agent: agent card at %s%s\n", publicURL, a2asrv.WellKnownAgentCardPath)
	fmt.Fprintf(os.Stderr, "svpchain-dex-agent: reading indexer at %s\n", indexerURL)

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
