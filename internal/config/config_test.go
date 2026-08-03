package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Dotted dex_chain.* keys (not a [dex_chain] header) so tests can keep
// appending top-level keys to this fixture without them landing inside the
// table.
const minimal = `
dex_chain.id               = "svp-test-1"
dex_chain.grpc_addr        = "127.0.0.1:9090"
dex_chain.comet_rpc_url    = "http://127.0.0.1:26657"
dex_chain.indexer_base_url = "http://127.0.0.1:3002"
listen_addr                = ":8081"
`

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Fee.Denom != DefaultFeeDenom || cfg.Fee.Amount != DefaultFeeAmount || cfg.Fee.GasLimit != DefaultFeeGasLimit {
		t.Errorf("fee defaults not applied: %+v", cfg.Fee)
	}
	if cfg.BroadcastMode != "server" {
		t.Errorf("broadcast_mode default not applied: %q", cfg.BroadcastMode)
	}
	if cfg.PublicURL != "http://localhost:8081" {
		t.Errorf("public_url default not derived from listen_addr: %q", cfg.PublicURL)
	}
}

func TestLoadRejectsMissingRequiredFields(t *testing.T) {
	for _, missing := range []string{"dex_chain.id", "dex_chain.grpc_addr", "dex_chain.comet_rpc_url", "dex_chain.indexer_base_url", "listen_addr"} {
		t.Run(missing, func(t *testing.T) {
			var body strings.Builder
			for _, line := range strings.Split(strings.TrimSpace(minimal), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), missing) {
					body.WriteString(line + "\n")
				}
			}
			if _, err := Load(writeConfig(t, body.String())); err == nil || !strings.Contains(err.Error(), missing) {
				t.Errorf("expected error naming %s, got %v", missing, err)
			}
		})
	}
}

func TestAgentChainIsBothOrNeither(t *testing.T) {
	if _, err := Load(writeConfig(t, minimal+`
agent_chain.rest_url = "http://127.0.0.1:1317"
`)); err == nil || !strings.Contains(err.Error(), "agent_chain.id") {
		t.Errorf("rest_url without id must fail, got %v", err)
	}
	cfg, err := Load(writeConfig(t, minimal+`
agent_chain.id       = "svpagent-1"
agent_chain.rest_url = "http://127.0.0.1:1317"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AgentChain.Enabled() {
		t.Error("agent chain should report enabled when configured")
	}
}

func TestSwapAddressesAreBothOrNeither(t *testing.T) {
	body := minimal + `
dex_chain.evm_rpc_url        = "http://127.0.0.1:8545"
evm.swap.uniswap_router_addr = "0x0000000000000000000000000000000000000001"
`
	if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Errorf("router without wsvp must fail, got %v", err)
	}
}

func TestBridgeRequiresAllThreeAndEVMRPC(t *testing.T) {
	body := minimal + `
evm.bridge.addr = "0x0000000000000000000000000000000000000002"
`
	if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "set together") {
		t.Errorf("partial bridge config must fail, got %v", err)
	}
}

func TestForeignChainRequiresHomeBridge(t *testing.T) {
	body := minimal + `
[[evm.bridge.foreign_chain]]
chain_id    = 421614
rpc_url     = "http://foreign:8545"
bridge_addr = "0x0000000000000000000000000000000000000003"
`
	if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "requires the bridge") {
		t.Errorf("foreign chain without home bridge must fail, got %v", err)
	}
}

func TestOperatorKeyFileResolvesAgainstConfigDir(t *testing.T) {
	path := writeConfig(t, minimal+`
[operator]
key_file = "operator.key"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(path), "operator.key")
	if cfg.Operator.KeyFile != want {
		t.Errorf("key_file %q should resolve against the config dir to %q", cfg.Operator.KeyFile, want)
	}
}

func TestFeeAmountMustBeANonNegativeInteger(t *testing.T) {
	body := minimal + `
[fee]
denom  = "asvp"
amount = "not-a-number"
`
	if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "fee.amount") {
		t.Errorf("bad fee amount must fail, got %v", err)
	}
}
