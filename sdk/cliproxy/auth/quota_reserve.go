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
	// AttributeQuotaReserveSteps stores additional soft thresholds as a JSON array.
	AttributeQuotaReserveSteps = "quota_reserve_steps"

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

// QuotaReservePolicy defines a credential's remaining-quota reserve. Percent is
// the first threshold; Steps optionally adds up to two strictly lower soft thresholds.
// A single soft threshold remains level 1 even below another credential's later levels.
type QuotaReservePolicy struct {
	Percent int    `json:"percent"`
	Mode    string `json:"mode"`
	Steps   []int  `json:"steps,omitempty"`
}

// ParseQuotaReserve validates a raw quota_reserve value decoded from JSON.
// It retains the original single-threshold API and ignores additional fields.
// Use ParseQuotaReservePolicy to validate and read steps.
func ParseQuotaReserve(raw any) (int, string, error) {
	if object, ok := raw.(map[string]any); ok {
		raw = map[string]any{"percent": object["percent"], "mode": object["mode"]}
	}
	policy, errParse := ParseQuotaReservePolicy(raw)
	if errParse != nil {
		return 0, "", errParse
	}
	return policy.Percent, policy.Mode, nil
}

// ParseQuotaReservePolicy validates and normalizes a raw quota_reserve JSON object.
func ParseQuotaReservePolicy(raw any) (QuotaReservePolicy, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return QuotaReservePolicy{}, fmt.Errorf("quota_reserve must be an object")
	}
	percent, errPercent := parseQuotaReservePercent(object["percent"])
	if errPercent != nil {
		return QuotaReservePolicy{}, errPercent
	}
	mode := QuotaReserveModeSoft
	if rawMode, exists := object["mode"]; exists && rawMode != nil {
		modeString, isString := rawMode.(string)
		if !isString {
			return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.mode must be %q or %q", QuotaReserveModeSoft, QuotaReserveModeHard)
		}
		mode = strings.ToLower(strings.TrimSpace(modeString))
		if mode != QuotaReserveModeSoft && mode != QuotaReserveModeHard {
			return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.mode must be %q or %q", QuotaReserveModeSoft, QuotaReserveModeHard)
		}
	}
	policy := QuotaReservePolicy{Percent: percent, Mode: mode}
	if rawSteps, exists := object["steps"]; exists {
		var values []any
		switch typed := rawSteps.(type) {
		case []any:
			values = typed
		case []int:
			values = make([]any, len(typed))
			for i, step := range typed {
				values[i] = step
			}
		default:
			return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.steps must be an array")
		}
		if len(values) > 2 {
			return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.steps supports at most two additional thresholds")
		}
		if len(values) > 0 && mode == QuotaReserveModeHard {
			return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.steps is only supported in soft mode")
		}
		previous := percent
		for i, rawStep := range values {
			step, errStep := parseQuotaReservePercent(rawStep)
			if errStep != nil {
				return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.steps[%d] must be an integer between 1 and 99", i)
			}
			if step >= previous {
				return QuotaReservePolicy{}, fmt.Errorf("quota_reserve.steps must strictly decrease below quota_reserve.percent")
			}
			policy.Steps = append(policy.Steps, step)
			previous = step
		}
	}
	return policy, nil
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
	delete(auth.Attributes, AttributeQuotaReserveSteps)
	raw, ok := metadata[quotaReserveMetadataKey]
	if !ok || raw == nil {
		return
	}
	if !QuotaReserveSupportedProvider(auth.Provider) {
		log.WithFields(log.Fields{"auth_id": auth.ID, "provider": auth.Provider}).Warn("ignoring quota_reserve: provider does not support quota reserve")
		return
	}
	policy, errParse := ParseQuotaReservePolicy(raw)
	if errParse != nil {
		log.WithField("auth_id", auth.ID).Warnf("ignoring invalid quota_reserve: %v", errParse)
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributeQuotaReservePercent] = strconv.Itoa(policy.Percent)
	auth.Attributes[AttributeQuotaReserveMode] = policy.Mode
	if len(policy.Steps) > 0 {
		steps, _ := json.Marshal(policy.Steps)
		auth.Attributes[AttributeQuotaReserveSteps] = string(steps)
	}
}

// QuotaReservePolicyForAuth returns the complete reserve synced into an auth's attributes.
func QuotaReservePolicyForAuth(auth *Auth) (QuotaReservePolicy, bool) {
	if auth == nil || !QuotaReserveSupportedProvider(auth.Provider) {
		return QuotaReservePolicy{}, false
	}
	percent, errAtoi := strconv.Atoi(strings.TrimSpace(auth.Attributes[AttributeQuotaReservePercent]))
	if errAtoi != nil || percent < 1 || percent > 99 {
		return QuotaReservePolicy{}, false
	}
	mode := strings.TrimSpace(auth.Attributes[AttributeQuotaReserveMode])
	if mode == "" {
		mode = QuotaReserveModeSoft
	}
	if mode != QuotaReserveModeSoft && mode != QuotaReserveModeHard {
		return QuotaReservePolicy{}, false
	}
	policy := QuotaReservePolicy{Percent: percent, Mode: mode}
	if rawSteps, exists := auth.Attributes[AttributeQuotaReserveSteps]; exists {
		var steps []int
		if errDecode := json.Unmarshal([]byte(rawSteps), &steps); errDecode != nil || steps == nil {
			return QuotaReservePolicy{}, false
		}
		parsed, errParse := ParseQuotaReservePolicy(map[string]any{"percent": percent, "mode": mode, "steps": steps})
		if errParse != nil {
			return QuotaReservePolicy{}, false
		}
		policy = parsed
	}
	return policy, true
}

// QuotaReserveForAuth returns the reserve synced into an auth's attributes.
// It retains the original single-threshold API; use QuotaReservePolicyForAuth to read steps.
func QuotaReserveForAuth(auth *Auth) (percent int, mode string, ok bool) {
	policy, ok := QuotaReservePolicyForAuth(auth)
	if !ok {
		return 0, "", false
	}
	return policy.Percent, policy.Mode, true
}

// authQuotaReserve returns the configured reserve percent and whether it is hard.
func authQuotaReserve(auth *Auth) (percent int, hard bool, ok bool) {
	policy, ok := QuotaReservePolicyForAuth(auth)
	if !ok {
		return 0, false, false
	}
	return policy.Percent, policy.Mode == QuotaReserveModeHard, true
}

// QuotaReserveVerdict reports whether the newest subscription window observation
// leaves less remaining quota than the configured reserve in any window, so new
// selections avoid the auth. until is the latest reset among tripping windows,
// when the reserve stops holding.
// Expired windows, windows without a usable reset, and malformed values are
// ignored so a reserve can never pin a credential beyond its observed reset.
// Usage above the limit counts as nothing remaining.
func QuotaReserveVerdict(auth *Auth, now time.Time) (active bool, until time.Time) {
	level, until := QuotaReserveLevel(auth, now)
	return level > 0, until
}

// QuotaReserveLevel returns the worst active reserve level across usable windows:
// 0 above all thresholds, then 1 through 3 for successive soft thresholds. An active
// hard reserve has level 1; its policy mode distinguishes it from a soft reserve.
// until retains QuotaReserveVerdict's meaning: the last reset holding any reserve,
// so a level can improve before until when its worst window expires first.
func QuotaReserveLevel(auth *Auth, now time.Time) (level int, until time.Time) {
	policy, ok := QuotaReservePolicyForAuth(auth)
	if !ok {
		return 0, time.Time{}
	}
	return quotaReserveLevel(auth, policy, now)
}

func quotaReserveLevel(auth *Auth, policy QuotaReservePolicy, now time.Time) (level int, until time.Time) {
	prefixes := subscriptionWindowPrefixes(auth.Provider)
	snapshot := newestSubscriptionSnapshot(auth, prefixes)
	if snapshot == nil || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) {
		return 0, time.Time{}
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
		if errParse != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
			continue
		}
		// Usage past the limit (Claude overage reports utilization above 1.0)
		// leaves nothing remaining. Round away float noise from fractional
		// utilization (0.7*100 != 70).
		remaining := math.Max(0, math.Round((100-used*scale)*1e6)/1e6)
		if remaining >= float64(policy.Percent) {
			continue
		}
		windowLevel := 1
		for _, threshold := range policy.Steps {
			if remaining >= float64(threshold) {
				break
			}
			windowLevel++
		}
		level = max(level, windowLevel)
		if reset.After(until) {
			until = reset
		}
	}
	return level, until
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
// below a hard reserve are dropped. Among the remainder, only the lowest active
// soft level is retained, including level 0 for credentials above all thresholds.
// The deepest soft level stays eligible as a last resort. hardCount and recoverAt
// describe hard reserves for model cooldown reporting. The filter never writes state.
func quotaReserveFilter(auths []*Auth, now time.Time) (selectable []*Auth, hardCount int, recoverAt time.Time) {
	if !anyQuotaReserveConfigured(auths) {
		return auths, 0, time.Time{}
	}
	selectable = make([]*Auth, 0, len(auths))
	lowestLevel := 4
	for _, candidate := range auths {
		policy, configured := QuotaReservePolicyForAuth(candidate)
		level := 0
		var until time.Time
		if configured {
			level, until = quotaReserveLevel(candidate, policy, now)
		}
		if level > 0 && policy.Mode == QuotaReserveModeHard {
			hardCount++
			if recoverAt.IsZero() || until.Before(recoverAt) {
				recoverAt = until
			}
			continue
		}
		if level < lowestLevel {
			lowestLevel = level
			selectable = selectable[:0]
		}
		if level == lowestLevel {
			selectable = append(selectable, candidate)
		}
	}
	return selectable, hardCount, recoverAt
}

// applyQuotaReserveToBuckets applies quotaReserveFilter across every priority tier at
// once, so a higher reserve level yields to a lower one before priority narrowing.
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

// laterHardQuotaReserveDeadline returns when an auth blocked until next becomes selectable for
// new selections: the later of next and an active hard reserve's recovery.
func laterHardQuotaReserveDeadline(auth *Auth, next, now time.Time) time.Time {
	if until, held := hardQuotaReserveUntil(auth, now); held && until.After(next) {
		return until
	}
	return next
}

// withoutQuotaReserveFor returns auths with exempt replaced by a copy that has no quota reserve,
// so recovery calculations ignore the reserve only for that credential.
func withoutQuotaReserveFor(auths []*Auth, exempt *Auth) []*Auth {
	if exempt == nil {
		return auths
	}
	out := make([]*Auth, len(auths))
	for i, auth := range auths {
		if auth != exempt {
			out[i] = auth
			continue
		}
		unreserved := auth.Clone()
		delete(unreserved.Attributes, AttributeQuotaReservePercent)
		delete(unreserved.Attributes, AttributeQuotaReserveMode)
		delete(unreserved.Attributes, AttributeQuotaReserveSteps)
		out[i] = unreserved
	}
	return out
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
