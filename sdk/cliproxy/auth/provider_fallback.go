package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// ClaudeFallbackSupported excludes routing owned by Home or a scheduler plugin.
func (m *Manager) ClaudeFallbackSupported() bool {
	return m != nil && !m.HomeEnabled() && !m.hasPluginScheduler()
}

// providerFallback is the fallback resolved for one request: the provider whose
// failed primary round triggers it, the target (codex or api), the mode (auto or
// always; always only for target codex), and the model of the fallback leg.
type providerFallback struct {
	provider string
	target   string
	mode     string
	model    string
}

// providerFallbackFor resolves the fallback of the request's routing profile for
// the first listed provider that has one, claude before codex. A pinned request
// and a fallback leg never fall back, so fallbacks do not chain.
func (m *Manager) providerFallbackFor(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) providerFallback {
	if !m.ClaudeFallbackSupported() || pinnedAuthIDFromMetadata(opts.Metadata) != "" || routingFallbackLeg(ctx) {
		return providerFallback{}
	}
	profile := routingProfileFromContext(ctx)
	if profile == nil {
		return providerFallback{}
	}
	normalized := m.normalizeProviders(providers)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	for _, provider := range []string{"claude", "codex"} {
		if !containsProvider(normalized, provider) {
			continue
		}
		policy := profile.providers.Codex
		if provider == "claude" {
			policy = profile.providers.Claude
		}
		var fallback providerFallback
		switch policy.Fallback.Target {
		case config.ProviderFallbackCodex:
			if provider == "claude" {
				fallback = codexFallbackFor(policy.Fallback, eligibility, req, opts)
			}
		case config.ProviderFallbackAPI:
			// The API leg replaces the provider policy, so a request with its own
			// credential constraints keeps them and does not fall back.
			if eligibility.requiredKind == "" && eligibility.credentialPolicy == "" && policy.Mode != config.ProviderModeAPI && policy.APICredential != "" {
				fallback = providerFallback{target: config.ProviderFallbackAPI, mode: "auto", model: req.Model}
			}
		}
		if fallback.target != "" {
			fallback.provider = provider
			return fallback
		}
	}
	return providerFallback{}
}

// codexFallbackFor resolves the Claude -> Codex fallback: the mapped Codex model
// with the requested reasoning suffix preserved, for OAuth-compatible requests.
func codexFallbackFor(fallback config.ProviderFallbackConfig, eligibility authSelectionEligibility, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) providerFallback {
	if eligibility.requiredKind != "" && eligibility.requiredKind != AuthKindOAuth {
		return providerFallback{}
	}
	mode := fallback.Mode
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "always" {
		return providerFallback{}
	}
	source := authSelectionModelFromOptions(opts, req.Model)
	target := fallback.MappingTarget(thinking.ParseSuffix(source).ModelName)
	if strings.TrimSpace(target) == "" {
		return providerFallback{}
	}
	return providerFallback{target: config.ProviderFallbackCodex, mode: mode, model: preserveRequestedModelSuffix(source, target)}
}

// shouldFallbackProvider reports whether a failed primary round may run the
// fallback leg: auth, quota, timeout, and server failures, missing credentials,
// and transient transport errors; never invalid requests, cancellation, or a
// matched request-stop rule.
func shouldFallbackProvider(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isRequestTerminatedError(err) || isRequestStopError(err) || isRequestInvalidError(err) {
		return false
	}
	status := statusCodeFromError(err)
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 {
		return true
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		switch authErr.Code {
		case "auth_not_found", "auth_unavailable":
			return true
		}
	}
	return isTransientTransportError(err)
}

// A completed credential round already tried the available primary pool (up to
// max-retry-credentials). Prefer the fallback, of either target, to sleeping for
// a primary cooldown.
func (m *Manager) hasProviderFallbackAfterError(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, err error) bool {
	fallback := m.providerFallbackFor(ctx, providers, req, opts)
	return fallback.mode == "auto" && fallback.target != "" && shouldFallbackProvider(err)
}

// apiFallbackProfile derives the profile of the API fallback leg: provider in
// mode api with its api-credential, session affinity off so the leg neither reads
// nor writes session bindings, and every fallback off.
func (p *routingProfile) apiFallbackProfile(provider string) *routingProfile {
	derived := *p
	derived.providers = p.providers.Clone()
	derived.providers.Claude.Fallback = config.ProviderFallbackConfig{Target: config.ProviderFallbackOff}
	derived.providers.Codex.Fallback = config.ProviderFallbackConfig{Target: config.ProviderFallbackOff}
	if provider == "claude" {
		derived.providers.Claude.Mode = config.ProviderModeAPI
	} else {
		derived.providers.Codex.Mode = config.ProviderModeAPI
	}
	derived.sticky = false
	derived.providerPolicy = true
	return &derived
}

func executeWithProviderFallback[T any](m *Manager, ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, execute func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (T, error)) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fallback := m.providerFallbackFor(ctx, providers, req, opts)
	var primaryErr error
	if fallback.mode != "always" || fallback.target == "" {
		result, errPrimary := execute(ctx, providers, req, opts)
		// The private paths keep the request-stop marker so a matched stop rule
		// blocks the fallback; strip it once the decision is made.
		shouldFallback := fallback.target != "" && shouldFallbackProvider(errPrimary)
		errPrimary = unwrapRequestStopError(errPrimary)
		if !shouldFallback || (ctx != nil && ctx.Err() != nil) {
			return result, errPrimary
		}
		primaryErr = errPrimary
	}
	if ctx != nil && ctx.Err() != nil {
		var zero T
		return zero, ctx.Err()
	}
	var result T
	var errFallback error
	if fallback.target == config.ProviderFallbackAPI {
		logEntryWithRequestID(ctx).WithFields(log.Fields{
			"provider": fallback.provider, "source_model": req.Model, "fallback_target": fallback.target,
			"primary_status": statusCodeFromError(primaryErr),
		}).Infof("Routing %s request to API key fallback", fallback.provider)
		// The same model runs again on the fallback provider alone, in mode api, so
		// other providers of the request (and their subscriptions) stay out of the
		// leg. The leg calls the inner execute, so it cannot fall back again.
		legCtx := withRoutingFallbackLeg(withRoutingProfile(ctx, routingProfileFromContext(ctx).apiFallbackProfile(fallback.provider)), fallback.target)
		result, errFallback = execute(legCtx, []string{fallback.provider}, req, opts)
	} else {
		logEntryWithRequestID(ctx).WithFields(log.Fields{
			"source_model": req.Model, "fallback_model": fallback.model, "fallback_mode": fallback.mode,
			"fallback_target": fallback.target, "primary_status": statusCodeFromError(primaryErr),
		}).Info("Routing Claude request to Codex fallback")
		opts = ensureRequestedModelMetadata(opts, req.Model)
		delete(opts.Metadata, cliproxyexecutor.AuthSelectionModelMetadataKey)
		req.Model = fallback.model
		// Keep the original payload, client protocol, session, tool history, and
		// requested-model metadata. The Codex executor translates using req.Model.
		// The leg keeps the request's routing profile, so the key's Codex preference and
		// selection apply; its selections are logged under the fallback stage.
		result, errFallback = execute(withRoutingFallbackLeg(withRequiredAuthKind(ctx, AuthKindOAuth), fallback.target), []string{"codex"}, req, opts)
	}
	errFallback = unwrapRequestStopError(errFallback)
	if primaryErr != nil && isFallbackSelectionError(errFallback) {
		// The fallback leg had no usable credential, so the primary failure (with its
		// status and Retry-After) is the more useful answer for the client.
		var zero T
		return zero, primaryErr
	}
	return result, errFallback
}

// isFallbackSelectionError reports whether a fallback leg failed before reaching an
// upstream because no credential could be selected.
func isFallbackSelectionError(err error) bool {
	if err == nil {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) {
		return true
	}
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && (authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable")
}

// Execute resolves the request's routing profile before the fallback wrapper so
// the fallback decision and the fallback leg use the same profile.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = m.ensureRoutingProfile(ctx)
	return executeWithProviderFallback(m, ctx, providers, req, opts, m.execute)
}

func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = m.ensureRoutingProfile(ctx)
	return executeWithProviderFallback(m, ctx, providers, req, opts, m.executeCount)
}

func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx = m.ensureRoutingProfile(ctx)
	// executeStream returns a matched stop rule or a terminated request as an error,
	// like the native stream path. The fallback wrapper strips the stop marker, so
	// record it here: only a failure that exhausted the retries renders as a stream.
	returnedAsError := false
	execute := func(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		result, errStream := m.executeStream(ctx, providers, req, opts)
		returnedAsError = isRequestStopError(errStream) || isRequestTerminatedError(errStream)
		return result, errStream
	}
	result, errStream := executeWithProviderFallback(m, ctx, providers, req, opts, execute)
	var bootstrapErr *streamBootstrapError
	if !returnedAsError && errors.As(errStream, &bootstrapErr) && bootstrapErr != nil {
		return streamErrorResult(bootstrapErr.Headers(), errStream), nil
	}
	return result, errStream
}
