package auth

import (
	"net/http"
	"strings"
	"time"
)

// ObserveQuotaProbe records a successful provider usage probe without treating it
// as a generation result, resetting cooldowns, or persisting credential data.
// startedAt must be captured before sending the probe: a response that arrives
// late must not replace observations collected since that request began.
func (m *Manager) ObserveQuotaProbe(base *Auth, headers http.Header, startedAt time.Time) bool {
	if m == nil || base == nil || base.ID == "" || startedAt.IsZero() {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(base.Provider))
	if provider != "codex" && provider != "claude" {
		return false
	}
	var observation QuotaState
	if !observation.ObserveResponseHeadersForProvider(provider, headers, startedAt) {
		return false
	}
	m.mu.Lock()
	current := m.auths[base.ID]
	if current == nil || current.RegistrationEpoch != base.RegistrationEpoch ||
		!strings.EqualFold(strings.TrimSpace(current.Provider), provider) ||
		(provider == "codex" && authMetadataString(current, "account_id") != authMetadataString(base, "account_id")) ||
		CredentialsChanged(current, base) || current.Quota.ObservedAt.After(startedAt) {
		m.mu.Unlock()
		return false
	}
	current.Quota = mergeQuotaObservation(current.Quota, observation)
	current.Generation++
	snapshot := current.Clone()
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuthResult(snapshot, nil, true)
	}
	return true
}
