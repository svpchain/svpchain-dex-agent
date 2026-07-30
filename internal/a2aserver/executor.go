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
)

// Executor answers A2A tasks against the read layer.
//
// Deliberately not LLM-driven. The read layer is a thin shell: a request names
// a skill and its parameters as JSON, and the executor dispatches it. There is
// no reasoning to do here — pricing an order or listing markets is a lookup,
// and putting a model in front of it would add cost, latency, and a failure
// mode for no gain. The intelligence the checklist allows is the batch-auction
// estimate, which lives in the market-data service, not here.
type Executor struct {
	market *marketdata.Service
}

var _ a2asrv.AgentExecutor = (*Executor)(nil)

// NewExecutor returns an executor backed by a market-data service.
func NewExecutor(market *marketdata.Service) *Executor {
	return &Executor{market: market}
}

// request is the JSON a caller sends in the task message.
type request struct {
	Skill  string `json:"skill"`
	Query  string `json:"query"`
	Ticker string `json:"ticker"`
	Side   string `json:"side"`
	Size   string `json:"size"`
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

		result, err := e.handle(ctx, messageText(execCtx.Message))
		if err != nil {
			// A refused or malformed request is reported as a message, not a
			// transport error: the task ran and produced an answer, and that
			// answer is "no". A caller distinguishes this from a crash.
			yield(a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx,
				a2a.NewTextPart(fmt.Sprintf("error: %v", err))), nil)
			return
		}

		yield(a2a.NewMessageForTask(a2a.MessageRoleAgent, execCtx, a2a.NewTextPart(result)), nil)
	}
}

// Cancel is a no-op: read-layer requests are synchronous lookups with nothing
// to unwind.
func (e *Executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// handle dispatches one request and returns its JSON result.
func (e *Executor) handle(ctx context.Context, raw string) (string, error) {
	var req request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return "", fmt.Errorf("request must be JSON naming a skill: %w", err)
	}

	switch req.Skill {
	case SkillMarketData:
		return e.handleMarketData(ctx, req)
	case SkillExecution:
		// ★ Advertised on the card, refused here. The execution layer verifies
		// a delegation proof and places orders on the user's subaccount; none
		// of that exists yet. Refusing with the reason — rather than omitting
		// the skill — is what tells a caller the capability is coming and what
		// it will require, instead of leaving them to guess from silence.
		return "", fmt.Errorf(
			"execution requires a delegation credential and is not yet available; "+
				"only the %s skill is served today", SkillMarketData)
	case "":
		return "", fmt.Errorf("no skill named; expected %q or %q", SkillMarketData, SkillExecution)
	default:
		return "", fmt.Errorf("unknown skill %q", req.Skill)
	}
}

func (e *Executor) handleMarketData(ctx context.Context, req request) (string, error) {
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
	case "":
		return "", fmt.Errorf("no query named for %s", SkillMarketData)
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
