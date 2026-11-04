package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func writeTestConfigFile(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write test config: %v", errWrite)
	}
	return path
}

func TestDeleteGeminiKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 2 {
		t.Fatalf("gemini keys len = %d, want 2", got)
	}
}

func TestDeleteGeminiKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			GeminiKey: []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://a.example.com", nil)

	h.DeleteGeminiKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.GeminiKey); got != 1 {
		t.Fatalf("gemini keys len = %d, want 1", got)
	}
	if got := h.cfg.GeminiKey[0].BaseURL; got != "https://b.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://b.example.com")
	}
}

func TestDeleteGeminiStyleKeyRejectsAmbiguousRoutingIdentity(t *testing.T) {
	tests := []struct {
		name         string
		interactions bool
	}{
		{name: "Gemini"},
		{name: "Interactions", interactions: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-a"},
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-b"},
			}
			cfg := &config.Config{}
			path := "/v0/management/gemini-api-key?api-key=shared-key&base-url=https://shared.example.com"
			if tc.interactions {
				cfg.InteractionsKey = entries
				path = "/v0/management/interactions-api-key?api-key=shared-key&base-url=https://shared.example.com"
			} else {
				cfg.GeminiKey = entries
			}
			handler := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodDelete, path, nil)

			if tc.interactions {
				handler.DeleteInteractionsKey(ctx)
			} else {
				handler.DeleteGeminiKey(ctx)
			}

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			remaining := cfg.GeminiKey
			if tc.interactions {
				remaining = cfg.InteractionsKey
			}
			if len(remaining) != 2 {
				t.Fatalf("remaining credential count = %d, want 2", len(remaining))
			}
		})
	}
}

func TestPatchGeminiStyleKeyRoutingIdentity(t *testing.T) {
	tests := []struct {
		name         string
		interactions bool
		firstBase    string
		wantStatus   int
	}{
		{name: "Gemini unique base URL", firstBase: "https://first.example.com", wantStatus: http.StatusOK},
		{name: "Gemini ambiguous base URL", firstBase: "https://shared.example.com", wantStatus: http.StatusBadRequest},
		{name: "Interactions unique base URL", interactions: true, firstBase: "https://first.example.com", wantStatus: http.StatusOK},
		{name: "Interactions ambiguous base URL", interactions: true, firstBase: "https://shared.example.com", wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := []config.GeminiKey{
				{APIKey: "shared-key", BaseURL: tc.firstBase, Prefix: "team-a"},
				{APIKey: "shared-key", BaseURL: "https://shared.example.com", Prefix: "team-b"},
			}
			cfg := &config.Config{}
			path := "/v0/management/gemini-api-key?base-url=https://shared.example.com"
			if tc.interactions {
				cfg.InteractionsKey = entries
				path = "/v0/management/interactions-api-key?base-url=https://shared.example.com"
			} else {
				cfg.GeminiKey = entries
			}
			handler := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPatch, path, strings.NewReader(`{"match":"shared-key","value":{"prefix":"updated"}}`))

			if tc.interactions {
				handler.PatchInteractionsKey(ctx)
			} else {
				handler.PatchGeminiKey(ctx)
			}

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			remaining := cfg.GeminiKey
			if tc.interactions {
				remaining = cfg.InteractionsKey
			}
			if tc.wantStatus == http.StatusOK {
				if remaining[0].Prefix != "team-a" || remaining[1].Prefix != "updated" {
					t.Fatalf("prefixes = %q, %q; want team-a, updated", remaining[0].Prefix, remaining[1].Prefix)
				}
			} else if remaining[0].Prefix != "team-a" || remaining[1].Prefix != "team-b" {
				t.Fatalf("ambiguous patch changed prefixes to %q, %q", remaining[0].Prefix, remaining[1].Prefix)
			}
		})
	}
}

func TestDeleteClaudeKey_DeletesEmptyBaseURLWhenExplicitlyProvided(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "shared-key", BaseURL: ""},
				{APIKey: "shared-key", BaseURL: "https://claude.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/claude-api-key?api-key=shared-key&base-url=", nil)

	h.DeleteClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude keys len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].BaseURL; got != "https://claude.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://claude.example.com")
	}
}

func TestDeleteVertexCompatKey_DeletesOnlyMatchingBaseURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/vertex-api-key?api-key=shared-key&base-url=https://b.example.com", nil)

	h.DeleteVertexCompatKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.VertexCompatAPIKey); got != 1 {
		t.Fatalf("vertex keys len = %d, want 1", got)
	}
	if got := h.cfg.VertexCompatAPIKey[0].BaseURL; got != "https://a.example.com" {
		t.Fatalf("remaining base-url = %q, want %q", got, "https://a.example.com")
	}
}

func TestDeleteXAIKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			XAIKey: []config.XAIKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/xai-api-key?api-key=shared-key", nil)

	h.DeleteXAIKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.XAIKey); got != 2 {
		t.Fatalf("xAI keys len = %d, want 2", got)
	}
}

func TestDeleteMetaKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			MetaKey: []config.MetaKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/meta-api-key?api-key=shared-key", nil)

	h.DeleteMetaKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.MetaKey); got != 2 {
		t.Fatalf("Meta keys len = %d, want 2", got)
	}
}

func TestDeleteCodexKey_RequiresBaseURLWhenAPIKeyDuplicated(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			CodexKey: []config.CodexKey{
				{APIKey: "shared-key", BaseURL: "https://a.example.com"},
				{APIKey: "shared-key", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-api-key?api-key=shared-key", nil)

	h.DeleteCodexKey(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := len(h.cfg.CodexKey); got != 2 {
		t.Fatalf("codex keys len = %d, want 2", got)
	}
}

func TestAPIKeyMutationsReconcileKeyProfiles(t *testing.T) {
	type step struct {
		method, url, body string
		gone              []string // keys whose profile must be absent from runtime and file
		present           []string // keys whose profile must stay or appear
	}
	cases := []struct {
		name string
		step step
	}{
		{"rotation moves", step{http.MethodPatch, "/api-keys", `{"old":"key-a","new":"key-a2"}`, []string{"key-a"}, []string{"key-a2", "key-b"}}},
		{"append moves nothing", step{http.MethodPatch, "/api-keys", `{"old":"missing","new":"key-n"}`, []string{"key-n"}, []string{"key-a", "key-b"}}},
		{"index patch to a new value rotates", step{http.MethodPatch, "/api-keys", `{"index":0,"value":"key-a3"}`, []string{"key-a"}, []string{"key-a3", "key-b"}}},
		{"index patch to a listed value drops", step{http.MethodPatch, "/api-keys", `{"index":0,"value":"key-c"}`, []string{"key-a"}, []string{"key-b"}}},
		{"index delete drops", step{http.MethodDelete, "/api-keys?index=1", "", []string{"key-b"}, []string{"key-a"}}},
		{"value delete drops", step{http.MethodDelete, "/api-keys?value=key-a", "", []string{"key-a"}, []string{"key-b"}}},
		{"put dropping a key drops", step{http.MethodPut, "/api-keys", `["key-b","key-c"]`, []string{"key-a"}, []string{"key-b"}}},
		{"put replacing every key drops all", step{http.MethodPut, "/api-keys", `["x","y"]`, []string{"key-a", "key-b"}, nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, path := newRoutingProfilesTestHandler(t, routingProfilesTestConfig)
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set(ConfigV8ContextKey, true) })
			router.PUT("/api-keys", h.PutAPIKeys)
			router.PATCH("/api-keys", h.PatchAPIKeys)
			router.DELETE("/api-keys", h.DeleteAPIKeys)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tc.step.method, tc.step.url, strings.NewReader(tc.step.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			saved, _ := savedKeyProfiles(t, path)
			loaded, errLoad := config.LoadConfig(path)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			for _, key := range tc.step.gone {
				if _, ok := h.cfg.Routing.KeyProfiles[key]; ok {
					t.Fatalf("runtime kept the profile of %s", key)
				}
				if _, ok := saved[key]; ok {
					t.Fatalf("file kept the profile of %s: %v", key, saved)
				}
			}
			for _, key := range tc.step.present {
				if h.cfg.Routing.KeyProfiles[key] == nil || saved[key] == nil || loaded.Routing.KeyProfiles[key] == nil {
					t.Fatalf("profile of %s missing: runtime=%v file=%v", key, h.cfg.Routing.KeyProfiles, saved)
				}
			}
			if len(tc.step.present) == 0 && h.cfg.Routing.KeyProfiles != nil {
				t.Fatalf("runtime profiles = %v", h.cfg.Routing.KeyProfiles)
			}
		})
	}
}

func TestAPIKeyRotationCarriesProfileContents(t *testing.T) {
	h, path := newRoutingProfilesTestHandler(t, routingProfilesTestConfig)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(ConfigV8ContextKey, true) })
	router.PATCH("/api-keys", h.PatchAPIKeys)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api-keys", strings.NewReader(`{"old":"key-b","new":"key-b2"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "key-b:") || strings.Contains(string(saved), `"key-b"`) {
		t.Fatalf("old key still on disk:\n%s", saved)
	}
	loaded, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	moved := loaded.Routing.KeyProfiles["key-b2"]
	if moved == nil || moved.PreferredAccounts["codex"] != "codex-9" || !moved.ClaudeFallback.Enabled || moved.ClaudeFallback.Mode != "always" {
		t.Fatalf("moved profile = %+v", moved)
	}
}
