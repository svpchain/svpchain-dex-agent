package a2aserver

import "encoding/json"

// Request is the JSON envelope a caller sends as the task message text.
//
// The general form names a skill, a tool, and the tool's arguments — the
// args object is exactly the MCP tool's input schema, so the A2A surface and
// the MCP tool surface share one vocabulary. The legacy read-layer form
// (skill + query + flat fields) is still honored so existing callers keep
// working.
type Request struct {
	Skill string          `json:"skill"`
	Tool  string          `json:"tool,omitempty"`
	Args  json.RawMessage `json:"args,omitempty"`

	// Bearer authenticates the caller when it cannot set an Authorization
	// header. The header wins when both are present.
	Bearer string `json:"bearer,omitempty"`

	// Legacy read-layer fields, honored when Skill is svpchain-market-data
	// and Query is set.
	Query  string `json:"query,omitempty"`
	Ticker string `json:"ticker,omitempty"`
	Side   string `json:"side,omitempty"`
	Size   string `json:"size,omitempty"`
}

// Response is the agent's reply, serialized as the message text.
//
// A refused or failed operation is a normal response with OK=false — the task
// ran and produced an answer, and that answer is "no". A caller distinguishes
// this from a transport error.
type Response struct {
	Skill  string `json:"skill"`
	Tool   string `json:"tool,omitempty"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}
