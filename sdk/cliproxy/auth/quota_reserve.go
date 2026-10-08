package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// AttributeQuotaReservePercent stores the remaining-quota percent kept free for external services.
	AttributeQuotaReservePercent = "quota_reserve_percent"
	// AttributeQuotaReserveMode stores how selection treats a credential below its reserve.
	AttributeQuotaReserveMode = "quota_reserve_mode"

	// QuotaReserveModeSoft keeps a credential below its reserve as a last resort.
	QuotaReserveModeSoft = "soft"
	// QuotaReserveModeHard excludes a credential below its reserve from new selections.
	QuotaReserveModeHard = "hard"

	quotaReserveMetadataKey = "quota_reserve"
)

// QuotaReserveSupportedProvider reports whether the provider exposes subscription windows usable by a reserve.
func QuotaReserveSupportedProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "claude":
		return true
	default:
		return false
	}
}

// ParseQuotaReserve validates a raw quota_reserve value decoded from JSON.
func ParseQuotaReserve(raw any) (int, string, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return 0, "", fmt.Errorf("quota_reserve must be an object")
	}
	percent, errPercent := parseQuotaReservePercent(object["percent"])
	if errPercent != nil {
		return 0, "", errPercent
	}
	mode := QuotaReserveModeSoft
	if rawMode, exists := object["mode"]; exists && rawMode != nil {
		modeString, isString := rawMode.(string)
		if !isString {
			return 0, "", fmt.Errorf("quota_reserve.mode must be %q or %q", QuotaReserveModeSoft, QuotaReserveModeHard)
		}
		mode = strings.ToLower(strings.TrimSpace(modeString))
		if mode != QuotaReserveModeSoft && mode != QuotaReserveModeHard {
			return 0, "", fmt.Errorf("quota_reserve.mode must be %q or %q", QuotaReserveModeSoft, QuotaReserveModeHard)
		}
	}
	return percent, mode, nil
}

func parseQuotaReservePercent(raw any) (int, error) {
	var value float64
	switch typed := raw.(type) {
	case float64:
		value = typed
	case int:
		value = float64(typed)
	case json.Number:
		parsed, errParse := typed.Float64()
		if errParse != nil {
			return 0, fmt.Errorf("quota_reserve.percent must be an integer between 1 and 99")
		}
		value = parsed
	default:
		return 0, fmt.Errorf("quota_reserve.percent must be an integer between 1 and 99")
	}
	if value != math.Trunc(value) || value < 1 || value > 99 {
		return 0, fmt.Errorf("quota_reserve.percent must be an integer between 1 and 99")
	}
	return int(value), nil
}

// ApplyAuthQuotaReserveMetadata copies a valid file quota reserve into an auth's routing attributes.
// Missing or invalid values remove any previously synced reserve.
func ApplyAuthQuotaReserveMetadata(auth *Auth, metadata map[string]any) {
	if auth == nil {
		return
	}
	delete(auth.Attributes, AttributeQuotaReservePercent)
	delete(auth.Attributes, AttributeQuotaReserveMode)
	raw, ok := metadata[quotaReserveMetadataKey]
	if !ok || raw == nil {
		return
	}
	if !QuotaReserveSupportedProvider(auth.Provider) {
		log.WithFields(log.Fields{"auth_id": auth.ID, "provider": auth.Provider}).Warn("ignoring quota_reserve: provider does not support quota reserve")
		return
	}
	percent, mode, errParse := ParseQuotaReserve(raw)
	if errParse != nil {
		log.WithField("auth_id", auth.ID).Warnf("ignoring invalid quota_reserve: %v", errParse)
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributeQuotaReservePercent] = strconv.Itoa(percent)
	auth.Attributes[AttributeQuotaReserveMode] = mode
}

// QuotaReserveForAuth returns the reserve synced into an auth's attributes.
func QuotaReserveForAuth(auth *Auth) (percent int, mode string, ok bool) {
	percent, hard, ok := authQuotaReserve(auth)
	if !ok {
		return 0, "", false
	}
	if hard {
		return percent, QuotaReserveModeHard, true
	}
	return percent, QuotaReserveModeSoft, true
}

// authQuotaReserve returns the configured reserve percent and whether it is hard.
func authQuotaReserve(auth *Auth) (percent int, hard bool, ok bool) {
	if auth == nil || !QuotaReserveSupportedProvider(auth.Provider) {
		return 0, false, false
	}
	percent, errAtoi := strconv.Atoi(strings.TrimSpace(auth.Attributes[AttributeQuotaReservePercent]))
	if errAtoi != nil || percent < 1 || percent > 99 {
		return 0, false, false
	}
	switch strings.TrimSpace(auth.Attributes[AttributeQuotaReserveMode]) {
	case "", QuotaReserveModeSoft:
		return percent, false, true
	case QuotaReserveModeHard:
		return percent, true, true
	default:
		return 0, false, false
	}
}

// QuotaReserveVerdict reports whether the newest subscription window observation
// leaves less remaining quota than the configured reserve in any window, so new
// selections avoid the auth. until is the latest reset among tripping windows,
// when the reserve stops holding.
// Expired windows, windows without a usable reset, and malformed values are
// ignored so a reserve can never pin a credential beyond its observed reset.
func QuotaReserveVerdict(auth *Auth, now time.Time) (active bool, until time.Time) {
	percent, _, ok := authQuotaReserve(auth)
	if !ok {
		return false, time.Time{}
	}
	prefixes := subscriptionWindowPrefixes(auth.Provider)
	snapshot := newestSubscriptionSnapshot(auth, prefixes)
	if snapshot == nil || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) {
		return false, time.Time{}
	}
	claude := strings.EqualFold(strings.TrimSpace(auth.Provider), "claude")
	for _, prefix := range prefixes {
		var reset time.Time
		var usedRaw string
		scale := 1.0
		if claude {
			reset, _ = subscriptionResetTimestamp(quotaSignal(snapshot.Signals, prefix+"reset"))
			usedRaw = quotaSignal(snapshot.Signals, prefix+"utilization")
			scale = 100
		} else {
			reset = codexSubscriptionReset(snapshot, prefix)
			usedRaw = quotaSignal(snapshot.Signals, prefix+"used-percent")
		}
		if !reset.After(now) {
			continue
		}
		used, errParse := strconv.ParseFloat(usedRaw, 64)
		if errParse != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used*scale > 100 {
			continue
		}
		// Round away float noise from fractional utilization (0.7*100 != 70).
		remaining := math.Round((100-used*scale)*1e6) / 1e6
		if remaining >= float64(percent) {
			continue
		}
		if reset.After(until) {
			until = reset
		}
	}
	return !until.IsZero(), until
}

// newestSubscriptionSnapshot returns the most recent observation carrying
// subscription window signals. Windows are account-wide, so any model's
// snapshot describes the whole credential; snapshots are never merged.
func newestSubscriptionSnapshot(auth *Auth, prefixes []string) *QuotaState {
	if len(prefixes) == 0 {
		return nil
	}
	var snapshot *QuotaState
	consider := func(quota *QuotaState) {
		if quota != nil && hasSubscriptionWindowSignals(quota.Signals, prefixes) &&
			(snapshot == nil || quota.ObservedAt.After(snapshot.ObservedAt)) {
			snapshot = quota
		}
	}
	consider(&auth.Quota)
	for _, state := range auth.ModelStates {
		if state != nil {
			consider(&state.Quota)
		}
	}
	return snapshot
}

// quotaReserveFilter narrows available candidates for a new selection. Credentials
// below a hard reserve are dropped; credentials below a soft reserve are kept only
// as a last-resort pool when no unreserved candidate remains. hardCount and
// recoverAt describe the dropped hard-reserved candidates so callers can report a
// model cooldown that lasts until the earliest reserve recovers. The filter never
// writes credential state. Without any configured reserve the input is returned.
func quotaReserveFilter(auths []*Auth, now time.Time) (selectable []*Auth, hardCount int, recoverAt time.Time) {
	if !anyQuotaReserveConfigured(auths) {
		return auths, 0, time.Time{}
	}
	unreserved := make([]*Auth, 0, len(auths))
	var soft []*Auth
	for _, candidate := range auths {
		active, until := QuotaReserveVerdict(candidate, now)
		if !active {
			unreserved = append(unreserved, candidate)
			continue
		}
		if _, hard, _ := authQuotaReserve(candidate); hard {
			hardCount++
			if recoverAt.IsZero() || until.Before(recoverAt) {
				recoverAt = until
			}
			continue
		}
		soft = append(soft, candidate)
	}
	if len(unreserved) > 0 {
		return unreserved, hardCount, recoverAt
	}
	if soft == nil {
		soft = []*Auth{}
	}
	return soft, hardCount, recoverAt
}

// applyQuotaReserveToBuckets applies quotaReserveFilter across every priority tier at
// once, so a reserved top tier yields to unreserved lower tiers before tier narrowing.
// Emptied tiers are removed from the map. Dropped hard-reserved candidates are added to
// cooldownCount, and earliest moves to their reserve recovery when that comes sooner.
func applyQuotaReserveToBuckets(buckets map[int][]*Auth, now time.Time, cooldownCount int, earliest time.Time) (int, time.Time) {
	configured := false
	total := 0
	for _, bucket := range buckets {
		total += len(bucket)
		configured = configured || anyQuotaReserveConfigured(bucket)
	}
	if !configured {
		return cooldownCount, earliest
	}
	all := make([]*Auth, 0, total)
	for _, bucket := range buckets {
		all = append(all, bucket...)
	}
	selectable, hardCount, recoverAt := quotaReserveFilter(all, now)
	cooldownCount += hardCount
	if !recoverAt.IsZero() && (earliest.IsZero() || recoverAt.Before(earliest)) {
		earliest = recoverAt
	}
	if len(selectable) == len(all) {
		return cooldownCount, earliest
	}
	keep := make(map[*Auth]struct{}, len(selectable))
	for _, candidate := range selectable {
		keep[candidate] = struct{}{}
	}
	for priority, bucket := range buckets {
		kept := bucket[:0]
		for _, candidate := range bucket {
			if _, ok := keep[candidate]; ok {
				kept = append(kept, candidate)
			}
		}
		if len(kept) == 0 {
			delete(buckets, priority)
		} else {
			buckets[priority] = kept
		}
	}
	return cooldownCount, earliest
}

func anyQuotaReserveConfigured(auths []*Auth) bool {
	for _, candidate := range auths {
		if _, _, ok := authQuotaReserve(candidate); ok {
			return true
		}
	}
	return false
}

// hardQuotaReserveUntil reports when an auth held by an active hard reserve becomes
// selectable again for new selections.
func hardQuotaReserveUntil(auth *Auth, now time.Time) (time.Time, bool) {
	if _, hard, _ := authQuotaReserve(auth); !hard {
		return time.Time{}, false
	}
	active, until := QuotaReserveVerdict(auth, now)
	return until, active
}

// quotaReserveSkippedContextKey marks a selection that must ignore the quota reserve.
type quotaReserveSkippedContextKey struct{}

// withQuotaReserveSkipped marks a selection that serves an existing binding, such as a
// pinned credential, so the reserve does not apply.
func withQuotaReserveSkipped(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, quotaReserveSkippedContextKey{}, true)
}

func quotaReserveSkipped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	skipped, _ := ctx.Value(quotaReserveSkippedContextKey{}).(bool)
	return skipped
}

// quotaReserveExhaustedContextKey carries the manager's error for a new selection that the
// quota reserve leaves without candidates.
type quotaReserveExhaustedContextKey struct{}

// withQuotaReserveExhaustedError hands the selector the error a reserve-aware availability
// pass reported. The selector only sees available credentials, so this error is the one that
// also accounts for credentials cooling down.
func withQuotaReserveExhaustedError(ctx context.Context, err error) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, quotaReserveExhaustedContextKey{}, err)
}

// quotaReserveExhaustedError reports a new selection whose available candidates are all held
// by a hard reserve, preferring the error supplied by the manager.
func quotaReserveExhaustedError(ctx context.Context, provider, model string, recoverAt, now time.Time) error {
	if ctx != nil {
		if errExhausted, _ := ctx.Value(quotaReserveExhaustedContextKey{}).(error); errExhausted != nil {
			return errExhausted
		}
	}
	return quotaReserveCooldownError(provider, model, recoverAt, now)
}

// quotaReserveCooldownError reports that every available candidate is held by a hard
// reserve. It reuses the model-cooldown 429 so Retry-After points at the earliest recovery.
func quotaReserveCooldownError(provider, model string, recoverAt, now time.Time) *modelCooldownError {
	if provider == "mixed" {
		provider = ""
	}
	return newModelCooldownError(model, provider, recoverAt.Sub(now))
}
