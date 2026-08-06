package delegated

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/svpchain/svpdt"

	"github.com/svpchain/svpchain-mcp/lib/mcp/policy"
	"github.com/svpchain/svpchain-mcp/lib/mcp/tools"
)

// ReadTenantSource resolves proof-derived tenants for the policy engine, so
// the vendored query handlers — which authorize through a TenantContext and
// policy.Engine.Tenant — serve delegated reads unchanged. A verified
// credential chain is admitted as a synthetic tenant scoped to its principal
// and subaccount caveat, living exactly as long as the leaf token.
//
// The tenant id is derived from the chain hash, so the same credential
// re-admits to the same tenant while polling instead of growing the table.
type ReadTenantSource struct {
	mu   sync.Mutex
	byID map[string]readTenant
	now  func() int64
}

type readTenant struct {
	policy  policy.TenantPolicy
	expires int64
}

// maxReadTenants bounds the table. Entries expire with their leaf token
// (≤ the chain's max TTL), so the cap only bites under a deliberate flood of
// distinct credentials — at which point refusing new admissions is the right
// failure mode.
const maxReadTenants = 4096

// NewReadTenantSource builds the source. now == nil means wall clock.
func NewReadTenantSource(now func() int64) *ReadTenantSource {
	if now == nil {
		now = func() int64 { return time.Now().Unix() }
	}
	return &ReadTenantSource{byID: map[string]readTenant{}, now: now}
}

// Admit registers the verified chain as a synthetic tenant and returns the
// TenantContext the tool handlers read. The "svpdt-" prefix keeps the id space
// disjoint from the bearer store's "auto-" tenants.
func (s *ReadTenantSource) Admit(v *svpdt.Verified) tools.TenantContext {
	id := "svpdt-" + hex.EncodeToString(v.ChainHash[:8])

	subs := make(map[uint32]struct{}, len(v.Effective.Subaccounts))
	for _, n := range v.Effective.Subaccounts {
		subs[n] = struct{}{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	if _, exists := s.byID[id]; !exists && len(s.byID) >= maxReadTenants {
		// Refuse to grow; the caller's lookup will fail with unknown-tenant,
		// which is the honest answer under a credential flood.
		return tools.TenantContext{TenantID: id, Owner: v.Principal}
	}
	s.byID[id] = readTenant{
		policy: policy.TenantPolicy{
			TenantID:           id,
			Owner:              v.Principal,
			AllowedSubaccounts: subs,
		},
		expires: v.LeafExp,
	}
	return tools.TenantContext{TenantID: id, Owner: v.Principal}
}

// LookupTenantPolicy implements policy.DynamicSource.
func (s *ReadTenantSource) LookupTenantPolicy(tenantID string) (policy.TenantPolicy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[tenantID]
	if !ok || s.now() >= t.expires {
		return policy.TenantPolicy{}, false
	}
	return t.policy, true
}

// sweepLocked drops expired entries. Called under mu on every Admit — the
// table is small and admissions are rare enough that a linear pass is fine.
func (s *ReadTenantSource) sweepLocked() {
	now := s.now()
	for id, t := range s.byID {
		if now >= t.expires {
			delete(s.byID, id)
		}
	}
}
