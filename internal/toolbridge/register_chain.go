package toolbridge

import (
	"github.com/svpchain/svpchain-dex-agent/internal/agentchain"
)

// RegisterAgentChain adds the x/agent and x/agentwallet operations: queries
// over both modules and unsigned tx building for the registration and
// delegation lifecycles.
func (r *Registry) RegisterAgentChain(s *agentchain.Service) {
	// x/agent queries.
	r.add(SkillAgentRegistry, "get_agent", adaptNative(s.GetAgent))
	r.add(SkillAgentRegistry, "get_agent_by_operator", adaptNative(s.GetAgentByOperator))
	r.add(SkillAgentRegistry, "list_agents", adaptNative(s.ListAgents))
	r.add(SkillAgentRegistry, "get_agents_by_owner", adaptNative(s.GetAgentsByOwner))
	r.add(SkillAgentRegistry, "get_agents_by_capability", adaptNative(s.GetAgentsByCapability))
	r.add(SkillAgentRegistry, "get_agent_params", adaptNative(s.GetAgentParams))

	// x/agent lifecycle builds (owner signs).
	r.add(SkillAgentRegistry, "build_register_agent", adaptNative(s.BuildRegisterAgent))
	r.add(SkillAgentRegistry, "build_update_agent", adaptNative(s.BuildUpdateAgent))
	r.add(SkillAgentRegistry, "build_deposit_bond", adaptNative(s.BuildDepositBond))
	r.add(SkillAgentRegistry, "build_withdraw_bond", adaptNative(s.BuildWithdrawBond))
	r.add(SkillAgentRegistry, "build_deregister_agent", adaptNative(s.BuildDeregisterAgent))

	// x/agentwallet queries.
	r.add(SkillDelegation, "get_delegation", adaptNative(s.GetDelegation))
	r.add(SkillDelegation, "get_delegations_by_delegator", adaptNative(s.GetDelegationsByDelegator))
	r.add(SkillDelegation, "get_delegation_epoch", adaptNative(s.GetDelegationEpoch))
	r.add(SkillDelegation, "get_delegation_spend", adaptNative(s.GetDelegationSpend))
	r.add(SkillDelegation, "get_agentwallet_params", adaptNative(s.GetAgentwalletParams))

	// x/agentwallet delegation lifecycle builds (delegator signs).
	r.add(SkillDelegation, "build_create_delegation", adaptNative(s.BuildCreateDelegation))
	r.add(SkillDelegation, "build_update_delegation", adaptNative(s.BuildUpdateDelegation))
	r.add(SkillDelegation, "build_pause_delegation", adaptNative(s.BuildPauseDelegation))
	r.add(SkillDelegation, "build_resume_delegation", adaptNative(s.BuildResumeDelegation))
	r.add(SkillDelegation, "build_revoke_delegation", adaptNative(s.BuildRevokeDelegation))
	r.add(SkillDelegation, "build_revoke_token", adaptNative(s.BuildRevokeToken))
}
