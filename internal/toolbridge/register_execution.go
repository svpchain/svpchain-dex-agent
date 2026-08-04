package toolbridge

import (
	"github.com/svpchain/svpchain-dex-agent/internal/delegated"
)

// executionTools is the execution skill's operation set, shared by both the
// live and the refusing registration so the advertised surface is identical
// either way.
var executionTools = []string{
	"execute_place_order",
	"execute_cancel_order",
	"execute_batch_cancel",
	"execute_deposit_to_subaccount",
	"agent_identity",
	"agent_self_register",
	"agent_self_update",
}

// RegisterExecution adds the delegated-execution operations. A nil service
// (no operator key configured) registers the same tool names as refusals, so
// a caller learns the requirement — an operator key and an SVP-DT delegation
// proof — instead of meeting an unknown-tool error.
func (r *Registry) RegisterExecution(s *delegated.Service) {
	if s == nil {
		const reason = "delegated execution requires this deployment to configure an operator key " +
			"([operator] section); the skill is advertised so callers know it exists, but this " +
			"deployment cannot serve it"
		for _, tool := range executionTools {
			r.addRefusing(SkillExecution, tool, reason)
		}
		return
	}
	r.add(SkillExecution, "execute_place_order", adaptNative(s.ExecutePlaceOrder))
	r.add(SkillExecution, "execute_cancel_order", adaptNative(s.ExecuteCancelOrder))
	r.add(SkillExecution, "execute_batch_cancel", adaptNative(s.ExecuteBatchCancel))
	r.add(SkillExecution, "execute_deposit_to_subaccount", adaptNative(s.ExecuteDepositToSubaccount))
	r.add(SkillExecution, "agent_identity", adaptNative(s.Identity))
	r.add(SkillExecution, "agent_self_register", adaptNative(s.SelfRegister))
	r.add(SkillExecution, "agent_self_update", adaptNative(s.SelfUpdate))
}
