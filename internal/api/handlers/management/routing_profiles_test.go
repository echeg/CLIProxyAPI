package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const routingProfilesTestConfig = `config-version: 8
access:
  api-keys: ["key-a", "key-b", "key-c"]
routing:
  strategy: fill-first
  session-affinity: true
  preferred-accounts: {claude: "claude-1"}
  key-profiles:
    key-b:
      preferred-accounts: {codex: "codex-9"}
      claude-fallback: {enabled: true, mode: always, mappings: [{from: "claude-*", to: "gpt-6-sol"}]}
    key-a:
      session-affinity: true
      selection: {primary: priority, tiebreak: round-robin}
`

// newRoutingProfilesTestHandler loads raw into a temporary config file and returns
// a handler bound to it.
func newRoutingProfilesTestHandler(t *testing.T, raw string) (*Handler, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	return &Handler{cfg: cfg, configFilePath: path, authManager: coreauth.NewManager(nil, nil, nil)}, path
}

type routingProfilesResponse struct {
	Supported bool                  `json:"supported"`
	Shared    config.RoutingProfile `json:"shared"`
	Keys      []struct {
		Key     string                `json:"key"`
		Profile config.RoutingProfile `json:"profile"`
	} `json:"keys"`
	SelectionOptions struct {
		Narrowing []string `json:"narrowing"`
		Pickers   []string `json:"pickers"`
	} `json:"selection_options"`
}

func getRoutingProfiles(t *testing.T, h *Handler) (routingProfilesResponse, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/profiles", nil)
	h.GetRoutingProfiles(c)
	var out routingProfilesResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &out); errDecode != nil {
		t.Fatalf("decode: %v body=%s", errDecode, rec.Body.String())
	}
	return out, rec
}

func TestGetRoutingProfilesShape(t *testing.T) {
	h, _ := newRoutingProfilesTestHandler(t, routingProfilesTestConfig)
	out, rec := getRoutingProfiles(t, h)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v", rec.Code, rec.Header())
	}
	if !out.Supported {
		t.Fatal("supported = false for a native manager")
	}
	if !out.Shared.SessionAffinity || out.Shared.PreferredAccounts["claude"] != "claude-1" ||
		out.Shared.Selection != (config.SelectionConfig{Primary: "reserves", Tiebreak: "fill-first"}) {
		t.Fatalf("shared = %+v", out.Shared)
	}
	if len(out.Keys) != 2 || out.Keys[0].Key != "key-a" || out.Keys[1].Key != "key-b" {
		t.Fatalf("keys = %+v", out.Keys)
	}
	if out.Keys[0].Profile.Selection != (config.SelectionConfig{Primary: "priority", Tiebreak: "round-robin"}) || !out.Keys[0].Profile.SessionAffinity {
		t.Fatalf("key-a = %+v", out.Keys[0].Profile)
	}
	keyB := out.Keys[1].Profile
	if keyB.Selection != config.DefaultSelection() || keyB.PreferredAccounts["codex"] != "codex-9" || !keyB.ClaudeFallback.Enabled || len(keyB.ClaudeFallback.Mappings) != 1 {
		t.Fatalf("key-b = %+v", keyB)
	}
	if !reflect.DeepEqual(out.SelectionOptions.Narrowing, config.SelectionNarrowingSlots) || !reflect.DeepEqual(out.SelectionOptions.Pickers, config.SelectionPickerSlots) {
		t.Fatalf("options = %+v", out.SelectionOptions)
	}

	// Home mode resolves no profile, so the backend reports the feature unsupported.
	h.authManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	out, _ = getRoutingProfiles(t, h)
	if out.Supported {
		t.Fatal("supported = true under Home")
	}
}

func TestGetRoutingProfilesEmptyConfig(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h.GetRoutingProfiles(c)
	var raw map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &raw); errDecode != nil {
		t.Fatal(errDecode)
	}
	if raw["supported"] != false {
		t.Fatalf("supported without a manager: %v", raw["supported"])
	}
	if keys, ok := raw["keys"].([]any); !ok || len(keys) != 0 {
		t.Fatalf("keys must be an empty list: %v", raw["keys"])
	}
	shared := raw["shared"].(map[string]any)
	fallback := shared["claude-fallback"].(map[string]any)
	if mappings, ok := fallback["mappings"].([]any); !ok || len(mappings) != 0 {
		t.Fatalf("mappings must be an empty list: %v", fallback["mappings"])
	}
}

func TestReconcileKeyProfiles(t *testing.T) {
	profileA := &config.RoutingProfile{SessionAffinity: true}
	profileB := &config.RoutingProfile{Selection: config.SelectionConfig{Primary: "priority"}}
	cases := []struct {
		name          string
		before, after []string
		want          map[string]*config.RoutingProfile
	}{
		{"unchanged", []string{"a", "b"}, []string{"a", "b"}, map[string]*config.RoutingProfile{"a": profileA, "b": profileB}},
		{"rotation moves", []string{"a", "b"}, []string{"a2", "b"}, map[string]*config.RoutingProfile{"a2": profileA, "b": profileB}},
		{"rotation of an unprofiled key", []string{"a", "b", "c"}, []string{"a", "b", "d"}, map[string]*config.RoutingProfile{"a": profileA, "b": profileB}},
		{"append moves nothing", []string{"a", "b"}, []string{"a", "b", "n"}, map[string]*config.RoutingProfile{"a": profileA, "b": profileB}},
		{"removal drops", []string{"a", "b"}, []string{"b"}, map[string]*config.RoutingProfile{"b": profileB}},
		{"overwrite with a listed key drops", []string{"a", "b"}, []string{"b", "b"}, map[string]*config.RoutingProfile{"b": profileB}},
		{"two removed one added drops both", []string{"a", "b"}, []string{"n"}, map[string]*config.RoutingProfile{}},
		{"one removed two added drops", []string{"a", "b"}, []string{"b", "n1", "n2"}, map[string]*config.RoutingProfile{"b": profileB}},
		{"duplicates count once", []string{"a", "a", "b"}, []string{"n", "n", "b"}, map[string]*config.RoutingProfile{"n": profileA, "b": profileB}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profiles := map[string]*config.RoutingProfile{"a": profileA, "b": profileB}
			reconcileKeyProfiles(tc.before, tc.after, profiles)
			if !reflect.DeepEqual(profiles, tc.want) {
				t.Fatalf("profiles = %v, want %v", profiles, tc.want)
			}
		})
	}
	reconcileKeyProfiles([]string{"a"}, nil, nil)
}
