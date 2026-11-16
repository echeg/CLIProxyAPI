package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const policyPinReleaseLogMessage = "responses websocket: releasing pinned auth rejected by routing policy"

// websocketPolicyPinExecutor serves every credential of one provider and
// records each turn per credential, so a test can attribute every turn to
// the subscription or the API key. failStatus makes a credential fail;
// compactionAuthID answers with a compaction item, executed as a local
// websocket turn when compactionLocal is set.
type websocketPolicyPinExecutor struct {
	provider         string
	mu               sync.Mutex
	failStatus       map[string]int
	compactionAuthID string
	compactionLocal  bool
	payloads         map[string][][]byte
	authIDs          []string
}

func (e *websocketPolicyPinExecutor) Identifier() string { return e.provider }

func (e *websocketPolicyPinExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPolicyPinExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	if e.payloads == nil {
		e.payloads = make(map[string][][]byte)
	}
	e.authIDs = append(e.authIDs, authID)
	e.payloads[authID] = append(e.payloads[authID], bytes.Clone(req.Payload))
	count := len(e.authIDs)
	failStatus := e.failStatus[authID]
	compaction := authID != "" && authID == e.compactionAuthID
	compactionLocal := compaction && e.compactionLocal
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	if failStatus > 0 {
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{
			status: failStatus,
			msg:    fmt.Sprintf(`{"error":{"message":"upstream failed with %d","type":"server_error","code":"upstream_failed"}}`, failStatus),
		}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	outputType := "message"
	if compaction {
		outputType = "compaction"
	}
	if compactionLocal {
		coreexecutor.MarkLocalWebsocketTurn(ctx)
	}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(fmt.Sprintf(
		`{"type":"response.completed","response":{"id":"resp-%s-%d","output":[{"type":%q,"id":"out-%d"}]}}`,
		authID, count, outputType, count,
	))}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketPolicyPinExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketPolicyPinExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketPolicyPinExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketPolicyPinExecutor) setFailStatus(authID string, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failStatus == nil {
		e.failStatus = make(map[string]int)
	}
	e.failStatus[authID] = status
}

// turns returns how many turns authID served.
func (e *websocketPolicyPinExecutor) turns(authID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.payloads[authID])
}

func (e *websocketPolicyPinExecutor) payloadsFor(authID string) [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]byte(nil), e.payloads[authID]...)
}

func (e *websocketPolicyPinExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
}

// policyPinFixture is one Codex subscription and one Codex API key, both on
// upstream websockets, behind a routing profile whose Codex fallback is the
// API key.
type policyPinFixture struct {
	t        *testing.T
	cfg      *config.Config
	manager  *coreauth.Manager
	executor *websocketPolicyPinExecutor
	model    string
	subID    string
	apiKeyID string
	wsURL    string
}

func newPolicyPinFixture(t *testing.T, subscriptionCooling bool) *policyPinFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	f := &policyPinFixture{
		t:        t,
		cfg:      &config.Config{},
		executor: &websocketPolicyPinExecutor{provider: "codex"},
		model:    "policy-pin-model-" + name,
		subID:    "policy-pin-sub-" + name,
		apiKeyID: "policy-pin-api-" + name,
	}
	f.cfg.Routing.Providers.Codex = config.ProviderRoutingConfig{
		APICredential: f.apiKeyID,
		Fallback:      config.ProviderFallbackConfig{Target: config.ProviderFallbackAPI},
	}
	f.manager = coreauth.NewManager(nil, nil, nil)
	f.manager.SetConfig(f.cfg)
	f.manager.RegisterExecutor(f.executor)

	subscription := &coreauth.Auth{
		ID:         f.subID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
		Metadata:   map[string]any{"access_token": "test-token"},
	}
	if subscriptionCooling {
		subscription.ModelStates = map[string]*coreauth.ModelState{
			f.model: {Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)},
		}
	}
	apiKey := &coreauth.Auth{
		ID:         f.apiKeyID,
		Index:      f.apiKeyID,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true", "api_key": "test-key"},
	}
	for _, auth := range []*coreauth.Auth{subscription, apiKey} {
		if _, err := f.manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: f.model}})
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(f.subID)
		registry.GetGlobalRegistry().UnregisterClient(f.apiKeyID)
	})

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&f.cfg.SDKConfig, f.manager))
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	f.wsURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	return f
}

// setSubscriptionCooldown moves the subscription's model cooldown to recoverAt
// without waiting: a past time means the subscription has recovered.
func (f *policyPinFixture) setSubscriptionCooldown(recoverAt time.Time) {
	f.t.Helper()
	f.setCooldown(f.subID, recoverAt)
}

// setCooldown puts authID's model into a cooldown that ends at recoverAt.
func (f *policyPinFixture) setCooldown(authID string, recoverAt time.Time) {
	f.t.Helper()
	auth, ok := f.manager.GetByID(authID)
	if !ok {
		f.t.Fatalf("auth %s not found", authID)
	}
	if auth.ModelStates == nil {
		auth.ModelStates = map[string]*coreauth.ModelState{}
	}
	auth.ModelStates[f.model] = &coreauth.ModelState{Status: coreauth.StatusError, Unavailable: true, NextRetryAfter: recoverAt}
	if _, err := f.manager.Update(context.Background(), auth); err != nil {
		f.t.Fatalf("update %s cooldown: %v", authID, err)
	}
}

func (f *policyPinFixture) dial() *websocket.Conn {
	f.t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(f.wsURL, nil)
	if err != nil {
		f.t.Fatalf("dial websocket: %v", err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	if errDeadline := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); errDeadline != nil {
		f.t.Fatalf("set read deadline: %v", errDeadline)
	}
	return conn
}

func (f *policyPinFixture) fullCreate(msgID string) string {
	return fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":%q}]}`, f.model, msgID)
}

func policyPinIncremental(previousResponseID, msgID string) string {
	return fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","id":%q}]}`, previousResponseID, msgID)
}

// sendCompleted writes request and returns the completed response ID.
func policyPinSendCompleted(t *testing.T, conn *websocket.Conn, request string) string {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil || ojson.GetBytesNoCopy(payload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("response = %s, err=%v, want %s", payload, err, wsEventTypeCompleted)
	}
	return ojson.GetBytesNoCopy(payload, "response.id").String()
}

// policyPinExpectReplayClose writes request and expects the HTTP-replay close.
func policyPinExpectReplayClose(t *testing.T, conn *websocket.Conn, request string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseServiceRestart || closeErr.Text != wsHTTPReplayRequiredCloseReason {
		t.Fatalf("response = %s, err=%v, want replay close %d %q", payload, err, websocket.CloseServiceRestart, wsHTTPReplayRequiredCloseReason)
	}
}

// policyPinLogHook records info-level entries of the standard logger and
// restores its previous hooks and level when the test ends.
func policyPinLogHook(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	savedHooks := make(log.LevelHooks)
	for level, hooks := range logger.Hooks {
		savedHooks[level] = append([]log.Hook(nil), hooks...)
	}
	savedLevel := logger.GetLevel()
	logger.SetLevel(log.InfoLevel)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		logger.ReplaceHooks(savedHooks)
		logger.SetLevel(savedLevel)
	})
	return hook
}

func policyPinReleaseLogged(hook *logtest.Hook) bool {
	for _, entry := range hook.AllEntries() {
		if entry.Message == policyPinReleaseLogMessage {
			return true
		}
	}
	return false
}

func TestResponsesWebsocketReleasesFallbackPinOnceSubscriptionRecovers(t *testing.T) {
	t.Run("incremental turn gets the replay close", func(t *testing.T) {
		f := newPolicyPinFixture(t, true)
		hook := policyPinLogHook(t)
		conn := f.dial()

		responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns after turn 1 = %d, want 1 (fallback); auth IDs %v", got, f.executor.AuthIDs())
		}

		f.setSubscriptionCooldown(time.Now().Add(-time.Minute))
		policyPinExpectReplayClose(t, conn, policyPinIncremental(responseID, "msg-2"))
		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns after released turn 2 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
		if !policyPinReleaseLogged(hook) {
			t.Fatalf("missing log %q", policyPinReleaseLogMessage)
		}
	})

	t.Run("cooling subscription keeps the API key incremental", func(t *testing.T) {
		f := newPolicyPinFixture(t, true)
		hook := policyPinLogHook(t)
		conn := f.dial()

		responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		policyPinSendCompleted(t, conn, policyPinIncremental(responseID, "msg-2"))
		if got := f.executor.turns(f.apiKeyID); got != 2 {
			t.Fatalf("API key turns = %d, want 2; auth IDs %v", got, f.executor.AuthIDs())
		}
		if got := f.executor.turns(f.subID); got != 0 {
			t.Fatalf("subscription turns = %d, want 0", got)
		}
		secondPayload := f.executor.payloadsFor(f.apiKeyID)[1]
		if ojson.GetBytesNoCopy(secondPayload, "previous_response_id").String() != responseID {
			t.Fatalf("turn 2 was not forwarded incrementally: %s", secondPayload)
		}
		if policyPinReleaseLogged(hook) {
			t.Fatalf("unexpected log %q while the subscription cools down", policyPinReleaseLogMessage)
		}
	})

	t.Run("full create returns to the subscription on the same socket", func(t *testing.T) {
		f := newPolicyPinFixture(t, true)
		conn := f.dial()

		policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		f.setSubscriptionCooldown(time.Now().Add(-time.Minute))
		policyPinSendCompleted(t, conn, f.fullCreate("msg-2"))

		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
		if got := f.executor.turns(f.subID); got != 1 {
			t.Fatalf("subscription turns = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
	})
}

func TestResponsesWebsocketReleasesRestoredFallbackPinAfterProviderSwitch(t *testing.T) {
	f := newPolicyPinFixture(t, true)
	other := &websocketPolicyPinExecutor{provider: "policy-pin-other"}
	f.manager.RegisterExecutor(other)
	otherModel := "policy-pin-other-model"
	otherAuth := &coreauth.Auth{ID: "policy-pin-other-auth", Provider: other.provider, Status: coreauth.StatusActive}
	if _, err := f.manager.Register(context.Background(), otherAuth); err != nil {
		t.Fatalf("register other auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(otherAuth.ID, other.provider, []*registry.ModelInfo{{ID: otherModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(otherAuth.ID) })
	conn := f.dial()

	policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
	if got := f.executor.turns(f.apiKeyID); got != 1 {
		t.Fatalf("API key turns after turn 1 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
	}
	policyPinSendCompleted(t, conn, fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-2"}]}`, otherModel))
	if got := other.turns(otherAuth.ID); got != 1 {
		t.Fatalf("other provider turns = %d, want 1", got)
	}

	f.setSubscriptionCooldown(time.Now().Add(-time.Minute))
	policyPinSendCompleted(t, conn, f.fullCreate("msg-3"))
	if got := f.executor.turns(f.apiKeyID); got != 1 {
		t.Fatalf("API key turns after turn 3 = %d, want 1 (restored pin released); auth IDs %v", got, f.executor.AuthIDs())
	}
	if got := f.executor.turns(f.subID); got != 1 {
		t.Fatalf("subscription turns after turn 3 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
	}
}

// disableWebsockets drops the upstream websocket attribute of authID, so its
// turns run in HTTP upstream mode and leave no pin behind.
func (f *policyPinFixture) disableWebsockets(authID string) {
	f.t.Helper()
	auth, ok := f.manager.GetByID(authID)
	if !ok {
		f.t.Fatalf("auth %s not found", authID)
	}
	attributes := make(map[string]string, len(auth.Attributes))
	for key, value := range auth.Attributes {
		if key != "websockets" {
			attributes[key] = value
		}
	}
	auth.Attributes = attributes
	if _, err := f.manager.Update(context.Background(), auth); err != nil {
		f.t.Fatalf("update %s attributes: %v", authID, err)
	}
}

// A compaction served by the API fallback key makes that key the compaction
// replay target of later turns. That target pins the turn like a remembered
// pin, so it follows the routing policy too.
func TestResponsesWebsocketCompactionReplayTargetFollowsRoutingPolicy(t *testing.T) {
	t.Run("local compaction pin returns to the recovered subscription", func(t *testing.T) {
		f := newPolicyPinFixture(t, true)
		f.executor.compactionAuthID = f.apiKeyID
		f.executor.compactionLocal = true
		hook := policyPinLogHook(t)
		conn := f.dial()

		policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns after turn 1 = %d, want 1 (fallback); auth IDs %v", got, f.executor.AuthIDs())
		}

		f.setSubscriptionCooldown(time.Now().Add(-time.Minute))
		policyPinSendCompleted(t, conn, f.fullCreate("msg-2"))
		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns after turn 2 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
		if got := f.executor.turns(f.subID); got != 1 {
			t.Fatalf("subscription turns after turn 2 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
		if !policyPinReleaseLogged(hook) {
			t.Fatalf("missing log %q", policyPinReleaseLogMessage)
		}
	})

	t.Run("HTTP-mode key compaction rebuilds the incremental turn on the subscription", func(t *testing.T) {
		f := newPolicyPinFixture(t, true)
		f.disableWebsockets(f.apiKeyID)
		f.executor.compactionAuthID = f.apiKeyID
		conn := f.dial()

		responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns after turn 1 = %d, want 1 (fallback); auth IDs %v", got, f.executor.AuthIDs())
		}

		f.setSubscriptionCooldown(time.Now().Add(-time.Minute))
		policyPinSendCompleted(t, conn, policyPinIncremental(responseID, "msg-2"))
		if got := f.executor.turns(f.apiKeyID); got != 1 {
			t.Fatalf("API key turns after turn 2 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
		if got := f.executor.turns(f.subID); got != 1 {
			t.Fatalf("subscription turns after turn 2 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
	})

	t.Run("cooling subscription keeps the compaction replay on the key", func(t *testing.T) {
		f := newPolicyPinFixture(t, true)
		f.disableWebsockets(f.apiKeyID)
		f.executor.compactionAuthID = f.apiKeyID
		hook := policyPinLogHook(t)
		conn := f.dial()

		responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		policyPinSendCompleted(t, conn, policyPinIncremental(responseID, "msg-2"))
		if got := f.executor.turns(f.apiKeyID); got != 2 {
			t.Fatalf("API key turns = %d, want 2; auth IDs %v", got, f.executor.AuthIDs())
		}
		if got := f.executor.turns(f.subID); got != 0 {
			t.Fatalf("subscription turns = %d, want 0", got)
		}
		if policyPinReleaseLogged(hook) {
			t.Fatalf("unexpected log %q while the subscription cools down", policyPinReleaseLogMessage)
		}
	})
}

func TestResponsesWebsocketReleasesPinAfterHotReloadModeSwitch(t *testing.T) {
	t.Run("switch to api mode replays", func(t *testing.T) {
		f := newPolicyPinFixture(t, false)
		hook := policyPinLogHook(t)
		conn := f.dial()

		responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		if got := f.executor.turns(f.subID); got != 1 {
			t.Fatalf("subscription turns after turn 1 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}

		reloaded := *f.cfg
		reloaded.Routing.Providers.Codex = config.ProviderRoutingConfig{Mode: config.ProviderModeAPI, APICredential: f.apiKeyID}
		f.manager.SetConfig(&reloaded)

		policyPinExpectReplayClose(t, conn, policyPinIncremental(responseID, "msg-2"))
		if got := f.executor.turns(f.subID); got != 1 {
			t.Fatalf("subscription turns after released turn 2 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
		}
		if got := f.executor.turns(f.apiKeyID); got != 0 {
			t.Fatalf("API key turns = %d, want 0", got)
		}
		if !policyPinReleaseLogged(hook) {
			t.Fatalf("missing log %q", policyPinReleaseLogMessage)
		}
	})

	t.Run("reload without mode change keeps the pin", func(t *testing.T) {
		f := newPolicyPinFixture(t, false)
		hook := policyPinLogHook(t)
		conn := f.dial()

		responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-1"))
		reloaded := *f.cfg
		f.manager.SetConfig(&reloaded)
		policyPinSendCompleted(t, conn, policyPinIncremental(responseID, "msg-2"))

		if got := f.executor.turns(f.subID); got != 2 {
			t.Fatalf("subscription turns = %d, want 2; auth IDs %v", got, f.executor.AuthIDs())
		}
		if policyPinReleaseLogged(hook) {
			t.Fatalf("unexpected log %q without a mode change", policyPinReleaseLogMessage)
		}
	})
}

// setCodexRouting applies a new Codex routing policy through a config reload.
func (f *policyPinFixture) setCodexRouting(routing config.ProviderRoutingConfig) {
	f.t.Helper()
	reloaded := *f.cfg
	reloaded.Routing.Providers.Codex = routing
	f.manager.SetConfig(&reloaded)
}

// policyPinExpectError writes request and expects an error event with status.
func policyPinExpectError(t *testing.T, conn *websocket.Conn, request string, status int) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil || ojson.GetBytesNoCopy(payload, "type").String() != wsEventTypeError {
		t.Fatalf("response = %s, err=%v, want %s", payload, err, wsEventTypeError)
	}
	if got := ojson.GetBytesNoCopy(payload, "status").Int(); got != int64(status) {
		t.Fatalf("error status = %d, want %d: %s", got, status, payload)
	}
}

// pinSubscription serves turn 1 on the subscription, which pins it over its
// upstream websocket, and returns the completed response ID.
func (f *policyPinFixture) pinSubscription(conn *websocket.Conn) string {
	f.t.Helper()
	responseID := policyPinSendCompleted(f.t, conn, f.fullCreate("msg-1"))
	if got := f.executor.turns(f.subID); got != 1 {
		f.t.Fatalf("subscription turns after turn 1 = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
	}
	return responseID
}

func TestResponsesWebsocketPinnedSubscriptionFailureReachesAPIFallback(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized} {
		t.Run(fmt.Sprintf("full create %d fails over in place to the API key", status), func(t *testing.T) {
			f := newPolicyPinFixture(t, false)
			conn := f.dial()
			f.pinSubscription(conn)

			f.executor.setFailStatus(f.subID, status)
			responseID := policyPinSendCompleted(t, conn, f.fullCreate("msg-2"))

			if got := f.executor.turns(f.apiKeyID); got != 1 {
				t.Fatalf("API key turns = %d, want 1; auth IDs %v", got, f.executor.AuthIDs())
			}
			if !strings.Contains(responseID, f.apiKeyID) {
				t.Fatalf("turn 2 response %q was not served by the API key", responseID)
			}
		})
	}

	t.Run("incremental 429 gets the replay close", func(t *testing.T) {
		f := newPolicyPinFixture(t, false)
		conn := f.dial()
		responseID := f.pinSubscription(conn)

		f.executor.setFailStatus(f.subID, http.StatusTooManyRequests)
		policyPinExpectReplayClose(t, conn, policyPinIncremental(responseID, "msg-2"))

		if got := f.executor.turns(f.apiKeyID); got != 0 {
			t.Fatalf("API key turns = %d, want 0; auth IDs %v", got, f.executor.AuthIDs())
		}
	})
}

func TestResponsesWebsocketPinnedSubscriptionFailureWithoutAPIFallback(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *policyPinFixture)
	}{
		{
			name: "API key cooling down",
			setup: func(f *policyPinFixture) {
				f.setCooldown(f.apiKeyID, time.Now().Add(time.Hour))
			},
		},
		{
			name: "fallback off",
			setup: func(f *policyPinFixture) {
				f.setCodexRouting(config.ProviderRoutingConfig{
					APICredential: f.apiKeyID,
					Fallback:      config.ProviderFallbackConfig{Target: config.ProviderFallbackOff},
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/full create exposes the error", func(t *testing.T) {
			f := newPolicyPinFixture(t, false)
			conn := f.dial()
			f.pinSubscription(conn)

			tc.setup(f)
			f.executor.setFailStatus(f.subID, http.StatusTooManyRequests)
			policyPinExpectError(t, conn, f.fullCreate("msg-2"), http.StatusTooManyRequests)

			if got := f.executor.turns(f.apiKeyID); got != 0 {
				t.Fatalf("API key turns = %d, want 0; auth IDs %v", got, f.executor.AuthIDs())
			}
		})
		t.Run(tc.name+"/incremental exposes the error", func(t *testing.T) {
			f := newPolicyPinFixture(t, false)
			conn := f.dial()
			responseID := f.pinSubscription(conn)

			tc.setup(f)
			f.executor.setFailStatus(f.subID, http.StatusTooManyRequests)
			policyPinExpectError(t, conn, policyPinIncremental(responseID, "msg-2"), http.StatusTooManyRequests)

			if got := f.executor.turns(f.apiKeyID); got != 0 {
				t.Fatalf("API key turns = %d, want 0; auth IDs %v", got, f.executor.AuthIDs())
			}
		})
	}

	t.Run("incremental 401 still gets the replay close", func(t *testing.T) {
		f := newPolicyPinFixture(t, false)
		conn := f.dial()
		responseID := f.pinSubscription(conn)

		f.setCodexRouting(config.ProviderRoutingConfig{
			APICredential: f.apiKeyID,
			Fallback:      config.ProviderFallbackConfig{Target: config.ProviderFallbackOff},
		})
		f.executor.setFailStatus(f.subID, http.StatusUnauthorized)
		policyPinExpectReplayClose(t, conn, policyPinIncremental(responseID, "msg-2"))

		if got := f.executor.turns(f.apiKeyID); got != 0 {
			t.Fatalf("API key turns = %d, want 0; auth IDs %v", got, f.executor.AuthIDs())
		}
	})
}
