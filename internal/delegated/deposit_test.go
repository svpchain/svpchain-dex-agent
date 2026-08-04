package delegated

import (
	"context"
	"strings"
	"testing"

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/gogoproto/proto"
	"github.com/svpchain/svpdt"

	wallettypes "github.com/dydxprotocol/v4-chain/protocol/x/agentwallet/types"
	sendingtypes "github.com/dydxprotocol/v4-chain/protocol/x/sending/types"

	"github.com/svpchain/svpchain-mcp/lib/mcp/limits"
)

// depositProof mints a credential that grants the deposit action on
// subaccount 0, alongside the fixture's default trading grants.
func depositProof(t *testing.T, f *fixture) []string {
	t.Helper()
	return f.issue(t, func(p *svpdt.IssueParams) {
		p.Caveats.Actions = svpdt.StringSet{ActionCancelOrder, ActionPlaceOrder, ActionDeposit}
	})
}

// A granted deposit broadcasts a wrapper whose inner message moves the
// principal's own wallet USDC into the principal's subaccount — sender and
// recipient owner both pinned to the credential's principal, matching the
// chain registry's containment — and, unlike short-term orders, pays a fee.
func TestExecuteDepositBuildsForThePrincipalAndPaysFees(t *testing.T) {
	f := newFixture(t)

	res, err := f.svc.ExecuteDepositToSubaccount(context.Background(), ExecDepositInput{
		Proof:   depositProof(t, f),
		Deposit: DepositParams{SubaccountNumber: 0, HumanUSDC: "25.5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TxHash == "" || res.Principal != testDelegator {
		t.Fatalf("unexpected result %+v", res)
	}

	var wrapper wallettypes.MsgAgentExecDelegated
	decodeSoleTxMsg(t, f.broadcast.txBytes, "/dydxprotocol.agentwallet.MsgAgentExecDelegated", &wrapper)
	if wrapper.Executor != f.svc.cfg.Operator || wrapper.AgentId != f.agentID {
		t.Errorf("wrapper executor/agent = %s/%s", wrapper.Executor, wrapper.AgentId)
	}

	var inner sendingtypes.MsgDepositToSubaccount
	if err := proto.Unmarshal(wrapper.InnerMsg.Value, &inner); err != nil {
		t.Fatal(err)
	}
	if inner.Sender != testDelegator || inner.Recipient.Owner != testDelegator {
		t.Errorf("deposit must be principal-to-principal, got sender %s recipient %s",
			inner.Sender, inner.Recipient.Owner)
	}
	if inner.Recipient.Number != 0 || inner.Quantums != 25_500_000 {
		t.Errorf("deposit target/amount wrong: %+v", inner)
	}

	// Deposits are not short-term CLOB msgs, so the wrapper must carry a fee.
	var txRaw txtypes.TxRaw
	if err := proto.Unmarshal(f.broadcast.txBytes, &txRaw); err != nil {
		t.Fatal(err)
	}
	var authInfo txtypes.AuthInfo
	if err := proto.Unmarshal(txRaw.AuthInfoBytes, &authInfo); err != nil {
		t.Fatal(err)
	}
	if authInfo.Fee == nil || len(authInfo.Fee.Amount) == 0 {
		t.Error("a delegated deposit must ride the fee-paying route")
	}
}

func TestExecuteDepositRefusals(t *testing.T) {
	t.Run("ungranted action", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.ExecuteDepositToSubaccount(context.Background(), ExecDepositInput{
			Proof:   f.issue(t, nil), // fixture default: place + cancel only
			Deposit: DepositParams{SubaccountNumber: 0, HumanUSDC: "1"},
		})
		if err == nil || !strings.Contains(err.Error(), "does not grant action") {
			t.Errorf("ungranted action must be refused by name, got %v", err)
		}
	})

	t.Run("ungranted subaccount", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.ExecuteDepositToSubaccount(context.Background(), ExecDepositInput{
			Proof:   depositProof(t, f),
			Deposit: DepositParams{SubaccountNumber: 3, HumanUSDC: "1"},
		})
		if err == nil || !strings.Contains(err.Error(), "does not grant subaccount") {
			t.Errorf("ungranted subaccount must be refused by name, got %v", err)
		}
	})

	t.Run("per-tx cap exceeded", func(t *testing.T) {
		f := newFixture(t)
		f.svc.cfg.Limits = limits.Config{DepositMaxUSDC: 10}
		_, err := f.svc.ExecuteDepositToSubaccount(context.Background(), ExecDepositInput{
			Proof:   depositProof(t, f),
			Deposit: DepositParams{SubaccountNumber: 0, HumanUSDC: "11"},
		})
		if err == nil || !strings.Contains(err.Error(), "deposit_max_usdc exceeded") {
			t.Errorf("cap breach must be refused by name, got %v", err)
		}
		if f.broadcast.txBytes != nil {
			t.Error("a refused deposit must never reach broadcast")
		}
	})

	t.Run("malformed amount", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.ExecuteDepositToSubaccount(context.Background(), ExecDepositInput{
			Proof:   depositProof(t, f),
			Deposit: DepositParams{SubaccountNumber: 0, HumanUSDC: "not-a-number"},
		})
		if err == nil || !strings.Contains(err.Error(), "human_usdc") {
			t.Errorf("malformed amount must be refused by field name, got %v", err)
		}
	})
}
