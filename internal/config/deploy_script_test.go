package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The deploy script renders agent.toml itself (render_agent_toml). This test
// pins the two artifacts together: whatever the script prints must parse and
// validate under this package, so schema changes that would brick a deploy
// fail here instead of on a remote host.
func TestDeployScriptConfigParses(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "dex-agent-deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("deploy script not found: %v", err)
	}

	cases := map[string][]string{
		"keyless": {"--print-config", "--host", "www@agent.example.com"},
		"with-operator-key": {
			"--print-config", "--host", "www@agent.example.com",
			"--operator-key-file", "operator.key", // rendered path only; file not read by --print-config
			"--public-url", "https://dex-agent.example.com",
		},
		"minimal-families-off": {
			"--print-config", "--host", "www@agent.example.com",
			"--evm-rpc", "", "--faucet-url", "",
			"--evm-uniswap-router", "", "--evm-wsvp", "", "--evm-oracle", "",
			"--evm-lendora-comptroller", "", "--evm-bridge-routes", "",
			"--evm-foreign-chains", "",
		},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := exec.Command("bash", append([]string{script}, args...)...).Output()
			if err != nil {
				t.Fatalf("render config: %v", err)
			}

			dir := t.TempDir()
			path := filepath.Join(dir, "agent.toml")
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("rendered config does not parse/validate:\n%s\nerror: %v", out, err)
			}
			if cfg.PublicURL == "" {
				t.Error("rendered config must carry a public_url")
			}
			if strings.Contains(name, "operator") && cfg.Operator.KeyFile == "" {
				t.Error("operator variant must set key_file")
			}
		})
	}
}
