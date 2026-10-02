package auth

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// EarliestResetSelector consumes available subscription quota in reset order.
// Existing credential priorities and availability still apply. Known future
// resets precede unknown observations; equally early resets use stable ID order.
// Without usable observations it falls back to round-robin. Session affinity
// can wrap this selector to keep established threads on their bound credential.
type EarliestResetSelector struct {
	fallback RoundRobinSelector
	nowFunc  func() time.Time
}

func (s *EarliestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	if s.nowFunc != nil {
		now = s.nowFunc()
	}
	available, errAvailable := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	var selected *Auth
	var earliest time.Time
	for _, candidate := range available {
		reset, ok := subscriptionResetForAuth(candidate, model, now)
		if !ok {
			continue
		}
		if selected == nil || reset.Before(earliest) || (reset.Equal(earliest) && candidate.ID < selected.ID) {
			selected, earliest = candidate, reset
		}
	}
	if selected != nil {
		return selected, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.fallback.Pick(context.WithValue(ctx, prevalidatedAuthCandidatesKey{}, true), provider, model, opts, available)
}

// subscriptionResetForAuth uses one observation, never a merge of timestamps
// from separate responses. A newer exhausted or malformed snapshot must not
// resurrect an older healthy observation. These signals influence ranking only;
// execution results remain responsible for credential cooldowns.
func subscriptionResetForAuth(auth *Auth, model string, now time.Time) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	prefixes := subscriptionWindowPrefixes(auth.Provider)
	if len(prefixes) == 0 {
		return time.Time{}, false
	}
	var snapshot *QuotaState
	consider := func(quota *QuotaState) {
		if quota != nil && hasSubscriptionWindowSignals(quota.Signals, prefixes) &&
			(snapshot == nil || quota.ObservedAt.After(snapshot.ObservedAt)) {
			snapshot = quota
		}
	}
	consider(&auth.Quota)
	if state := auth.ModelStates[model]; state != nil {
		consider(&state.Quota)
	}
	if baseModel := canonicalModelKey(model); baseModel != model {
		if state := auth.ModelStates[baseModel]; state != nil {
			consider(&state.Quota)
		}
	}
	if snapshot == nil || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) {
		return time.Time{}, false
	}
	claude := strings.EqualFold(strings.TrimSpace(auth.Provider), "claude")
	if !claude && (strings.EqualFold(quotaSignal(snapshot.Signals, "x-codex-allowed"), "false") ||
		strings.EqualFold(quotaSignal(snapshot.Signals, "x-codex-limit-reached"), "true")) {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, prefix := range prefixes {
		var reset time.Time
		var usedRaw string
		maximum := 100.0
		if claude {
			reset, _ = subscriptionResetTimestamp(quotaSignal(snapshot.Signals, prefix+"reset"))
			usedRaw = quotaSignal(snapshot.Signals, prefix+"utilization")
			maximum = 1
		} else {
			reset = codexSubscriptionReset(snapshot, prefix)
			usedRaw = quotaSignal(snapshot.Signals, prefix+"used-percent")
		}
		// Expired windows are unknown until a fresh upstream observation arrives.
		if !reset.IsZero() && !reset.After(now) {
			continue
		}
		if claude && strings.EqualFold(quotaSignal(snapshot.Signals, prefix+"status"), "rejected") {
			return time.Time{}, false
		}
		used, errParse := strconv.ParseFloat(usedRaw, 64)
		if errParse != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > maximum {
			continue
		}
		// Exhaustion of either shared window prevents usable subscription capacity.
		if used == maximum {
			return time.Time{}, false
		}
		if !reset.After(now) {
			continue
		}
		if earliest.IsZero() || reset.Before(earliest) {
			earliest = reset
		}
	}
	return earliest, !earliest.IsZero()
}

func subscriptionWindowPrefixes(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return []string{"x-codex-primary-", "x-codex-secondary-"}
	case "claude":
		// Model-specific Fable, overage, and additional limits are deliberately
		// excluded: their applicability cannot be inferred from a shared snapshot.
		return []string{"anthropic-ratelimit-unified-5h-", "anthropic-ratelimit-unified-7d-"}
	default:
		return nil
	}
}

func hasSubscriptionWindowSignals(signals map[string]string, prefixes []string) bool {
	for name := range signals {
		name = strings.ToLower(name)
		// A newer response may report rejection without repeating window data.
		// Such a snapshot must supersede an older, healthy model observation.
		switch name {
		case "x-codex-allowed", "x-codex-limit-reached", "anthropic-ratelimit-unified-status":
			return true
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	return false
}

func quotaSignal(signals map[string]string, name string) string {
	for key, value := range signals {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func codexSubscriptionReset(snapshot *QuotaState, prefix string) time.Time {
	if raw := quotaSignal(snapshot.Signals, prefix+"reset-at"); raw != "" {
		// An explicit absolute deadline is authoritative, including when expired.
		if reset, ok := subscriptionResetTimestamp(raw); ok {
			return reset
		}
	}
	seconds, errParse := strconv.ParseInt(quotaSignal(snapshot.Signals, prefix+"reset-after-seconds"), 10, 64)
	if errParse != nil || seconds < 0 || seconds > int64(math.MaxInt64)/int64(time.Second) {
		return time.Time{}
	}
	return snapshot.ObservedAt.Add(time.Duration(seconds) * time.Second)
}

func subscriptionResetTimestamp(raw string) (time.Time, bool) {
	if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds > 0 {
		// Restrict timestamps to the range representable by RFC3339 rather than
		// accepting overflowed or millisecond Unix timestamps as distant resets.
		if seconds <= 253402300799 {
			return time.Unix(seconds, 0), true
		}
	}
	reset, errParse := time.Parse(time.RFC3339Nano, raw)
	return reset, errParse == nil
}
