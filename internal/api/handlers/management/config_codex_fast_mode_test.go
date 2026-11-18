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
	"gopkg.in/yaml.v3"
)

// legacyCodexFastModeConfig still spells the removed oauth.providers.codex.fast-mode key.
const legacyCodexFastModeConfig = "config-version: 8\nserver:\n  port: 8317\noauth:\n  providers:\n    codex:\n      fast-mode: true\n      model-level-cooling: true\n      response-steering: true\n"

func writeLegacyCodexFastModeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(legacyCodexFastModeConfig), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return path
}

func TestConfigV8LeftoverCodexFastModeLoads(t *testing.T) {
	path := writeLegacyCodexFastModeConfig(t)
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("config with leftover fast-mode failed to load: %v", errLoad)
	}
	if !cfg.Codex.ModelLevelCooling || !cfg.Codex.ResponseSteering || cfg.Port != 8317 {
		t.Fatalf("loaded codex = %+v port %d, want other settings preserved", cfg.Codex, cfg.Port)
	}
	codex := cfg.Routing.SharedProfile().EffectiveProviders().Codex
	if codex.Speed != config.CodexSpeedStandard || codex.IgnoreClientTier {
		t.Fatalf("shared codex policy = %+v, want speed standard without ignore-client-tier", codex)
	}
}

func TestConfigV8LeftoverCodexFastModeAcceptsWrites(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := writeLegacyCodexFastModeConfig(t)
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	handler := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PATCH("/v8/management/config", handler.ConfigV8)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"requests":{"proxy-url":"direct"}}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", recorder.Code, recorder.Body.String())
	}
	saved, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var doc yaml.Node
	if errUnmarshal := yaml.Unmarshal(saved, &doc); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if configV8Node(doc.Content[0], []string{"oauth", "providers", "codex", "fast-mode"}) != nil {
		t.Fatalf("saved file still has an active fast-mode key:\n%s", saved)
	}
	if errValidate := config.ValidateV8Config(saved); errValidate != nil {
		t.Fatalf("saved file is not a valid v8 config: %v", errValidate)
	}
	loaded, errReload := config.LoadConfig(path)
	if errReload != nil {
		t.Fatal(errReload)
	}
	if !loaded.Codex.ModelLevelCooling || !loaded.Codex.ResponseSteering || loaded.Port != 8317 || loaded.ProxyURL != "direct" {
		t.Fatalf("saved config = codex %+v port %d proxy %q, want other settings preserved and the patch applied", loaded.Codex, loaded.Port, loaded.ProxyURL)
	}
}
