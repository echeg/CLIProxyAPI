package openai

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func websocketUpstreamSupportsIncrementalInput(attributes map[string]string, metadata map[string]any) bool {
	if len(attributes) > 0 {
		if raw := strings.TrimSpace(attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(value))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsIncrementalInputForModel(ctx context.Context, modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(ctx, modelName)
	for _, auth := range auths {
		if responsesWebsocketAuthSupportsIncrementalInput(auth) {
			return true
		}
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsCompactionReplayForModel(ctx context.Context, modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(ctx, modelName)
	if len(auths) == 0 {
		return false
	}
	for _, auth := range auths {
		if !responsesWebsocketAuthSupportsCompactionReplay(auth) {
			return false
		}
	}
	return true
}

// responsesWebsocketAvailableAuthsForModel lists the available auths that can
// serve modelName for the connection in ctx. Auths of a provider the key's
// routing profile disables are left out: the turn can never run on them, so they
// must not shape transport or replay choices.
func (h *OpenAIResponsesAPIHandler) responsesWebsocketAvailableAuthsForModel(ctx context.Context, modelName string) ([]*coreauth.Auth, string) {
	if h == nil || h.AuthManager == nil {
		return nil, ""
	}
	resolvedModelName := h.responsesWebsocketResolvedModel(ctx, modelName)
	providerSet, modelKey := responsesWebsocketProviderSetForModel(resolvedModelName)
	if len(providerSet) == 0 {
		return nil, modelKey
	}

	registryRef := registry.GetGlobalRegistry()
	now := time.Now()
	providerEnabled := h.AuthManager.ProviderEnabledForRequest(ctx)
	auths := h.AuthManager.List()
	available := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if !responsesWebsocketAuthMatchesModel(auth, providerSet, modelKey, registryRef, now) {
			continue
		}
		if !providerEnabled(auth.Provider) {
			continue
		}
		available = append(available, auth)
	}
	return available, modelKey
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketHasAlternativeReplayTarget(ctx context.Context, modelName string, rawJSON []byte, failedAuthID string) bool {
	if h == nil || h.BaseAPIHandler == nil || h.AuthManager == nil {
		return false
	}
	return h.HasAlternativeExecutionTarget(ctx, h.HandlerType(), modelName, rawJSON, failedAuthID)
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketUsesUpstreamWebsocketPassthrough(ctx context.Context, modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if h == nil || h.AuthManager == nil || modelName == "" {
		return false
	}
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(ctx, modelName)
	if len(auths) == 0 {
		return false
	}
	provider := ""
	for _, auth := range auths {
		if auth == nil {
			return false
		}
		authProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if authProvider != "codex" && authProvider != "xai" {
			return false
		}
		if provider == "" {
			provider = authProvider
			if _, ok := h.AuthManager.Executor(provider); !ok {
				return false
			}
		} else if authProvider != provider {
			return false
		}
		if !websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata) {
			return false
		}
	}
	return provider != ""
}

func responsesWebsocketAuthSupportsIncrementalInput(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata)
}

// responsesWebsocketPinnedAuthMatchesModel reports whether a pinned auth can serve
// resolvedModelName, the turn's model after responsesWebsocketResolvedModel.
func responsesWebsocketPinnedAuthMatchesModel(auth *coreauth.Auth, resolvedModelName string, pinnedModelKey string, homeRuntime bool) bool {
	if auth == nil {
		return false
	}
	providerSet, modelKey := responsesWebsocketProviderSetForModel(resolvedModelName)
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if !homeRuntime || len(providerSet) > 0 {
		if _, ok := providerSet[providerKey]; !ok {
			return false
		}
	}
	if !responsesWebsocketAuthAvailableForModel(auth, modelKey, time.Now()) {
		return false
	}

	if homeRuntime {
		return strings.EqualFold(strings.TrimSpace(pinnedModelKey), strings.TrimSpace(modelKey))
	}
	return registry.GetGlobalRegistry().ClientSupportsModel(auth.ID, modelKey)
}

// responsesWebsocketResolvedModel resolves "auto" with the routing profile of the
// connection's key in ctx, as execution does (BaseAPIHandler.AutoModelProviderFilter),
// so pin matching and transport choices follow the model the turn runs on.
// Any other model name is returned unchanged.
func (h *OpenAIResponsesAPIHandler) responsesWebsocketResolvedModel(ctx context.Context, modelName string) string {
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName != "auto" {
		return modelName
	}
	var acceptProvider func(provider string) bool
	if h != nil {
		acceptProvider = h.AutoModelProviderFilter(ctx)
	}
	resolvedBase := util.ResolveAutoModelMatching(initialSuffix.ModelName, acceptProvider)
	if initialSuffix.HasSuffix {
		return fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
	}
	return resolvedBase
}

func responsesWebsocketProviderSetForModel(resolvedModelName string) (map[string]struct{}, string) {
	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)
	providers := util.GetProviderName(baseModel)
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		providerSet[providerKey] = struct{}{}
	}
	modelKey := baseModel
	if modelKey == "" {
		modelKey = strings.TrimSpace(resolvedModelName)
	}
	return providerSet, modelKey
}

func responsesWebsocketAuthMatchesModel(auth *coreauth.Auth, providerSet map[string]struct{}, modelKey string, registryRef *registry.ModelRegistry, now time.Time) bool {
	if auth == nil {
		return false
	}
	providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
	if _, ok := providerSet[providerKey]; !ok {
		return false
	}
	if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(auth.ID, modelKey) {
		return false
	}
	return responsesWebsocketAuthAvailableForModel(auth, modelKey, now)
}

func responsesWebsocketAuthSupportsCompactionReplay(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
}

func responsesWebsocketAuthAvailableForModel(auth *coreauth.Auth, modelName string, now time.Time) bool {
	return coreauth.IsAuthAvailableForModel(auth, modelName, now)
}
