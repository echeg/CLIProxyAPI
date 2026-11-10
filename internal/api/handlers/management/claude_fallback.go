package management

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// GetClaudeFallback advertises support separately from the persisted YAML tree.
// Older backends must not accept panel edits that they would silently discard.
func (h *Handler) GetClaudeFallback(c *gin.Context) {
	h.mu.Lock()
	manager := h.authManager
	settings := config.ClaudeFallbackConfig{Mode: "auto", Mappings: []config.ClaudeFallbackMapping{}}
	if h.cfg != nil {
		// The legacy view is derived from providers.claude.fallback of the shared profile.
		settings = h.cfg.Routing.SharedProfile().EffectiveProviders().Claude.Fallback.LegacyClaudeFallback()
		settings.Mappings = append([]config.ClaudeFallbackMapping{}, settings.Mappings...)
		if settings.Mode == "" {
			settings.Mode = "auto"
		}
	}
	h.mu.Unlock()
	models := map[string][]string{"claude": {}, "codex": {}}
	if manager != nil {
		seen := map[string]map[string]bool{"claude": {}, "codex": {}}
		for _, auth := range manager.List() {
			if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled || seen[auth.Provider] == nil {
				continue
			}
			if auth.Provider == "codex" && auth.AuthKind() != coreauth.AuthKindOAuth {
				continue
			}
			for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
				if model != nil && model.ID != "" && !seen[auth.Provider][model.ID] {
					seen[auth.Provider][model.ID] = true
					models[auth.Provider] = append(models[auth.Provider], model.ID)
				}
			}
		}
	}
	for provider := range models {
		sort.Strings(models[provider])
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"supported": manager.ClaudeFallbackSupported(), "settings": settings, "models": models})
}
