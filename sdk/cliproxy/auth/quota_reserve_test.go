package auth

import "testing"

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
