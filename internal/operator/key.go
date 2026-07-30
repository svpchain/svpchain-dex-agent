// Package operator holds the agent's own signing identity: the eth_secp256k1
// operator key delegated executions (and the agent's self-registration) are
// signed with, and the fee-aware direct-sign path those transactions need.
//
// This is the one place the agent stops being keyless. Everything else the
// agent builds is signed by the *caller*; only MsgAgentExecDelegated — where
// the chain requires the registered operator as the tx signer — and the
// operator's own registry lifecycle sign in-process.
package operator

import (
	"fmt"
	"os"
	"strings"

	"github.com/cosmos/evm/crypto/ethsecp256k1"

	"github.com/svpchain/svpchain-dex-agent/internal/config"
	"github.com/svpchain/svpchain-mcp/lib/mcp/signer"
)

// KeyEnvVar overrides the configured key file, so deployments can inject the
// operator key through the environment without writing it to disk.
const KeyEnvVar = "DEX_AGENT_OPERATOR_KEY"

// Load resolves the operator key: the environment variable first, then the
// configured key file. Both absent means the agent runs keyless — (nil, "",
// nil) — and the execution skill refuses at call time.
func Load(cfg config.Operator) (*ethsecp256k1.PrivKey, string, error) {
	hexKey := strings.TrimSpace(os.Getenv(KeyEnvVar))
	if hexKey == "" && cfg.KeyFile != "" {
		raw, err := os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, "", fmt.Errorf("read operator key file: %w", err)
		}
		hexKey = strings.TrimSpace(string(raw))
	}
	if hexKey == "" {
		return nil, "", nil
	}
	priv, err := signer.ParsePrivKey(hexKey)
	if err != nil {
		return nil, "", fmt.Errorf("parse operator key: %w", err)
	}
	return priv, signer.DeriveAddress(priv), nil
}
