package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type routingActivityTestExecutor struct{ coreauth.ProviderExecutor }

func (routingActivityTestExecutor) Identifier() string { return "routing-activity-test" }

func (routingActivityTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func TestGetRoutingActivityOnlyExposesSelectionFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := coreauth.WithSkipPersist(context.Background())
	manager := coreauth.NewManager(nil, nil, nil)
	executor := routingActivityTestExecutor{}
	manager.RegisterExecutor(executor)
	auth, errRegister := manager.Register(ctx, &coreauth.Auth{
		ID: "activity-api-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "do-not-return", "email": "private@example.test"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "activity-api-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	_, errExecute := manager.Execute(ctx, []string{auth.Provider}, cliproxyexecutor.Request{Model: "activity-api-model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	handler := &Handler{authManager: manager}
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/activity", nil)
	handler.GetRoutingActivity(c)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if errDecode := json.Unmarshal(response.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(payload.Accounts) != 1 || len(payload.Accounts[0]) != 3 {
		t.Fatalf("unexpected activity fields: %s", response.Body.String())
	}
	account := payload.Accounts[0]
	if account["auth_index"] != auth.Index || account["provider"] != auth.Provider {
		t.Fatalf("unexpected activity identity: %+v", account)
	}
	selectedAt, ok := account["last_selected_at"].(string)
	if !ok {
		t.Fatal("missing selection timestamp")
	}
	if _, errParse := time.Parse(time.RFC3339Nano, selectedAt); errParse != nil {
		t.Fatal(errParse)
	}
}

func TestGetRoutingActivityEmptyAndUnavailable(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		handler *Handler
		status  int
	}{
		{"empty", &Handler{authManager: coreauth.NewManager(nil, nil, nil)}, http.StatusOK},
		{"unavailable", &Handler{}, http.StatusServiceUnavailable},
		{"nil handler", nil, http.StatusServiceUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			testCase.handler.GetRoutingActivity(c)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d", response.Code, testCase.status)
			}
			if testCase.status == http.StatusOK && response.Body.String() != `{"accounts":[]}` {
				t.Fatalf("empty history must be an array: %s", response.Body.String())
			}
		})
	}
}
