package auth

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

func TestApplyAuthQuotaReserveMetadata(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		metadata    map[string]any
		wantPercent string
		wantMode    string
	}{
		{name: "hard", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25), "mode": "hard"}}, wantPercent: "25", wantMode: QuotaReserveModeHard},
		{name: "soft", provider: "claude", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(40), "mode": "soft"}}, wantPercent: "40", wantMode: QuotaReserveModeSoft},
		{name: "mode case and spaces", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(10), "mode": " HARD "}}, wantPercent: "10", wantMode: QuotaReserveModeHard},
		{name: "missing mode defaults to soft", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25)}}, wantPercent: "25", wantMode: QuotaReserveModeSoft},
		{name: "int percent", provider: "Claude", metadata: map[string]any{"quota_reserve": map[string]any{"percent": 1}}, wantPercent: "1", wantMode: QuotaReserveModeSoft},
		{name: "upper bound", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(99)}}, wantPercent: "99", wantMode: QuotaReserveModeSoft},
		{name: "json number percent", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": json.Number("35"), "mode": "hard"}}, wantPercent: "35", wantMode: QuotaReserveModeHard},
		{name: "missing key", provider: "codex", metadata: map[string]any{}},
		{name: "nil metadata", provider: "codex"},
		{name: "null value", provider: "codex", metadata: map[string]any{"quota_reserve": nil}},
		{name: "not an object", provider: "codex", metadata: map[string]any{"quota_reserve": float64(25)}},
		{name: "missing percent", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"mode": "hard"}}},
		{name: "percent zero", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(0)}}},
		{name: "percent hundred", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(100)}}},
		{name: "percent negative", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(-5)}}},
		{name: "percent fractional", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25.5)}}},
		{name: "percent string", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": "25"}}},
		{name: "json number fractional", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": json.Number("25.5")}}},
		{name: "json number malformed", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": json.Number("x")}}},
		{name: "null mode defaults to soft", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25), "mode": nil}}, wantPercent: "25", wantMode: QuotaReserveModeSoft},
		{name: "unknown mode", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25), "mode": "strict"}}},
		{name: "mode not a string", provider: "codex", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25), "mode": true}}},
		{name: "unsupported provider", provider: "gemini", metadata: map[string]any{"quota_reserve": map[string]any{"percent": float64(25), "mode": "hard"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := &Auth{
				Provider: tt.provider,
				Attributes: map[string]string{
					AttributeQuotaReservePercent: "50",
					AttributeQuotaReserveMode:    QuotaReserveModeHard,
				},
			}
			ApplyAuthQuotaReserveMetadata(auth, tt.metadata)
			gotPercent, hasPercent := auth.Attributes[AttributeQuotaReservePercent]
			gotMode, hasMode := auth.Attributes[AttributeQuotaReserveMode]
			if tt.wantPercent == "" {
				if hasPercent || hasMode {
					t.Fatalf("attributes = %q/%q, want removed", gotPercent, gotMode)
				}
				return
			}
			if gotPercent != tt.wantPercent || gotMode != tt.wantMode {
				t.Fatalf("attributes = %q/%q, want %q/%q", gotPercent, gotMode, tt.wantPercent, tt.wantMode)
			}
		})
	}
}

func TestApplyAuthQuotaReserveMetadataNilSafety(t *testing.T) {
	ApplyAuthQuotaReserveMetadata(nil, map[string]any{"quota_reserve": map[string]any{"percent": float64(25)}})

	auth := &Auth{Provider: "codex"}
	ApplyAuthQuotaReserveMetadata(auth, map[string]any{"quota_reserve": map[string]any{"percent": float64(25)}})
	if got := auth.Attributes[AttributeQuotaReservePercent]; got != "25" {
		t.Fatalf("percent attribute = %q, want 25", got)
	}
}

func TestAuthQuotaReserve(t *testing.T) {
	tests := []struct {
		name        string
		auth        *Auth
		wantPercent int
		wantHard    bool
		wantOK      bool
	}{
		{name: "nil auth"},
		{name: "no attributes", auth: &Auth{Provider: "codex"}},
		{name: "hard", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeQuotaReservePercent: "25", AttributeQuotaReserveMode: "hard"}}, wantPercent: 25, wantHard: true, wantOK: true},
		{name: "soft", auth: &Auth{Provider: "claude", Attributes: map[string]string{AttributeQuotaReservePercent: "30", AttributeQuotaReserveMode: "soft"}}, wantPercent: 30, wantOK: true},
		{name: "missing mode is soft", auth: &Auth{Provider: "claude", Attributes: map[string]string{AttributeQuotaReservePercent: "30"}}, wantPercent: 30, wantOK: true},
		{name: "invalid percent", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeQuotaReservePercent: "x"}}},
		{name: "out of range percent", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeQuotaReservePercent: "100"}}},
		{name: "unknown mode", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeQuotaReservePercent: "25", AttributeQuotaReserveMode: "strict"}}},
		{name: "unsupported provider", auth: &Auth{Provider: "gemini", Attributes: map[string]string{AttributeQuotaReservePercent: "25", AttributeQuotaReserveMode: "hard"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			percent, hard, ok := authQuotaReserve(tt.auth)
			if percent != tt.wantPercent || hard != tt.wantHard || ok != tt.wantOK {
				t.Fatalf("authQuotaReserve() = %d/%v/%v, want %d/%v/%v", percent, hard, ok, tt.wantPercent, tt.wantHard, tt.wantOK)
			}
		})
	}
}

func reserveVerdictAuth(provider string, percent string, observedAt time.Time, signals map[string]string) *Auth {
	return &Auth{
		ID:         provider + "-reserve",
		Provider:   provider,
		Attributes: map[string]string{AttributeQuotaReservePercent: percent, AttributeQuotaReserveMode: QuotaReserveModeHard},
		Quota:      QuotaState{ObservedAt: observedAt, Signals: signals},
	}
}

func TestQuotaReserveVerdict(t *testing.T) {
	now := time.Unix(2000000000, 0)
	observed := now.Add(-time.Minute)
	unix := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	codex := func(primaryUsed, primaryReset, secondaryUsed, secondaryReset string) map[string]string {
		signals := map[string]string{}
		if primaryUsed != "" {
			signals["X-Codex-Primary-Used-Percent"] = primaryUsed
		}
		if primaryReset != "" {
			signals["X-Codex-Primary-Reset-At"] = primaryReset
		}
		if secondaryUsed != "" {
			signals["X-Codex-Secondary-Used-Percent"] = secondaryUsed
		}
		if secondaryReset != "" {
			signals["X-Codex-Secondary-Reset-At"] = secondaryReset
		}
		return signals
	}
	claude := func(shortUsed, shortReset, longUsed, longReset string) map[string]string {
		signals := map[string]string{}
		if shortUsed != "" {
			signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = shortUsed
		}
		if shortReset != "" {
			signals["Anthropic-Ratelimit-Unified-5h-Reset"] = shortReset
		}
		if longUsed != "" {
			signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = longUsed
		}
		if longReset != "" {
			signals["Anthropic-Ratelimit-Unified-7d-Reset"] = longReset
		}
		return signals
	}
	tests := []struct {
		name      string
		auth      *Auth
		wantUntil time.Duration
		active    bool
	}{
		{name: "codex healthy", auth: reserveVerdictAuth("codex", "25", observed, codex("50", unix(time.Hour), "40", unix(48*time.Hour)))},
		{name: "codex primary trips", auth: reserveVerdictAuth("codex", "25", observed, codex("80", unix(time.Hour), "40", unix(48*time.Hour))), active: true, wantUntil: time.Hour},
		{name: "codex secondary trips", auth: reserveVerdictAuth("codex", "25", observed, codex("10", unix(time.Hour), "90", unix(48*time.Hour))), active: true, wantUntil: 48 * time.Hour},
		{name: "codex both trip uses latest reset", auth: reserveVerdictAuth("codex", "25", observed, codex("90", unix(time.Hour), "90", unix(48*time.Hour))), active: true, wantUntil: 48 * time.Hour},
		{name: "codex relative reset anchored to observation", auth: reserveVerdictAuth("codex", "25", observed, map[string]string{
			"X-Codex-Primary-Used-Percent":        "80",
			"X-Codex-Primary-Reset-After-Seconds": "3660",
		}), active: true, wantUntil: time.Hour},
		{name: "codex boundary remaining equals percent", auth: reserveVerdictAuth("codex", "25", observed, codex("75", unix(time.Hour), "", ""))},
		{name: "codex fractional just below boundary", auth: reserveVerdictAuth("codex", "25", observed, codex("75.5", unix(time.Hour), "", "")), active: true, wantUntil: time.Hour},
		{name: "codex expired window ignored", auth: reserveVerdictAuth("codex", "25", observed, codex("90", unix(-time.Second), "10", unix(48*time.Hour)))},
		{name: "codex reset equal to now ignored", auth: reserveVerdictAuth("codex", "25", observed, codex("90", unix(0), "", ""))},
		{name: "codex window without reset ignored", auth: reserveVerdictAuth("codex", "25", observed, codex("90", "", "", ""))},
		{name: "codex malformed used ignored", auth: reserveVerdictAuth("codex", "25", observed, codex("lots", unix(time.Hour), "", ""))},
		{name: "codex over-limit used trips", auth: reserveVerdictAuth("codex", "25", observed, codex("150", unix(time.Hour), "", "")), active: true, wantUntil: time.Hour},
		{name: "codex negative used ignored", auth: reserveVerdictAuth("codex", "25", observed, codex("-1", unix(time.Hour), "", ""))},
		{name: "codex exhausted trips", auth: reserveVerdictAuth("codex", "25", observed, codex("100", unix(time.Hour), "", "")), active: true, wantUntil: time.Hour},
		{name: "claude healthy", auth: reserveVerdictAuth("claude", "30", observed, claude("0.2", rfc(time.Hour), "0.36", rfc(72*time.Hour)))},
		{name: "claude 5h trips", auth: reserveVerdictAuth("claude", "30", observed, claude("0.8", rfc(time.Hour), "0.36", rfc(72*time.Hour))), active: true, wantUntil: time.Hour},
		{name: "claude 7d trips", auth: reserveVerdictAuth("claude", "70", observed, claude("0.2", rfc(time.Hour), "0.36", rfc(72*time.Hour))), active: true, wantUntil: 72 * time.Hour},
		{name: "claude unix reset", auth: reserveVerdictAuth("claude", "30", observed, claude("0.8", unix(time.Hour), "", "")), active: true, wantUntil: time.Hour},
		{name: "claude boundary remaining equals percent", auth: reserveVerdictAuth("claude", "30", observed, claude("0.7", rfc(time.Hour), "", ""))},
		{name: "claude boundary float rounding", auth: reserveVerdictAuth("claude", "43", observed, claude("0.57", rfc(time.Hour), "", ""))},
		{name: "claude expired window ignored", auth: reserveVerdictAuth("claude", "30", observed, claude("0.9", rfc(-time.Minute), "", ""))},
		{name: "claude malformed utilization ignored", auth: reserveVerdictAuth("claude", "30", observed, claude("ninety", rfc(time.Hour), "", ""))},
		{name: "claude over-limit utilization trips", auth: reserveVerdictAuth("claude", "30", observed, claude("1.05", rfc(time.Hour), "0.2", rfc(72*time.Hour))), active: true, wantUntil: time.Hour},
		{name: "claude malformed reset ignored", auth: reserveVerdictAuth("claude", "30", observed, claude("0.9", "soon", "", ""))},
		{name: "no signals", auth: reserveVerdictAuth("codex", "25", observed, nil)},
		{name: "no observation time", auth: reserveVerdictAuth("codex", "25", time.Time{}, codex("90", unix(time.Hour), "", ""))},
		{name: "future observation", auth: reserveVerdictAuth("codex", "25", now.Add(time.Minute), codex("90", unix(time.Hour), "", ""))},
		{name: "no reserve configured", auth: &Auth{ID: "plain", Provider: "codex", Quota: QuotaState{ObservedAt: observed, Signals: codex("90", unix(time.Hour), "", "")}}},
		{name: "unsupported provider", auth: reserveVerdictAuth("gemini", "25", observed, codex("90", unix(time.Hour), "", ""))},
		{name: "nil auth"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active, until := QuotaReserveVerdict(tt.auth, now)
			if active != tt.active {
				t.Fatalf("active = %v, want %v", active, tt.active)
			}
			if !tt.active {
				if !until.IsZero() {
					t.Fatalf("until = %v, want zero when inactive", until)
				}
				return
			}
			if want := now.Add(tt.wantUntil); !until.Equal(want) {
				t.Fatalf("until = %v, want %v", until, want)
			}
		})
	}
}

func TestQuotaReserveVerdictUsesNewestSnapshot(t *testing.T) {
	now := time.Unix(2000000000, 0)
	reset := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	healthy := map[string]string{"X-Codex-Primary-Used-Percent": "10", "X-Codex-Primary-Reset-At": reset}
	tripped := map[string]string{"X-Codex-Primary-Used-Percent": "90", "X-Codex-Primary-Reset-At": reset}

	auth := reserveVerdictAuth("codex", "25", now.Add(-2*time.Minute), tripped)
	auth.ModelStates = map[string]*ModelState{"gpt-5": {Quota: QuotaState{ObservedAt: now.Add(-time.Minute), Signals: healthy}}}
	if active, _ := QuotaReserveVerdict(auth, now); active {
		t.Fatal("newer healthy model snapshot must supersede older tripped auth snapshot")
	}

	auth = reserveVerdictAuth("codex", "25", now.Add(-2*time.Minute), healthy)
	auth.ModelStates = map[string]*ModelState{"gpt-5": {Quota: QuotaState{ObservedAt: now.Add(-time.Minute), Signals: tripped}}, "nil-model": nil}
	if active, _ := QuotaReserveVerdict(auth, now); !active {
		t.Fatal("newer tripped model snapshot must supersede older healthy auth snapshot")
	}

	// Unrelated signals (e.g. a bare Retry-After) must not hide the last window observation.
	auth = reserveVerdictAuth("codex", "25", now.Add(-2*time.Minute), tripped)
	auth.ModelStates = map[string]*ModelState{"gpt-5": {Quota: QuotaState{ObservedAt: now.Add(-time.Minute), Signals: map[string]string{"Retry-After": "5"}}}}
	if active, _ := QuotaReserveVerdict(auth, now); !active {
		t.Fatal("snapshot without window signals must not supersede a window observation")
	}

	if snapshot := newestSubscriptionSnapshot(auth, nil); snapshot != nil {
		t.Fatalf("snapshot without window prefixes = %+v, want nil", snapshot)
	}
}

func TestQuotaReserveForAuth(t *testing.T) {
	tests := []struct {
		name        string
		auth        *Auth
		wantPercent int
		wantMode    string
		wantOK      bool
	}{
		{name: "nil", auth: nil},
		{name: "none", auth: &Auth{Provider: "codex"}},
		{name: "soft default", auth: &Auth{Provider: "codex", Attributes: map[string]string{AttributeQuotaReservePercent: "25"}}, wantPercent: 25, wantMode: QuotaReserveModeSoft, wantOK: true},
		{name: "hard", auth: &Auth{Provider: "claude", Attributes: map[string]string{AttributeQuotaReservePercent: "40", AttributeQuotaReserveMode: QuotaReserveModeHard}}, wantPercent: 40, wantMode: QuotaReserveModeHard, wantOK: true},
		{name: "unsupported provider", auth: &Auth{Provider: "gemini", Attributes: map[string]string{AttributeQuotaReservePercent: "25"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			percent, mode, ok := QuotaReserveForAuth(tt.auth)
			if percent != tt.wantPercent || mode != tt.wantMode || ok != tt.wantOK {
				t.Fatalf("QuotaReserveForAuth() = (%d, %q, %v), want (%d, %q, %v)", percent, mode, ok, tt.wantPercent, tt.wantMode, tt.wantOK)
			}
		})
	}
}
