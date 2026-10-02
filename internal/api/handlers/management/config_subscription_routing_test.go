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

func TestConfigV8SubscriptionRoutingPatchPreservesOtherSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\nrouting:\n  strategy: round-robin\n  retry:\n    request-retry: 2\n"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	handler := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PATCH("/v8/management/config/*path", handler.ConfigV8)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/v8/management/config/routing", strings.NewReader(
		`{"strategy":"earliest-reset","session-affinity":true,"session-affinity-ttl":"24h"}`,
	)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", recorder.Code, recorder.Body.String())
	}
	loaded, errReload := config.LoadConfig(path)
	if errReload != nil {
		t.Fatal(errReload)
	}
	if loaded.Routing.Strategy != "earliest-reset" || !loaded.Routing.SessionAffinity || loaded.Routing.SessionAffinityTTL != "24h" {
		t.Fatalf("saved routing = %+v", loaded.Routing)
	}
	if loaded.Port != 8317 || loaded.RequestRetry != 2 {
		t.Fatal("routing patch replaced unrelated configuration")
	}
}
