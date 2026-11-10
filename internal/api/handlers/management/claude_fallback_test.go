package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestClaudeFallbackManagementSaveAndDisable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\nrouting:\n  strategy: earliest-reset\n"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	m := coreauth.NewManager(nil, nil, nil)
	h := &Handler{cfg: cfg, configFilePath: path, authManager: m}
	router := gin.New()
	router.GET("/routing/claude-fallback", h.GetClaudeFallback)
	router.PUT("/config/*path", h.ConfigV8)
	for _, enabled := range []bool{true, false} {
		body := `{"enabled":true,"mode":"always","mappings":[{"from":"claude-opus-*","to":"gpt-6-astra"}]}`
		if !enabled {
			body = strings.Replace(body, "true", "false", 1)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/config/routing/claude-fallback", strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
		loaded, errReload := config.LoadConfig(path)
		if errReload != nil {
			t.Fatal(errReload)
		}
		// A legacy write is stored as providers.claude.fallback; read it back through the legacy view.
		saved := loaded.Routing.SharedProfile().EffectiveProviders().Claude.Fallback.LegacyClaudeFallback()
		if saved.Enabled != enabled || saved.Mode != "always" || len(saved.Mappings) != 1 || loaded.Routing.Strategy != "earliest-reset" {
			t.Fatalf("lost settings: %+v", loaded.Routing)
		}
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/routing/claude-fallback", nil))
		var state struct {
			Supported bool
			Settings  config.ClaudeFallbackConfig
		}
		if errDecode := json.Unmarshal(rec.Body.Bytes(), &state); errDecode != nil {
			t.Fatal(errDecode)
		}
		if !state.Supported || state.Settings.Enabled != enabled || state.Settings.Mode != "always" {
			t.Fatal(rec.Body.String())
		}
	}
}

func TestClaudeFallbackManagementCatalog(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	for _, kind := range []string{coreauth.AuthKindOAuth, coreauth.AuthKindAPIKey} {
		id := t.Name() + kind
		_, errRegister := m.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{coreauth.AttributeAuthKind: kind}, Metadata: map[string]any{"access_token": "secret-value"}})
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: kind + "-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	h := &Handler{cfg: &config.Config{}, authManager: m}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h.GetClaudeFallback(c)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "oauth-model") || strings.Contains(body, "apikey-model") || strings.Contains(body, "secret-value") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(body)
	}
	m.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	h.GetClaudeFallback(c)
	if !strings.Contains(rec.Body.String(), `"supported":false`) {
		t.Fatal(rec.Body.String())
	}
}
