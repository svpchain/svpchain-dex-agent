module github.com/svpchain/svpchain-dex-agent

go 1.25.0

require (
	github.com/a2aproject/a2a-go/v2 v2.3.1
	github.com/svpchain/svpchain-mcp v0.0.0-00010101000000-000000000000
)

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/time v0.12.0 // indirect
)

// svpchain-mcp has no tagged release yet; the indexer client is consumed
// through a local replace for development. A tag is required before this repo
// builds outside the workspace.
replace github.com/svpchain/svpchain-mcp => ../svpchain-mcp
