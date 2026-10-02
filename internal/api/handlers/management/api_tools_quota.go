package management

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// APICallV8 additionally records recognized usage probes for quota-aware routing.
func (h *Handler) APICallV8(c *gin.Context) {
	h.apiCall(c, true)
}

func (h *Handler) observeAPICallQuota(auth *coreauth.Auth, request *http.Request, response *http.Response, body []byte, startedAt time.Time) {
	if h == nil || h.authManager == nil || auth == nil || response == nil ||
		response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	// Validate both ends of a redirect chain; generic management API calls to
	// other endpoints must never supply credential quota observations.
	if !isQuotaProbeRequest(provider, request) || !isQuotaProbeRequest(provider, response.Request) ||
		!quotaProbeUsesCredential(auth, request) || !quotaProbeUsesCredential(auth, response.Request) {
		return
	}
	headers := quotaProbeHeaders(provider, body)
	if len(headers) != 0 {
		h.authManager.ObserveQuotaProbe(auth, headers, startedAt)
	}
}

func quotaProbeUsesCredential(auth *coreauth.Auth, request *http.Request) bool {
	token := tokenValueForAuth(auth)
	if token == "" || len(request.Header.Values("Authorization")) != 1 ||
		request.Header.Get("Authorization") != "Bearer "+token ||
		len(request.Header.Values("Cookie")) != 0 || len(request.Header.Values("X-Api-Key")) != 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		accountID := stringValue(auth.Metadata, "account_id")
		if accountID == "" {
			accountID, _ = extractCodexIDTokenClaims(auth)["chatgpt_account_id"].(string)
		}
		return len(request.Header.Values("Chatgpt-Account-Id")) <= 1 &&
			strings.TrimSpace(request.Header.Get("Chatgpt-Account-Id")) == accountID
	}
	return true
}

func isQuotaProbeRequest(provider string, request *http.Request) bool {
	if request == nil || request.Method != http.MethodGet || request.URL == nil {
		return false
	}
	target := request.URL
	if target.Scheme != "https" || target.User != nil || target.RawQuery != "" ||
		target.Fragment != "" || target.RawPath != "" || target.Opaque != "" {
		return false
	}
	var host, path string
	switch provider {
	case "codex":
		host, path = "chatgpt.com", "/backend-api/wham/usage"
	case "claude":
		host, path = "api.anthropic.com", "/api/oauth/usage"
	default:
		return false
	}
	return target.Host == host && target.Path == path &&
		(request.Host == "" || request.Host == host)
}

func quotaProbeHeaders(provider string, body []byte) http.Header {
	headers := make(http.Header)
	switch provider {
	case "codex":
		var payload struct {
			RateLimit struct {
				Allowed   *bool             `json:"allowed"`
				Reached   *bool             `json:"limit_reached"`
				Primary   *codexProbeWindow `json:"primary_window"`
				Secondary *codexProbeWindow `json:"secondary_window"`
			} `json:"rate_limit"`
		}
		if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
			return nil
		}
		addCodexProbeWindow(headers, "X-Codex-Primary-", payload.RateLimit.Primary)
		addCodexProbeWindow(headers, "X-Codex-Secondary-", payload.RateLimit.Secondary)
		if payload.RateLimit.Allowed != nil {
			headers.Set("X-Codex-Allowed", strconv.FormatBool(*payload.RateLimit.Allowed))
		}
		if payload.RateLimit.Reached != nil {
			headers.Set("X-Codex-Limit-Reached", strconv.FormatBool(*payload.RateLimit.Reached))
		}
	case "claude":
		var payload struct {
			FiveHour *claudeProbeWindow `json:"five_hour"`
			SevenDay *claudeProbeWindow `json:"seven_day"`
		}
		if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
			return nil
		}
		addClaudeProbeWindow(headers, "Anthropic-Ratelimit-Unified-5h-", payload.FiveHour)
		addClaudeProbeWindow(headers, "Anthropic-Ratelimit-Unified-7d-", payload.SevenDay)
	}
	return headers
}

type codexProbeWindow struct {
	UsedPercent       *json.Number `json:"used_percent"`
	LimitWindowSecond *json.Number `json:"limit_window_seconds"`
	ResetAfterSeconds *json.Number `json:"reset_after_seconds"`
	ResetAt           *json.Number `json:"reset_at"`
}

func addCodexProbeWindow(headers http.Header, prefix string, window *codexProbeWindow) {
	if window == nil || window.UsedPercent == nil || window.LimitWindowSecond == nil {
		return
	}
	used, errUsed := window.UsedPercent.Float64()
	seconds, errSeconds := window.LimitWindowSecond.Int64()
	if errUsed != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 ||
		errSeconds != nil || seconds <= 0 || seconds%60 != 0 {
		return
	}
	hasReset := false
	if window.ResetAt != nil {
		if resetAt, errReset := window.ResetAt.Int64(); errReset == nil && resetAt > 0 {
			headers.Set(prefix+"Reset-At", strconv.FormatInt(resetAt, 10))
			hasReset = true
		}
	}
	if window.ResetAfterSeconds != nil {
		if resetAfter, errReset := window.ResetAfterSeconds.Int64(); errReset == nil && resetAfter >= 0 {
			headers.Set(prefix+"Reset-After-Seconds", strconv.FormatInt(resetAfter, 10))
			hasReset = true
		}
	}
	if !hasReset {
		return
	}
	headers.Set(prefix+"Used-Percent", strconv.FormatFloat(used, 'f', -1, 64))
	headers.Set(prefix+"Window-Minutes", strconv.FormatInt(seconds/60, 10))
}

type claudeProbeWindow struct {
	Utilization *json.Number `json:"utilization"`
	ResetsAt    *string      `json:"resets_at"`
}

func addClaudeProbeWindow(headers http.Header, prefix string, window *claudeProbeWindow) {
	if window == nil || window.Utilization == nil || window.ResetsAt == nil {
		return
	}
	used, errUsed := window.Utilization.Float64()
	resetAt, errReset := time.Parse(time.RFC3339Nano, *window.ResetsAt)
	if errUsed != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 ||
		errReset != nil || resetAt.Unix() <= 0 {
		return
	}
	headers.Set(prefix+"Utilization", strconv.FormatFloat(used/100, 'f', -1, 64))
	headers.Set(prefix+"Reset", strconv.FormatInt(resetAt.Unix(), 10))
}
