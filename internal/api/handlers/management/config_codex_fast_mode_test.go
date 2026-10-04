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

func TestConfigV8CodexFastModeToggle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\noauth:\n  providers:\n    codex:\n      model-level-cooling: true\n"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	handler := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", handler.ConfigV8)
	for _, want := range []bool{true, false} {
		body := "false"
		if want {
			body = "true"
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/oauth/providers/codex/fast-mode", strings.NewReader(body)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("put status %d: %s", recorder.Code, recorder.Body.String())
		}
		loaded, errReload := config.LoadConfig(path)
		if errReload != nil {
			t.Fatal(errReload)
		}
		if loaded.Codex.FastMode != want || !loaded.Codex.ModelLevelCooling || loaded.Port != 8317 {
			t.Fatalf("saved codex = %+v, want fast-mode %t with other settings preserved", loaded.Codex, want)
		}
		if loaded.ForAPIKey().Codex.FastMode {
			t.Fatal("API-key view inherited OAuth-only fast-mode")
		}
	}
}
