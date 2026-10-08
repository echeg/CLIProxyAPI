package auth

import (
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

// quotaReserveVerdict reports whether the newest subscription window observation
// leaves less remaining quota than the configured reserve in any window. until is
// the latest reset among tripping windows, when the reserve stops holding.
// Expired windows, windows without a usable reset, and malformed values are
// ignored so a reserve can never pin a credential beyond its observed reset.
func quotaReserveVerdict(auth *Auth, now time.Time) (active bool, until time.Time) {
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
