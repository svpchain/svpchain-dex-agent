package a2aserver

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/svpchain/svpchain-dex-agent/internal/marketdata"
	"github.com/svpchain/svpchain-dex-agent/internal/toolbridge"
)

// Executor answers A2A tasks by dispatching them into the operation registry.
//
// Deliberately not LLM-driven. A request names a skill, a tool, and its
// arguments as JSON, and the executor dispatches it — pricing an order or
// building a tx payload is a lookup, and putting a model in front of it would
// add cost, latency, and a failure mode for no gain.
type Executor struct {
	market *marketdata.Service

	// registry and authr are nil in legacy read-only mode (no config file):
	// only the market-data legacy queries answer, everything else refuses.
	registry *toolbridge.Registry
	authr    *AuthResolver
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// NewExecutor returns a read-only executor backed by a market-data service.
func NewExecutor(market *marketdata.Service) *Executor {
	return &Executor{market: market}
}

// NewFullExecutor returns an executor serving the whole operation registry.
func NewFullExecutor(market *marketdata.Service, registry *toolbridge.Registry, authr *AuthResolver) *Executor {
	return &Executor{market: market, registry: registry, authr: authr}
}

func (e *Executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.Message == nil {
			yield(nil, fmt.Errorf("empty message"))
			return
		}

		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		result, err := e.handle(ctx, execCtx)
		if err != nil {
			// A refused or malformed request completes the task with the
			// reason as its answer, not a transport error or a failed state:
			// the task ran and produced an answer, and that answer is "no".
			// A caller distinguishes this from a crash.
			result = fmt.Sprintf("error: %v", err)
		}

		// The answer rides the terminal status update — once the submitted
		// task exists, the protocol forbids bare Message events.
		reply := a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart(result))
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, reply), nil)
	}
}

// Cancel is a no-op: operations are synchronous request/response calls with
// nothing to unwind.
func (e *Executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// handle dispatches one request and returns its JSON result.
func (e *Executor) handle(ctx context.Context, execCtx *a2asrv.ExecutorContext) (string, error) {
	raw := messageText(execCtx.Message)

	var req Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", fmt.Errorf("request must be JSON naming a skill: %w", err)
	}
	if req.Skill == "" {
		return "", fmt.Errorf("no skill named")
	}

	// Legacy read-layer form: {"skill":"svpchain-market-data","query":...}.
	// Served straight from the market-data service so pre-envelope callers
	// (and the original examples on the card) keep working byte-for-byte.
	if req.Skill == toolbridge.SkillMarketData && req.Query != "" {
		return e.handleMarketData(ctx, req)
	}

	if e.registry == nil {
		switch req.Skill {
		case toolbridge.SkillMarketData:
			return "", fmt.Errorf("no query named for %s", toolbridge.SkillMarketData)
		case toolbridge.SkillExecution:
			// ★ Advertised on the read-only card, refused here with the
			// requirement — a caller learns what execution needs instead of
			// meeting silence.
			return "", fmt.Errorf(
				"execution requires a delegation credential and an operator-configured deployment; "+
					"this read-only deployment serves only the %s skill", toolbridge.SkillMarketData)
		default:
			return "", fmt.Errorf(
				"unknown skill %q; this read-only deployment serves only the %s skill (run with -config for the full agent)",
				req.Skill, toolbridge.SkillMarketData)
		}
	}

	if req.Tool == "" {
		return "", fmt.Errorf("no tool named for %s", req.Skill)
	}
	op, ok := e.registry.Lookup(req.Tool)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", req.Tool)
	}
	if op.Skill != req.Skill {
		return "", fmt.Errorf("tool %q belongs to skill %q, not %q", req.Tool, op.Skill, req.Skill)
	}

	if e.authr != nil {
		ctx = e.authr.Attach(ctx, execCtx, &req)
	}

	resp := Response{Skill: req.Skill, Tool: req.Tool}
	result, err := op.Call(ctx, req.Args)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.OK = true
		resp.Result = result
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(b), nil
}

func (e *Executor) handleMarketData(ctx context.Context, req Request) (string, error) {
	switch req.Query {
	case "markets":
		return asJSON(e.market.Markets(ctx))
	case "market":
		return asJSON(e.market.Market(ctx, req.Ticker))
	case "orderbook":
		return asJSON(e.market.Orderbook(ctx, req.Ticker))
	case "funding":
		return asJSON(e.market.Funding(ctx, req.Ticker))
	case "estimate":
		side, err := parseSide(req.Side)
		if err != nil {
			return "", err
		}
		return asJSON(e.market.EstimateClearing(ctx, req.Ticker, side, req.Size))
	default:
		return "", fmt.Errorf("unknown query %q", req.Query)
	}
}

func parseSide(s string) (marketdata.Side, error) {
	switch marketdata.Side(s) {
	case marketdata.Buy:
		return marketdata.Buy, nil
	case marketdata.Sell:
		return marketdata.Sell, nil
	default:
		return "", fmt.Errorf("side must be %q or %q, got %q", marketdata.Buy, marketdata.Sell, s)
	}
}

// asJSON renders a service result, propagating the service error unchanged so
// the caller sees why a lookup failed rather than a generic marshalling error.
func asJSON(v any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	b, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		return "", fmt.Errorf("encode result: %w", marshalErr)
	}
	return string(b), nil
}

// messageText concatenates the text parts of a message. Mirrors the helper the
// wallet agent uses, so both read A2A messages the same way.
func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var out strings.Builder
	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		if text := part.Text(); text != "" {
			out.WriteString(text)
		}
	}
	return strings.TrimSpace(out.String())
}
