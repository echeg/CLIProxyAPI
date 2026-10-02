package auth

import (
	"sort"
	"strings"
	"time"
)

// RoutingActivityAccount describes the latest selection of a current credential.
// It is not an in-flight request or a claim that all sessions use this account.
type RoutingActivityAccount struct {
	AuthIndex      string    `json:"auth_index"`
	Provider       string    `json:"provider"`
	LastSelectedAt time.Time `json:"last_selected_at"`
}

type routingActivityRecord struct {
	RoutingActivityAccount
	registrationEpoch uint64
}

func (m *Manager) publishSelectedAuthMetadata(meta map[string]any, auth *Auth) {
	m.recordRoutingSelection(auth, time.Now())
	publishSelectedAuthMetadata(meta, auth)
}

func (m *Manager) recordRoutingSelection(auth *Auth, selectedAt time.Time) {
	if m == nil || auth == nil || selectedAt.IsZero() {
		return
	}
	// Take manager locks before the activity lock, never in reverse order.
	// Keeping the registration check and write together excludes stale selections
	// arriving after a credential has been removed or replaced.
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.auths[auth.ID]
	if current == nil || current.RegistrationEpoch != auth.RegistrationEpoch ||
		current.Index != auth.Index || current.Provider != auth.Provider || current.Index == "" {
		return
	}
	m.routingActivityMu.Lock()
	defer m.routingActivityMu.Unlock()
	if m.routingActivity == nil {
		m.routingActivity = make(map[string]routingActivityRecord)
	}
	previous := m.routingActivity[auth.ID]
	if previous.registrationEpoch == auth.RegistrationEpoch && !selectedAt.After(previous.LastSelectedAt) {
		return
	}
	m.routingActivity[auth.ID] = routingActivityRecord{
		RoutingActivityAccount: RoutingActivityAccount{
			AuthIndex:      current.Index,
			Provider:       strings.ToLower(strings.TrimSpace(current.Provider)),
			LastSelectedAt: selectedAt.UTC(),
		},
		registrationEpoch: current.RegistrationEpoch,
	}
}

// RoutingActivity returns a detached snapshot without exposing credentials or sessions.
// History is process-local and excludes removed or re-registered credentials.
func (m *Manager) RoutingActivity() []RoutingActivityAccount {
	accounts := make([]RoutingActivityAccount, 0)
	if m == nil {
		return accounts
	}
	m.mu.RLock()
	m.routingActivityMu.Lock()
	for authID, activity := range m.routingActivity {
		current := m.auths[authID]
		if current == nil || current.RegistrationEpoch != activity.registrationEpoch ||
			current.Index != activity.AuthIndex || strings.ToLower(strings.TrimSpace(current.Provider)) != activity.Provider {
			delete(m.routingActivity, authID)
			continue
		}
		accounts = append(accounts, activity.RoutingActivityAccount)
	}
	m.routingActivityMu.Unlock()
	m.mu.RUnlock()
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Provider != accounts[j].Provider {
			return accounts[i].Provider < accounts[j].Provider
		}
		return accounts[i].AuthIndex < accounts[j].AuthIndex
	})
	return accounts
}
