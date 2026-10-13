package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// ListAuthFilesV8 includes the full staged reserve policy and its current level.
func (h *Handler) ListAuthFilesV8(c *gin.Context) {
	h.listAuthFiles(c, true)
}

// PatchAuthFileFieldsV8 validates a complete reserve policy before changing metadata.
func (h *Handler) PatchAuthFileFieldsV8(c *gin.Context) {
	h.patchAuthFileFields(c, true)
}

func authFilesListResponseForVersion(response gin.H, stagedReserve bool) gin.H {
	if stagedReserve {
		response["quota_reserve_max_levels"] = 3
	}
	return response
}

func (h *Handler) buildAuthFileEntryForVersion(auth *coreauth.Auth, stagedReserve bool, quotaSupported map[string]struct{}) gin.H {
	entry := h.buildAuthFileEntry(auth, quotaSupported)
	if entry == nil || !stagedReserve {
		return entry
	}
	delete(entry, "quota_reserve")
	delete(entry, "quota_reserve_until")
	if policy, ok := coreauth.QuotaReservePolicyForAuth(auth); ok {
		entry["quota_reserve"] = policy
	}
	level, until := coreauth.QuotaReserveLevel(auth, h.now())
	entry["quota_reserve_level"] = level
	entry["quota_reserve_active"] = level > 0
	if level > 0 {
		entry["quota_reserve_until"] = until.UTC().Format(time.RFC3339)
	}
	return entry
}

func authFileQuotaReservePolicyFromJSON(data []byte, provider string) (coreauth.QuotaReservePolicy, bool) {
	if !coreauth.QuotaReserveSupportedProvider(provider) {
		return coreauth.QuotaReservePolicy{}, false
	}
	raw := gjson.GetBytes(data, authFileQuotaReserveField)
	if !raw.Exists() {
		return coreauth.QuotaReservePolicy{}, false
	}
	value, errDecode := decodeAuthFileFieldValue(json.RawMessage(raw.Raw))
	if errDecode != nil {
		return coreauth.QuotaReservePolicy{}, false
	}
	policy, errParse := coreauth.ParseQuotaReservePolicy(value)
	return policy, errParse == nil
}

// validateAuthFileQuotaReservePatchV8 runs before other fields can mutate nested metadata.
// An older editor must explicitly acknowledge removing stages instead of silently losing them.
func validateAuthFileQuotaReservePatchV8(auth *coreauth.Auth, fields map[string]json.RawMessage) (map[string]any, int, error) {
	for fieldPath := range fields {
		if rootAuthFileField(fieldPath) == authFileQuotaReserveField && fieldPath != authFileQuotaReserveField {
			return nil, http.StatusBadRequest, fmt.Errorf("quota_reserve does not support nested fields")
		}
	}
	raw, exists := fields[authFileQuotaReserveField]
	if !exists {
		return nil, 0, nil
	}
	value, errDecode := decodeAuthFileFieldValue(raw)
	if errDecode != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid field quota_reserve")
	}
	if value == nil {
		return nil, 0, nil
	}
	if !coreauth.QuotaReserveSupportedProvider(auth.Provider) {
		return nil, http.StatusBadRequest, fmt.Errorf("quota reserve is supported for codex and claude")
	}
	policy, errParse := coreauth.ParseQuotaReservePolicy(value)
	if errParse != nil {
		return nil, http.StatusBadRequest, errParse
	}
	current, hasCurrent := coreauth.QuotaReservePolicyForAuth(auth)
	if !hasCurrent {
		var errCurrent error
		current, errCurrent = coreauth.ParseQuotaReservePolicy(auth.Metadata[authFileQuotaReserveField])
		hasCurrent = errCurrent == nil
	}
	if _, hasSteps := value.(map[string]any)["steps"]; !hasSteps && hasCurrent && len(current.Steps) > 0 {
		if policy.Percent != current.Percent || policy.Mode != current.Mode {
			return nil, http.StatusConflict, fmt.Errorf("quota_reserve has multiple levels; include steps explicitly (use [] to remove additional levels)")
		}
		policy.Steps = current.Steps
	}
	canonical := map[string]any{"percent": policy.Percent, "mode": policy.Mode}
	if len(policy.Steps) > 0 {
		canonical["steps"] = policy.Steps
	}
	return canonical, 0, nil
}
