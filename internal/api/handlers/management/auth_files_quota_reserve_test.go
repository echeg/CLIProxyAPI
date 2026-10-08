package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func patchQuotaReserveFields(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFields(ctx)
	return rec
}

func readPersistedAuthFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var persisted map[string]any
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	return persisted
}

func TestPatchAuthFileFields_QuotaReservePersistsAndSyncsRuntime(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "reserved.json"
	filePath := filepath.Join(authDir, fileName)
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "codex",
		Attributes: map[string]string{"path": filePath},
		Metadata:   map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	steps := []struct {
		name        string
		value       string
		wantPercent string
		wantMode    string
	}{
		{name: "soft", value: `{"percent":25,"mode":"soft"}`, wantPercent: "25", wantMode: "soft"},
		{name: "hard normalized", value: `{"percent":40,"mode":" HARD "}`, wantPercent: "40", wantMode: "hard"},
		{name: "missing mode defaults to soft", value: `{"percent":30}`, wantPercent: "30", wantMode: "soft"},
		{name: "null deletes", value: `null`},
	}
	for _, step := range steps {
		rec := patchQuotaReserveFields(t, h, `{"name":"reserved.json","quota_reserve":`+step.value+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body=%s", step.name, rec.Code, rec.Body.String())
		}
		updated, ok := manager.GetByID(fileName)
		if !ok {
			t.Fatalf("%s: auth missing after patch", step.name)
		}
		persisted := readPersistedAuthFile(t, filePath)
		if step.wantPercent == "" {
			if _, exists := updated.Attributes[coreauth.AttributeQuotaReservePercent]; exists {
				t.Fatalf("%s: percent attribute remains", step.name)
			}
			if _, exists := updated.Attributes[coreauth.AttributeQuotaReserveMode]; exists {
				t.Fatalf("%s: mode attribute remains", step.name)
			}
			if _, exists := persisted["quota_reserve"]; exists {
				t.Fatalf("%s: persisted quota_reserve remains: %#v", step.name, persisted["quota_reserve"])
			}
			continue
		}
		if got := updated.Attributes[coreauth.AttributeQuotaReservePercent]; got != step.wantPercent {
			t.Fatalf("%s: percent attribute = %q, want %q", step.name, got, step.wantPercent)
		}
		if got := updated.Attributes[coreauth.AttributeQuotaReserveMode]; got != step.wantMode {
			t.Fatalf("%s: mode attribute = %q, want %q", step.name, got, step.wantMode)
		}
		reserve, ok := persisted["quota_reserve"].(map[string]any)
		if !ok {
			t.Fatalf("%s: persisted quota_reserve = %#v, want object", step.name, persisted["quota_reserve"])
		}
		if reserve["percent"] != float64(mustAtoi(t, step.wantPercent)) || reserve["mode"] != step.wantMode {
			t.Fatalf("%s: persisted quota_reserve = %#v", step.name, reserve)
		}
	}
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	out, errAtoi := strconv.Atoi(value)
	if errAtoi != nil {
		t.Fatalf("Atoi(%q) error = %v", value, errAtoi)
	}
	return out
}

func TestPatchAuthFileFields_RejectsInvalidQuotaReserve(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:         "claude.json",
		FileName:   "claude.json",
		Provider:   "claude",
		Attributes: map[string]string{coreauth.AttributeQuotaReservePercent: "20", coreauth.AttributeQuotaReserveMode: "soft"},
		Metadata:   map[string]any{"type": "claude", "quota_reserve": map[string]any{"percent": float64(20), "mode": "soft"}},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	cases := []struct {
		name    string
		field   string
		value   string
		wantErr string
	}{
		{name: "not an object", field: "quota_reserve", value: `25`, wantErr: "quota_reserve must be an object"},
		{name: "array", field: "quota_reserve", value: `[25]`, wantErr: "quota_reserve must be an object"},
		{name: "missing percent", field: "quota_reserve", value: `{"mode":"hard"}`, wantErr: "quota_reserve.percent"},
		{name: "percent zero", field: "quota_reserve", value: `{"percent":0}`, wantErr: "quota_reserve.percent"},
		{name: "percent hundred", field: "quota_reserve", value: `{"percent":100}`, wantErr: "quota_reserve.percent"},
		{name: "percent negative", field: "quota_reserve", value: `{"percent":-1}`, wantErr: "quota_reserve.percent"},
		{name: "percent fractional", field: "quota_reserve", value: `{"percent":25.5}`, wantErr: "quota_reserve.percent"},
		{name: "percent string", field: "quota_reserve", value: `{"percent":"25"}`, wantErr: "quota_reserve.percent"},
		{name: "unknown mode", field: "quota_reserve", value: `{"percent":25,"mode":"strict"}`, wantErr: "quota_reserve.mode"},
		{name: "nested field", field: "quota_reserve.percent", value: `25`, wantErr: "quota_reserve does not support nested fields"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := patchQuotaReserveFields(t, h, `{"name":"claude.json","`+tc.field+`":`+tc.value+`}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("body = %s, want error containing %q", rec.Body.String(), tc.wantErr)
			}
			current, _ := manager.GetByID("claude.json")
			if current.Attributes[coreauth.AttributeQuotaReservePercent] != "20" {
				t.Fatalf("existing reserve changed to %q", current.Attributes[coreauth.AttributeQuotaReservePercent])
			}
		})
	}
}

func TestPatchAuthFileFields_QuotaReserveRejectsUnsupportedProvider(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "gemini.json",
		FileName: "gemini.json",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini", "quota_reserve": map[string]any{"percent": float64(20)}},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)

	rec := patchQuotaReserveFields(t, h, `{"name":"gemini.json","quota_reserve":{"percent":25,"mode":"hard"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "quota reserve is supported for codex and claude") {
		t.Fatalf("body = %s, want unsupported provider error", rec.Body.String())
	}

	// Deleting a stale reserve stays allowed for any provider.
	rec = patchQuotaReserveFields(t, h, `{"name":"gemini.json","quota_reserve":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	current, _ := manager.GetByID("gemini.json")
	if _, exists := current.Metadata["quota_reserve"]; exists {
		t.Fatalf("stale quota_reserve remains: %#v", current.Metadata["quota_reserve"])
	}
}

func TestListAuthFilesExposesQuotaReserveVerdict(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	reset7d := now.Add(48 * time.Hour)
	reset5h := now.Add(2 * time.Hour)
	manager := coreauth.NewManager(nil, nil, nil)
	authDir := t.TempDir()
	claudeSignals := func(utilization5h, utilization7d string) map[string]string {
		return map[string]string{
			"anthropic-ratelimit-unified-5h-utilization": utilization5h,
			"anthropic-ratelimit-unified-5h-reset":       time.Unix(reset5h.Unix(), 0).UTC().Format(time.RFC3339),
			"anthropic-ratelimit-unified-7d-utilization": utilization7d,
			"anthropic-ratelimit-unified-7d-reset":       time.Unix(reset7d.Unix(), 0).UTC().Format(time.RFC3339),
		}
	}
	records := []*coreauth.Auth{
		{
			ID:       "tripped.json",
			FileName: "tripped.json",
			Provider: "claude",
			Attributes: map[string]string{
				"path":                                filepath.Join(authDir, "tripped.json"),
				coreauth.AttributeQuotaReservePercent: "30",
				coreauth.AttributeQuotaReserveMode:    "hard",
			},
			Quota: coreauth.QuotaState{ObservedAt: now.Add(-time.Minute), Signals: claudeSignals("0.1", "0.8")},
		},
		{
			ID:       "healthy.json",
			FileName: "healthy.json",
			Provider: "claude",
			Attributes: map[string]string{
				"path":                                filepath.Join(authDir, "healthy.json"),
				coreauth.AttributeQuotaReservePercent: "30",
			},
			Quota: coreauth.QuotaState{ObservedAt: now.Add(-time.Minute), Signals: claudeSignals("0.1", "0.7")},
		},
		{
			ID:         "plain.json",
			FileName:   "plain.json",
			Provider:   "codex",
			Attributes: map[string]string{"path": filepath.Join(authDir, "plain.json")},
		},
	}
	for _, record := range records {
		if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", record.ID, errRegister)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.nowFunc = func() time.Time { return now }

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	byName := make(map[string]map[string]any, len(payload.Files))
	for _, file := range payload.Files {
		name, _ := file["name"].(string)
		byName[name] = file
	}

	tripped := byName["tripped.json"]
	if tripped == nil {
		t.Fatalf("tripped.json missing from %s", rec.Body.String())
	}
	reserve, ok := tripped["quota_reserve"].(map[string]any)
	if !ok || reserve["percent"] != float64(30) || reserve["mode"] != "hard" {
		t.Fatalf("tripped quota_reserve = %#v", tripped["quota_reserve"])
	}
	if tripped["quota_reserve_active"] != true {
		t.Fatalf("tripped quota_reserve_active = %#v, want true", tripped["quota_reserve_active"])
	}
	if got := tripped["quota_reserve_until"]; got != reset7d.Format(time.RFC3339) {
		t.Fatalf("tripped quota_reserve_until = %#v, want %s", got, reset7d.Format(time.RFC3339))
	}

	healthy := byName["healthy.json"]
	reserve, ok = healthy["quota_reserve"].(map[string]any)
	if !ok || reserve["percent"] != float64(30) || reserve["mode"] != "soft" {
		t.Fatalf("healthy quota_reserve = %#v", healthy["quota_reserve"])
	}
	if healthy["quota_reserve_active"] != false {
		t.Fatalf("healthy quota_reserve_active = %#v, want false", healthy["quota_reserve_active"])
	}
	if _, exists := healthy["quota_reserve_until"]; exists {
		t.Fatalf("healthy quota_reserve_until = %#v, want absent", healthy["quota_reserve_until"])
	}

	plain := byName["plain.json"]
	if _, exists := plain["quota_reserve"]; exists {
		t.Fatalf("plain quota_reserve = %#v, want absent", plain["quota_reserve"])
	}
	if plain["quota_reserve_active"] != false {
		t.Fatalf("plain quota_reserve_active = %#v, want false", plain["quota_reserve_active"])
	}
	if _, exists := plain["quota_reserve_until"]; exists {
		t.Fatalf("plain quota_reserve_until = %#v, want absent", plain["quota_reserve_until"])
	}

	// Once the tripping window resets, the verdict clears without new observations.
	h.nowFunc = func() time.Time { return reset7d.Add(time.Second) }
	entry := h.buildAuthFileEntry(mustGetAuth(t, manager, "tripped.json"))
	if entry["quota_reserve_active"] != false {
		t.Fatalf("after reset quota_reserve_active = %#v, want false", entry["quota_reserve_active"])
	}
}

func mustGetAuth(t *testing.T, manager *coreauth.Manager, id string) *coreauth.Auth {
	t.Helper()
	auth, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %s missing", id)
	}
	return auth
}

func TestListAuthFilesFromDiskExposesQuotaReserve(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	files := map[string]string{
		"reserved.json":    `{"type":"codex","quota_reserve":{"percent":25,"mode":"hard"}}`,
		"soft.json":        `{"type":"claude","quota_reserve":{"percent":40}}`,
		"invalid.json":     `{"type":"codex","quota_reserve":{"percent":100}}`,
		"unsupported.json": `{"type":"gemini","quota_reserve":{"percent":25}}`,
		"plain.json":       `{"type":"codex"}`,
	}
	for name, content := range files {
		if errWrite := os.WriteFile(filepath.Join(authDir, name), []byte(content), 0o600); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, errWrite)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(payload.Files) != len(files) {
		t.Fatalf("files = %d, want %d: %s", len(payload.Files), len(files), rec.Body.String())
	}
	want := map[string]map[string]any{
		"reserved.json": {"percent": float64(25), "mode": "hard"},
		"soft.json":     {"percent": float64(40), "mode": "soft"},
	}
	for _, file := range payload.Files {
		name, _ := file["name"].(string)
		if file["quota_reserve_active"] != false {
			t.Fatalf("%s quota_reserve_active = %#v, want false", name, file["quota_reserve_active"])
		}
		if _, exists := file["quota_reserve_until"]; exists {
			t.Fatalf("%s quota_reserve_until = %#v, want absent", name, file["quota_reserve_until"])
		}
		reserve, exists := file["quota_reserve"]
		wantReserve, wantExists := want[name]
		if exists != wantExists {
			t.Fatalf("%s quota_reserve = %#v, want present=%t", name, reserve, wantExists)
		}
		if wantExists {
			got, _ := reserve.(map[string]any)
			if got["percent"] != wantReserve["percent"] || got["mode"] != wantReserve["mode"] {
				t.Fatalf("%s quota_reserve = %#v, want %#v", name, got, wantReserve)
			}
		}
	}
}
