// Package toolbridge exposes svpchain-mcp's MCP tool handlers — and the
// agent's own chain-module services — as A2A operations.
//
// Every MCP handler shares one shape, func(ctx, *mcp.CallToolRequest, In)
// (*mcp.CallToolResult, Out, error), and none of them reads the request
// parameter (auth, IP, and session travel in ctx). That uniformity is what
// lets a single generic adapter serve all of them: decode the A2A args into
// In, call the handler with a nil request, return Out. The bridge adds no
// behavior — authorization, limits, and refusal messages are the handlers'
// own, so the A2A surface and the MCP surface cannot drift.
package toolbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Op is one invokable operation: a tool bound to the A2A skill it is
// advertised under.
type Op struct {
	Skill string
	Tool  string
	Call  func(ctx context.Context, args json.RawMessage) (any, error)
}

// handler is the uniform svpchain-mcp tool handler shape.
type handler[In, Out any] func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)

// adapt wraps an MCP tool handler into an Op call. The nil CallToolRequest is
// safe: every handler ignores it (verified across the tool package — identity
// comes from ctx via tools.WithTenant / WithIP / WithSessionID).
func adapt[In, Out any](h handler[In, Out]) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in In
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
		}
		res, out, err := h(ctx, nil, in)
		if err != nil {
			return nil, err
		}
		// Handlers report failures as errors, not IsError results; this guard
		// exists so a future handler that does neither cannot smuggle an error
		// result through as success.
		if res != nil && res.IsError {
			return nil, errors.New(resultText(res))
		}
		return out, nil
	}
}

// adaptNative wraps a plain service method (no MCP types) into an Op call —
// the twin of adapt for the agent's own chain-module operations.
func adaptNative[In, Out any](f func(context.Context, In) (Out, error)) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in In
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, fmt.Errorf("decode args: %w", err)
			}
		}
		return f(ctx, in)
	}
}

func resultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok && t.Text != "" {
			return t.Text
		}
	}
	return "tool refused"
}

// Registry maps tool names to operations and groups them by skill for the
// Agent Card.
type Registry struct {
	ops map[string]Op
}

func newRegistry() *Registry { return &Registry{ops: map[string]Op{}} }

func (r *Registry) add(skill, tool string, call func(context.Context, json.RawMessage) (any, error)) {
	if _, dup := r.ops[tool]; dup {
		panic(fmt.Sprintf("toolbridge: duplicate tool %q", tool))
	}
	r.ops[tool] = Op{Skill: skill, Tool: tool, Call: call}
}

// addRefusing registers a tool that always refuses with reason — used for
// skills whose backing service is not configured, so a caller learns the
// requirement instead of meeting an unknown-tool error.
func (r *Registry) addRefusing(skill, tool, reason string) {
	r.add(skill, tool, func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New(reason)
	})
}

// Lookup returns the operation registered under tool.
func (r *Registry) Lookup(tool string) (Op, bool) {
	op, ok := r.ops[tool]
	return op, ok
}

// BySkill returns tool names grouped by skill, each group sorted — the card
// generator and the completeness tests read this.
func (r *Registry) BySkill() map[string][]string {
	out := map[string][]string{}
	for _, op := range r.ops {
		out[op.Skill] = append(out[op.Skill], op.Tool)
	}
	for _, tools := range out {
		sort.Strings(tools)
	}
	return out
}
