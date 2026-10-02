package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestConfigV8PreferredAccountsPatchAndClear(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\nrouting:\n  strategy: earliest-reset\n  session-affinity: true\n  preferred-accounts:\n    claude: claude-index\n"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	handler := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PATCH("/v8/management/config", handler.ConfigV8)
	for _, testCase := range []struct {
		patch string
		want  map[string]string
	}{
		{`{"routing":{"preferred-accounts":{"codex":"codex-index"}}}`, map[string]string{"codex": "codex-index", "claude": "claude-index"}},
		{`{"routing":{"preferred-accounts":{"codex":null}}}`, map[string]string{"claude": "claude-index"}},
		{`{"routing":{"preferred-accounts":null}}`, nil},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(testCase.patch)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("patch status %d: %s", recorder.Code, recorder.Body.String())
		}
		loaded, errReload := config.LoadConfig(path)
		if errReload != nil {
			t.Fatal(errReload)
		}
		if !reflect.DeepEqual(loaded.Routing.PreferredAccounts, testCase.want) {
			t.Fatalf("preferences = %+v, want %+v", loaded.Routing.PreferredAccounts, testCase.want)
		}
		if loaded.Routing.Strategy != "earliest-reset" || !loaded.Routing.SessionAffinity || loaded.Port != 8317 {
			t.Fatal("preference patch replaced unrelated settings")
		}
	}
}
