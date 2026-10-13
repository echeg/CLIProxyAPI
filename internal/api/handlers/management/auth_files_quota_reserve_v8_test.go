package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newQuotaReserveV8Fixture(t *testing.T) (*Handler, *coreauth.Manager, string) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "reserved.json")
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID: "reserved.json", FileName: "reserved.json", Provider: "claude",
		Attributes: map[string]string{"path": filePath},
		Metadata: map[string]any{
			"type": "claude", "custom": map[string]any{"keep": "original"},
			"quota_reserve": map[string]any{"percent": 30, "mode": "soft", "steps": []int{15, 5}},
		},
	}
	coreauth.ApplyAuthQuotaReserveMetadata(record, record.Metadata)
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatal(errRegister)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager), manager, filePath
}

func patchQuotaReserveFieldsV8(t *testing.T, h *Handler, fields string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(`{"name":"reserved.json",`+fields+`}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFieldsV8(ctx)
	return rec
}

func listQuotaReserveFiles(t *testing.T, h *Handler, v8 bool, query string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/credentials"+query, nil)
	if v8 {
		h.ListAuthFilesV8(ctx)
	} else {
		h.ListAuthFiles(ctx)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	return response
}

func TestPatchAuthFileFieldsV8StagedQuotaReserveRoundTrip(t *testing.T) {
	h, manager, filePath := newQuotaReserveV8Fixture(t)
	for _, step := range []struct {
		name, fields, wantPolicy, wantSteps string
	}{
		{"replace stages", `"quota_reserve":{"percent":40,"mode":" SOFT ","steps":[20,10]}`, `{"percent":40,"mode":"soft","steps":[20,10]}`, "[20,10]"},
		{"legacy no-op preserves stages", `"quota_reserve":{"percent":40}`, `{"percent":40,"mode":"soft","steps":[20,10]}`, "[20,10]"},
		{"collapse explicitly", `"quota_reserve":{"percent":40,"mode":"soft","steps":[]}`, `{"percent":40,"mode":"soft"}`, ""},
		{"legacy edit single level", `"quota_reserve":{"percent":35,"mode":"hard"}`, `{"percent":35,"mode":"hard"}`, ""},
		{"restore stages", `"quota_reserve":{"percent":30,"mode":"soft","steps":[15,5]}`, `{"percent":30,"mode":"soft","steps":[15,5]}`, "[15,5]"},
		{"explicit hard transition", `"quota_reserve":{"percent":30,"mode":"hard","steps":[]}`, `{"percent":30,"mode":"hard"}`, ""},
		{"delete", `"quota_reserve":null`, `null`, ""},
	} {
		t.Run(step.name, func(t *testing.T) {
			rec := patchQuotaReserveFieldsV8(t, h, step.fields)
			if rec.Code != http.StatusOK {
				t.Fatalf("patch status = %d: %s", rec.Code, rec.Body.String())
			}
			current := mustGetAuth(t, manager, "reserved.json")
			if got := current.Attributes[coreauth.AttributeQuotaReserveSteps]; got != step.wantSteps {
				t.Fatalf("steps attribute = %q, want %q", got, step.wantSteps)
			}
			var want any
			if errDecode := json.Unmarshal([]byte(step.wantPolicy), &want); errDecode != nil {
				t.Fatal(errDecode)
			}
			persisted := readPersistedAuthFile(t, filePath)
			if !reflect.DeepEqual(persisted["quota_reserve"], want) {
				t.Fatalf("persisted policy = %#v, want %#v", persisted["quota_reserve"], want)
			}
			response := listQuotaReserveFiles(t, h, true, "")
			if response["quota_reserve_max_levels"] != float64(3) {
				t.Fatalf("capability = %#v", response["quota_reserve_max_levels"])
			}
			file := response["files"].([]any)[0].(map[string]any)
			if !reflect.DeepEqual(file["quota_reserve"], want) {
				t.Fatalf("listed policy = %#v, want %#v", file["quota_reserve"], want)
			}
			if file["quota_reserve_level"] != float64(0) || file["quota_reserve_active"] != false {
				t.Fatalf("unobserved reserve unexpectedly active: %#v", file)
			}
		})
	}
}

func TestPatchAuthFileFieldsV8RejectsInvalidOrStaleStagesWithoutMutation(t *testing.T) {
	h, manager, filePath := newQuotaReserveV8Fixture(t)
	baseline, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	before := mustGetAuth(t, manager, "reserved.json")
	beforeMetadata, _ := json.Marshal(before.Metadata)
	for _, tc := range []struct {
		name, field, value, wantError string
		status                        int
	}{
		{"missing steps with new percent", "quota_reserve", `{"percent":40,"mode":"soft"}`, "include steps explicitly", 409},
		{"missing steps with new mode", "quota_reserve", `{"percent":30,"mode":"hard"}`, "include steps explicitly", 409},
		{"null steps", "quota_reserve", `{"percent":30,"steps":null}`, "steps must be an array", 400},
		{"object steps", "quota_reserve", `{"percent":30,"steps":{}}`, "steps must be an array", 400},
		{"string steps", "quota_reserve", `{"percent":30,"steps":"15,5"}`, "steps must be an array", 400},
		{"too many", "quota_reserve", `{"percent":30,"steps":[20,10,5]}`, "at most two", 400},
		{"equal to first", "quota_reserve", `{"percent":30,"steps":[30]}`, "strictly decrease", 400},
		{"above first", "quota_reserve", `{"percent":30,"steps":[40]}`, "strictly decrease", 400},
		{"ascending", "quota_reserve", `{"percent":30,"steps":[5,15]}`, "strictly decrease", 400},
		{"duplicate", "quota_reserve", `{"percent":30,"steps":[15,15]}`, "strictly decrease", 400},
		{"zero", "quota_reserve", `{"percent":30,"steps":[0]}`, "integer between 1 and 99", 400},
		{"negative", "quota_reserve", `{"percent":30,"steps":[-1]}`, "integer between 1 and 99", 400},
		{"fractional", "quota_reserve", `{"percent":30,"steps":[15.5]}`, "integer between 1 and 99", 400},
		{"string threshold", "quota_reserve", `{"percent":30,"steps":["15"]}`, "integer between 1 and 99", 400},
		{"null threshold", "quota_reserve", `{"percent":30,"steps":[null]}`, "integer between 1 and 99", 400},
		{"hard stages", "quota_reserve", `{"percent":30,"mode":"hard","steps":[15]}`, "only supported in soft mode", 400},
		{"nested steps", "quota_reserve.steps", `[15]`, "does not support nested fields", 400},
		{"nested percent", "quota_reserve.percent", `20`, "does not support nested fields", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := patchQuotaReserveFieldsV8(t, h, `"custom.keep":"changed","`+tc.field+`":`+tc.value)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.wantError) {
				t.Fatalf("status = %d, body = %s; want %d containing %q", rec.Code, rec.Body.String(), tc.status, tc.wantError)
			}
			current := mustGetAuth(t, manager, "reserved.json")
			currentMetadata, _ := json.Marshal(current.Metadata)
			if !bytes.Equal(currentMetadata, beforeMetadata) || !reflect.DeepEqual(current.Attributes, before.Attributes) {
				t.Fatalf("rejected patch mutated live credential: metadata=%s attributes=%#v", currentMetadata, current.Attributes)
			}
			after, errRead := os.ReadFile(filePath)
			if errRead != nil || !bytes.Equal(after, baseline) {
				t.Fatalf("rejected patch mutated persisted credential; read error=%v", errRead)
			}
		})
	}
}

func TestListAuthFilesV8QuotaReserveLevels(t *testing.T) {
	h, manager, _ := newQuotaReserveV8Fixture(t)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	reset5h, reset7d := now.Add(2*time.Hour), now.Add(48*time.Hour)
	h.nowFunc = func() time.Time { return now }
	for _, tc := range []struct {
		name, used5h, used7d string
		mode                 string
		steps                []int
		level                int
	}{
		{"at first threshold", "0.7", "0.1", "soft", []int{15, 5}, 0},
		{"first level", "0.8", "0.8", "soft", []int{15, 5}, 1},
		{"at second threshold", "0.85", "0.8", "soft", []int{15, 5}, 1},
		{"second level", "0.9", "0.8", "soft", []int{15, 5}, 2},
		{"at third threshold", "0.95", "0.8", "soft", []int{15, 5}, 2},
		{"third level", "0.98", "0.8", "soft", []int{15, 5}, 3},
		{"hard level", "0.98", "0.8", "hard", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := mustGetAuth(t, manager, "reserved.json")
			policy := map[string]any{"percent": 30, "mode": tc.mode}
			if len(tc.steps) > 0 {
				policy["steps"] = tc.steps
			}
			auth.Metadata["quota_reserve"] = policy
			coreauth.ApplyAuthQuotaReserveMetadata(auth, auth.Metadata)
			auth.Quota = coreauth.QuotaState{ObservedAt: now.Add(-time.Minute), Signals: map[string]string{
				"anthropic-ratelimit-unified-5h-utilization": tc.used5h,
				"anthropic-ratelimit-unified-5h-reset":       reset5h.Format(time.RFC3339),
				"anthropic-ratelimit-unified-7d-utilization": tc.used7d,
				"anthropic-ratelimit-unified-7d-reset":       reset7d.Format(time.RFC3339),
			}}
			if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			response := listQuotaReserveFiles(t, h, true, "?page=1&page_size=1")
			file := response["files"].([]any)[0].(map[string]any)
			if file["quota_reserve_level"] != float64(tc.level) || file["quota_reserve_active"] != (tc.level > 0) {
				t.Fatalf("wrong reserve verdict: %#v", file)
			}
			if tc.level == 0 {
				if _, exists := file["quota_reserve_until"]; exists {
					t.Fatalf("inactive reserve has until: %#v", file)
				}
			} else if file["quota_reserve_until"] != reset7d.Format(time.RFC3339) {
				t.Fatalf("until = %v, want latest reset %s", file["quota_reserve_until"], reset7d)
			}
			if response["quota_reserve_max_levels"] != float64(3) || response["total"] != float64(1) {
				t.Fatalf("missing paginated capability/total: %#v", response)
			}
		})
	}
	h.nowFunc = func() time.Time { return reset7d.Add(time.Second) }
	file := listQuotaReserveFiles(t, h, true, "")["files"].([]any)[0].(map[string]any)
	if file["quota_reserve_level"] != float64(0) || file["quota_reserve_active"] != false {
		t.Fatalf("expired reserve still active: %#v", file)
	}
}

func TestListAuthFilesV8DiskCapabilityAndLegacyIsolation(t *testing.T) {
	h, _, _ := newQuotaReserveV8Fixture(t)
	for _, disk := range []bool{false, true} {
		if disk {
			h.authManager = nil
		}
		for _, query := range []string{"", "?page=1&page_size=1", "?name=missing.json", "?auth_index=missing", "?page=2&page_size=1"} {
			for _, v8 := range []bool{false, true} {
				response := listQuotaReserveFiles(t, h, v8, query)
				_, hasCapability := response["quota_reserve_max_levels"]
				if hasCapability != v8 {
					t.Fatalf("disk=%v v8=%v query=%q: capability presence = %v", disk, v8, query, hasCapability)
				}
				for _, raw := range response["files"].([]any) {
					file := raw.(map[string]any)
					policy := file["quota_reserve"].(map[string]any)
					_, hasSteps := policy["steps"]
					_, hasLevel := file["quota_reserve_level"]
					if hasSteps != v8 || hasLevel != v8 {
						t.Fatalf("disk=%v v8=%v: unexpected fields %#v", disk, v8, file)
					}
					if v8 && !reflect.DeepEqual(policy["steps"], []any{float64(15), float64(5)}) {
						t.Fatalf("disk=%v: wrong steps %#v", disk, policy["steps"])
					}
				}
			}
		}
	}
}

func TestPatchAuthFileFieldsLegacyStillIgnoresStages(t *testing.T) {
	h, manager, _ := newQuotaReserveV8Fixture(t)
	rec := patchQuotaReserveFields(t, h, `{"name":"reserved.json","quota_reserve":{"percent":40,"mode":"hard","steps":"ignored"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy patch status = %d: %s", rec.Code, rec.Body.String())
	}
	policy, ok := coreauth.QuotaReservePolicyForAuth(mustGetAuth(t, manager, "reserved.json"))
	if !ok || policy.Percent != 40 || policy.Mode != "hard" || len(policy.Steps) != 0 {
		t.Fatalf("legacy policy = %#v, ok=%v", policy, ok)
	}
}

func TestPatchAuthFileFieldsV8ReleasesStatusLockBeforePersistHook(t *testing.T) {
	h, _, _ := newQuotaReserveV8Fixture(t)
	hookStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHook := func() { releaseOnce.Do(func() { close(release) }) }
	var calls atomic.Int32
	h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error {
		if calls.Add(1) == 1 {
			close(hookStarted)
			<-release
		}
		return nil
	})
	first := make(chan *httptest.ResponseRecorder, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		first <- patchQuotaReserveFieldsV8(t, h, `"quota_reserve":{"percent":40,"steps":[20,10]}`)
	}()
	t.Cleanup(func() {
		releaseHook()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
		}
	})
	select {
	case <-hookStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("field patch persist hook did not start")
	}
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/status", strings.NewReader(`{"name":"reserved.json","disabled":true}`))
		h.PatchAuthFileStatus(ctx)
		second <- rec
	}()
	select {
	case rec := <-second:
		if rec.Code != http.StatusOK {
			t.Fatalf("status patch = %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("status patch blocked on field patch persist hook")
	}
	releaseHook()
	select {
	case rec := <-first:
		if rec.Code != http.StatusOK {
			t.Fatalf("field patch = %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("field patch did not finish after releasing persist hook")
	}
}
