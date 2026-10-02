package management

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const codexProbeFixture = `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":120,"reset_at":1800000000},"secondary_window":{"used_percent":"90.5","limit_window_seconds":"604800","reset_after_seconds":"3600"}}}`

func TestQuotaProbeHeaders(t *testing.T) {
	for _, test := range []struct {
		name, provider, body string
		want                 map[string]string
	}{
		{name: "codex", provider: "codex", body: codexProbeFixture, want: map[string]string{
			"X-Codex-Allowed": "false", "X-Codex-Limit-Reached": "true",
			"X-Codex-Primary-Used-Percent": "25", "X-Codex-Primary-Window-Minutes": "300",
			"X-Codex-Primary-Reset-At": "1800000000", "X-Codex-Primary-Reset-After-Seconds": "120",
			"X-Codex-Secondary-Used-Percent": "90.5", "X-Codex-Secondary-Window-Minutes": "10080",
			"X-Codex-Secondary-Reset-After-Seconds": "3600",
		}},
		{name: "claude", provider: "claude", body: `{"five_hour":{"utilization":0,"resets_at":"2027-01-01T00:00:00Z"},"seven_day":{"utilization":75.5,"resets_at":"2027-01-02T00:00:00.000Z"}}`, want: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": "0", "Anthropic-Ratelimit-Unified-5h-Reset": "1798761600",
			"Anthropic-Ratelimit-Unified-7d-Utilization": "0.755", "Anthropic-Ratelimit-Unified-7d-Reset": "1798848000",
		}},
		{name: "broken JSON", provider: "codex", body: codexProbeFixture[:len(codexProbeFixture)-1]},
		{name: "rejected without windows", provider: "codex", body: `{"rate_limit":{"allowed":false,"limit_reached":true}}`, want: map[string]string{
			"X-Codex-Allowed": "false", "X-Codex-Limit-Reached": "true",
		}},
		{name: "unrelated JSON", provider: "codex", body: `{"status":"ok"}`},
		{name: "missing utilization", provider: "claude", body: `{"five_hour":{"resets_at":"2027-01-01T00:00:00Z"}}`},
		{name: "null reset", provider: "claude", body: `{"five_hour":{"utilization":0,"resets_at":null}}`},
		{name: "invalid reset", provider: "claude", body: `{"five_hour":{"utilization":10,"resets_at":"yesterday"}}`},
		{name: "invalid utilization", provider: "claude", body: `{"five_hour":{"utilization":101,"resets_at":"2027-01-01T00:00:00Z"}}`},
		{name: "negative utilization", provider: "codex", body: `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":18000,"reset_at":1800000000}}}`},
		{name: "invalid window", provider: "codex", body: `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":0,"reset_at":1800000000}}}`},
		{name: "missing reset", provider: "codex", body: `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}`},
		{name: "unsupported provider", provider: "gemini", body: codexProbeFixture},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := quotaProbeHeaders(test.provider, []byte(test.body))
			if len(got) != len(test.want) {
				t.Fatalf("headers = %v, want %v", got, test.want)
			}
			for key, want := range test.want {
				if got.Get(key) != want {
					t.Errorf("%s = %q, want %q", key, got.Get(key), want)
				}
			}
		})
	}
}

func TestAPICallQuotaRequiresKnownSuccessfulProbe(t *testing.T) {
	for _, test := range []struct {
		name, provider, method, target, finalTarget, host string
		body                                              string
		extraHeader                                       map[string]string
		status                                            int
		want                                              bool
	}{
		{name: "codex", provider: "codex", want: true},
		{name: "claude", provider: "claude", target: "https://api.anthropic.com/api/oauth/usage", want: true,
			body: `{"five_hour":{"utilization":10,"resets_at":"2027-01-01T00:00:00Z"}}`},
		{name: "other token", provider: "codex", extraHeader: map[string]string{"Authorization": "Bearer other"}},
		{name: "other account", provider: "codex", extraHeader: map[string]string{"Chatgpt-Account-Id": "other"}},
		{name: "missing token", provider: "codex", extraHeader: map[string]string{"Authorization": ""}},
		{name: "missing account", provider: "codex", extraHeader: map[string]string{"Chatgpt-Account-Id": ""}},
		{name: "cookie", provider: "codex", extraHeader: map[string]string{"Cookie": "other=credential"}},
		{name: "api key", provider: "codex", extraHeader: map[string]string{"X-Api-Key": "other"}},
		{name: "wrong provider", provider: "claude"},
		{name: "post", provider: "codex", method: http.MethodPost},
		{name: "http", provider: "codex", target: "http://chatgpt.com/backend-api/wham/usage"},
		{name: "other host", provider: "codex", target: "https://example.com/backend-api/wham/usage"},
		{name: "port", provider: "codex", target: "https://chatgpt.com:9443/backend-api/wham/usage"},
		{name: "query", provider: "codex", target: "https://chatgpt.com/backend-api/wham/usage?account=other"},
		{name: "userinfo", provider: "codex", target: "https://user@chatgpt.com/backend-api/wham/usage"},
		{name: "host override", provider: "codex", host: "example.com"},
		{name: "redirect", provider: "codex", finalTarget: "https://example.com/backend-api/wham/usage"},
		{name: "non-probe redirects to probe", provider: "codex", target: "https://example.com/", finalTarget: "https://chatgpt.com/backend-api/wham/usage"},
		{name: "failed", provider: "codex", status: http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "probe", Provider: test.provider,
				Metadata: map[string]any{"access_token": "test-token", "account_id": "test-account"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			method := test.method
			if method == "" {
				method = http.MethodGet
			}
			target := test.target
			if target == "" {
				target = "https://chatgpt.com/backend-api/wham/usage"
			}
			request, errRequest := http.NewRequest(method, target, nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			request.Header.Set("Authorization", "Bearer test-token")
			request.Header.Set("Chatgpt-Account-Id", "test-account")
			for key, value := range test.extraHeader {
				request.Header.Set(key, value)
			}
			if test.host != "" {
				request.Host = test.host
			}
			final := request
			if test.finalTarget != "" {
				final, errRequest = http.NewRequest(method, test.finalTarget, nil)
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				final.Header = request.Header.Clone()
			}
			status := test.status
			if status == 0 {
				status = http.StatusOK
			}
			handler := &Handler{authManager: manager}
			body := test.body
			if body == "" {
				body = codexProbeFixture
			}
			handler.observeAPICallQuota(auth, request, &http.Response{StatusCode: status, Request: final}, []byte(body), time.Now())
			got, _ := manager.GetByID(auth.ID)
			if observed := len(got.Quota.Signals) > 0; observed != test.want {
				t.Fatalf("observed = %v, want %v", observed, test.want)
			}
		})
	}
}

func TestAPICallV8ObservesQuotaWithoutChangingLegacyAPICall(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Redirect") != "" && r.URL.Path == "/backend-api/wham/usage" {
			http.Redirect(w, r, "https://chatgpt.com/other", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(codexProbeFixture))
	}))
	t.Cleanup(upstream.Close)
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = upstream.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previousTransport
		transport.CloseIdleConnections()
	})
	for i, test := range []struct {
		name                     string
		v8, host, redirect, want bool
	}{
		{name: "v8", v8: true, want: true},
		{name: "legacy"},
		{name: "explicit host", v8: true, host: true},
		{name: "redirect", v8: true, redirect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "probe-" + strconv.Itoa(i), Provider: "codex",
				Metadata: map[string]any{"access_token": "test-token", "account_id": "test-account"},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			handler := &Handler{authManager: manager}
			router := gin.New()
			if test.v8 {
				router.POST("/", handler.APICallV8)
			} else {
				router.POST("/", handler.APICall)
			}
			input := apiCallRequest{AuthIndexSnake: &auth.Index, Method: http.MethodGet, URL: "https://chatgpt.com/backend-api/wham/usage", Header: map[string]string{
				"Authorization": "Bearer $TOKEN$", "Chatgpt-Account-Id": "test-account",
			}}
			if test.host {
				input.Header["Host"] = "chatgpt.com"
			}
			if test.redirect {
				input.Header["X-Test-Redirect"] = "true"
			}
			body, errMarshal := json.Marshal(input)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			got, _ := manager.GetByID(auth.ID)
			if observed := len(got.Quota.Signals) > 0; observed != test.want {
				t.Fatalf("observed = %v, want %v", observed, test.want)
			}
			if !test.want && !reflect.DeepEqual(got, auth) {
				t.Fatal("non-probe API call changed credential state")
			}
		})
	}
}
