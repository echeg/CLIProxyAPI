package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"

	. "github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// PluginModelRouterHost routes matching requests to a plugin executor, the router's own executor,
// or a built-in provider before model-to-provider resolution and auth selection.
type PluginModelRouterHost interface {
	RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
}

type pluginModelRouterSkipHost interface {
	RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
}

type modelRouterDetector interface {
	HasModelRouters() bool
}

type modelRouterSkipDetector interface {
	HasModelRoutersExcept(string) bool
}

func preferExecutionProvider(providers []string, preferred string) []string {
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if preferred == "" || len(providers) < 2 {
		return providers
	}
	preferredIndex := -1
	for i := range providers {
		if strings.ToLower(strings.TrimSpace(providers[i])) == preferred {
			preferredIndex = i
			break
		}
	}
	if preferredIndex <= 0 {
		return providers
	}
	out := make([]string, 0, len(providers))
	out = append(out, providers[preferredIndex])
	out = append(out, providers[:preferredIndex]...)
	out = append(out, providers[preferredIndex+1:]...)
	return out
}

func adjustExecutionProvidersForEntryProtocol(entryProtocol string, providers []string) []string {
	if entryProtocol == Interactions {
		return preferExecutionProvider(providers, GeminiInteractions)
	}
	if supportsNativeInteractionsEntryProtocol(entryProtocol) {
		return providers
	}
	return excludeExecutionProvider(providers, GeminiInteractions)
}

func supportsNativeInteractionsEntryProtocol(entryProtocol string) bool {
	switch entryProtocol {
	case Interactions, OpenAI, OpenaiResponse, Claude, Gemini:
		return true
	default:
		return false
	}
}

func excludeExecutionProvider(providers []string, excluded string) []string {
	excluded = strings.ToLower(strings.TrimSpace(excluded))
	if excluded == "" || len(providers) == 0 {
		return providers
	}
	excludedIndex := -1
	for i := range providers {
		if strings.ToLower(strings.TrimSpace(providers[i])) == excluded {
			excludedIndex = i
			break
		}
	}
	if excludedIndex == -1 {
		return providers
	}
	out := make([]string, 0, len(providers)-1)
	out = append(out, providers[:excludedIndex]...)
	out = append(out, providers[excludedIndex+1:]...)
	return out
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	return h.getRequestDetailsWithOptions(context.Background(), modelName, false, modelExecutionOptions{Probe: true})
}

func validateNativeInteractionsExecution(entryProtocol string, execOptions modelExecutionOptions, routeDecision modelRouteDecision) *interfaces.ErrorMessage {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider == "" || entryProtocol != Interactions {
		return nil
	}
	if routeDecision.ExecutorPluginID != "" {
		return nativeInteractionsExecutionError()
	}
	if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
		return nativeInteractionsExecutionError()
	}
	return nil
}

func nativeInteractionsExecutionError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf("agent is only supported for native interactions execution"),
	}
}

// providersForExecution resolves the providers and normalized model for a request. When a model
// router selected a built-in provider, it skips model->provider resolution and uses the router's
// provider (with an optional target model); otherwise it falls back to the registry-based path.
func (h *BaseAPIHandler) providersForExecution(ctx context.Context, modelName, originalRequestedModel string, allowImageModel bool, routeDecision modelRouteDecision, execOptions modelExecutionOptions) ([]string, string, *interfaces.ErrorMessage) {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider != "" {
		if routeDecision.ExecutorPluginID != "" {
			return nil, "", nativeInteractionsExecutionError()
		}
		if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
			return nil, "", nativeInteractionsExecutionError()
		}
		normalizedModel := strings.TrimSpace(modelName)
		if normalizedModel == "" {
			normalizedModel = strings.TrimSpace(originalRequestedModel)
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		if errMsg := h.validateSpeechOnlyModel(normalizedModel, execOptions.AllowSpeechModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{forcedProvider}, normalizedModel, nil
	}
	if routeDecision.Provider != "" {
		normalizedModel := originalRequestedModel
		if routeDecision.Model != "" {
			normalizedModel = routeDecision.Model
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		if errMsg := h.validateSpeechOnlyModel(normalizedModel, execOptions.AllowSpeechModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{routeDecision.Provider}, normalizedModel, nil
	}
	return h.getRequestDetailsWithOptions(ctx, modelName, allowImageModel, execOptions)
}

// AutoModelProviderFilter returns the providers "auto" may resolve to for the
// request in ctx: those its routing profile leaves enabled. It returns nil, which
// allows every provider and keeps the unfiltered resolution, when the profile
// disables no provider and without an auth manager. ResolveAutoModelForRequest
// resolves "auto" with it.
func (h *BaseAPIHandler) AutoModelProviderFilter(ctx context.Context) func(provider string) bool {
	if h == nil || h.AuthManager == nil {
		return nil
	}
	ctx = h.AuthManager.WithRequestRoutingProfile(ctx)
	if !h.AuthManager.DisablesProviderForRequest(ctx) {
		return nil
	}
	return h.AuthManager.ProviderEnabledForRequest(ctx)
}

// withRequestRoutingProfile returns ctx carrying the routing profile of its
// request (coreauth.Manager.WithRequestRoutingProfile). Every execution entry
// point calls it before routing, so resolving "auto", the refusal of an "auto"
// routed only to disabled providers, and execution with its retries and
// fallback legs read one profile even when a config reload lands in between.
func (h *BaseAPIHandler) withRequestRoutingProfile(ctx context.Context) context.Context {
	if h == nil || h.AuthManager == nil {
		return ctx
	}
	return h.AuthManager.WithRequestRoutingProfile(ctx)
}

// ResolveAutoModelForRequest resolves "auto", with or without a thinking suffix,
// for the request in ctx: the newest model that, with that suffix appended, has
// an available client of a provider AutoModelProviderFilter allows among the
// clients execution would run it on (see util.ResolveAutoModelMatching). Every
// resolution of "auto" for one request (execution, Responses websocket pin
// matching) must use it. Any other model name is returned unchanged.
func (h *BaseAPIHandler) ResolveAutoModelForRequest(ctx context.Context, modelName string) string {
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName != "auto" {
		return modelName
	}
	withSuffix := func(model string) string {
		if !initialSuffix.HasSuffix {
			return model
		}
		return fmt.Sprintf("%s(%s)", model, initialSuffix.RawSuffix)
	}
	acceptProvider := h.AutoModelProviderFilter(ctx)
	var routeModel registry.ModelRouter
	if acceptProvider != nil {
		// Judge a listed model by the request execution makes: the client's
		// suffix is appended first, routing picks the registration (routedModel:
		// the base model's, so one provider may register "x(high)" while another
		// registers "x", or the full name's when no provider registers the base
		// model), and credential selection runs the request on the clients of
		// that registration's providers that register the base model, compared
		// case-insensitively, or the model their OAuth alias or prefix maps it
		// to (authSupportsRouteModel), each available when it is not cooling
		// down for the model selection checks (the alias's upstream model, not
		// the base model). So a model registered only under its suffixed name
		// is skipped, while one that a client serves through "X" or a forked
		// alias is not. Only enabled providers' clients count, so a model
		// routed only to disabled providers is skipped before the auths are
		// scanned for selection keys.
		routeModel = func(modelID string) (registry.ModelRoute, bool) {
			model := withSuffix(modelID)
			routedID, providers := routedModel(model)
			enabledProviders := slices.DeleteFunc(providers, func(provider string) bool { return !acceptProvider(provider) })
			if len(enabledProviders) == 0 {
				return registry.ModelRoute{}, false
			}
			return registry.ModelRoute{
				Model:         model,
				ID:            routedID,
				BaseModel:     strings.TrimSpace(thinking.ParseSuffix(model).ModelName),
				SelectionKeys: h.AuthManager.RouteModelSelectionKeys(ctx, enabledProviders, model),
			}, true
		}
	}
	return withSuffix(util.ResolveAutoModelMatching(initialSuffix.ModelName, acceptProvider, routeModel))
}

// routedModel returns the registered model a request for model is routed by and
// the providers it is routed to: its base model, or, when no provider registers
// the base model, the full name, each as util.GetRegisteredModelProviders
// matches it (possibly lowercased). The fallback supports custom models
// registered with their full suffixed name (e.g., "my-model(8192)"); it was
// evaluated in Story 11.8 and is intentionally preserved.
func routedModel(model string) (string, []string) {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	registeredID, providers := util.GetRegisteredModelProviders(baseModel)
	if len(providers) == 0 && baseModel != model {
		return util.GetRegisteredModelProviders(model)
	}
	return registeredID, providers
}

// getRequestDetailsWithOptions resolves the providers and normalized model of a
// request for modelName. execOptions supplies AllowSpeechModel and Probe.
func (h *BaseAPIHandler) getRequestDetailsWithOptions(ctx context.Context, modelName string, allowImageModel bool, execOptions modelExecutionOptions) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	if h == nil || h.AuthManager == nil || !h.AuthManager.HomeEnabled() {
		requestedAuto := thinking.ParseSuffix(modelName).ModelName == "auto"
		if requestedAuto {
			// "auto" and its refusal below read one snapshot of the profile; the
			// execution entry points pinned it already (withRequestRoutingProfile).
			ctx = h.withRequestRoutingProfile(ctx)
		}
		resolvedModelName = h.ResolveAutoModelForRequest(ctx, modelName)
		// "auto" resolves to a model only disabled providers serve when no enabled
		// provider registers one. Refuse it here with the profile "auto" was
		// resolved with: an endpoint check below must not answer first with an
		// error about a model the client never named. A probe gets the refusal
		// without the log line the request itself writes.
		if requestedAuto && h != nil && h.AuthManager != nil {
			_, routedProviders := routedModel(resolvedModelName)
			refuse := h.AuthManager.RefuseDisabledProviders
			if execOptions.Probe {
				refuse = h.AuthManager.DisabledProvidersError
			}
			if errDisabled := refuse(ctx, routedProviders); errDisabled != nil {
				return nil, "", &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errDisabled}
			}
		}
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	if errMsg := h.validateImageOnlyModel(baseModel, allowImageModel); errMsg != nil {
		return nil, "", errMsg
	}
	if errMsg := h.validateSpeechOnlyModel(baseModel, execOptions.AllowSpeechModel); errMsg != nil {
		return nil, "", errMsg
	}

	if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return []string{"home"}, resolvedModelName, nil
	}

	_, providers = routedModel(resolvedModelName)

	if len(providers) == 0 {
		// The client asked for a model this proxy cannot route. Report it as a request
		// error so streaming clients receive an actionable message instead of a
		// gateway failure they would keep retrying. 400 is used rather than 404 to keep
		// it distinguishable from an unregistered HTTP route.
		// The model name is client supplied, so it is inserted through sjson rather
		// than formatted into the JSON literal: an unescaped quote would otherwise
		// corrupt the body or let the caller overwrite the error code.
		body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		body, errSet := ojson.Set(body, "error.message", "unknown provider for model "+modelName)
		if errSet != nil {
			body = `{"error":{"message":"unknown provider for model","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		}
		return nil, "", &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(body),
		}
	}

	// The thinking suffix is preserved in the model name itself, so no
	// metadata-based configuration passing is needed.
	return providers, resolvedModelName, nil
}

func (h *BaseAPIHandler) validateImageOnlyModel(modelName string, allowImageModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isOpenAIImageOnlyModel(baseModel) && !allowImageModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("model %s is only supported on /v1/images/generations and /v1/images/edits", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isOpenAIImageOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2.5", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst", "gpt-image-2.6", "grok-imagine-image", "grok-imagine-image-quality", "grok-imagine-image-2.0":
		return true
	default:
		return false
	}
}

// validateSpeechOnlyModel rejects speech-only models outside the speech endpoints so they are
// never sent to a chat upstream.
func (h *BaseAPIHandler) validateSpeechOnlyModel(modelName string, allowSpeechModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isXAISpeechOnlyModel(baseModel) && !allowSpeechModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("model %s is only supported on /v1/audio/speech and /v1/tts", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isXAISpeechOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "grok-tts", "grok-voice-tts-1.0":
		return true
	default:
		return false
	}
}

func routeModelBaseName(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func (h *BaseAPIHandler) modelRouterHost() PluginModelRouterHost {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	routerHost := h.ModelRouterHost
	pluginHost := h.PluginHost
	h.runtimeMu.RUnlock()
	if !isNilPluginModelRouterHost(routerHost) {
		return routerHost
	}
	if isNilPluginInterceptorHost(pluginHost) {
		return nil
	}
	router, ok := pluginHost.(PluginModelRouterHost)
	if !ok {
		return nil
	}
	return router
}

type modelRouteDecision struct {
	ExecutorPluginID string
	Provider         string
	Model            string
}

func routeModel(ctx context.Context, host PluginModelRouterHost, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if host == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if skipper, ok := host.(pluginModelRouterSkipHost); ok {
			return skipper.RouteModelExcept(ctx, req, skipPluginID)
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	return host.RouteModel(ctx, req)
}

func modelRoutersEnabled(host PluginModelRouterHost, skipPluginID string) bool {
	if host == nil {
		return false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if _, ok := host.(pluginModelRouterSkipHost); !ok {
			return false
		}
		if detector, ok := host.(modelRouterSkipDetector); ok {
			return detector.HasModelRoutersExcept(skipPluginID)
		}
	}
	if detector, ok := host.(modelRouterDetector); ok {
		return detector.HasModelRouters()
	}
	// No detector: treat routing as disabled (same conservative default as before any
	// ModelRouter existed). Hosts that route must implement HasModelRouters (pluginhost.Host does).
	return false
}

func (h *BaseAPIHandler) applyModelRouter(ctx context.Context, handlerType, modelName string, rawJSON []byte, stream bool, execOptions modelExecutionOptions) modelRouteDecision {
	var decision modelRouteDecision
	host := h.modelRouterHost()
	if host == nil || !modelRoutersEnabled(host, execOptions.SkipRouterPluginID) {
		return decision
	}
	meta := requestExecutionMetadata(ctx)
	addCallerScopeMetadata(meta, execOptions.CallerScope)
	meta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(meta, execOptions.InternalSource)
	resp, ok := routeModel(ctx, host, pluginapi.ModelRouteRequest{
		SourceFormat:   handlerType,
		RequestedModel: modelName,
		Stream:         stream,
		Headers:        modelExecutionHeaders(ctx, execOptions.Headers),
		Query:          modelExecutionQuery(ctx, execOptions.Query),
		Body:           cloneBytes(rawJSON),
		Metadata:       meta,
	}, execOptions.SkipRouterPluginID)
	if !ok || !resp.Handled {
		return decision
	}
	switch resp.TargetKind {
	case pluginapi.ModelRouteTargetSelf, pluginapi.ModelRouteTargetExecutor:
		decision.ExecutorPluginID = strings.TrimSpace(resp.Target)
	case pluginapi.ModelRouteTargetProvider:
		decision.Provider = strings.ToLower(strings.TrimSpace(resp.Target))
		decision.Model = strings.TrimSpace(resp.TargetModel)
	}
	return decision
}
