package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// ClaudeFallbackSupported excludes routing owned by Home or a scheduler plugin.
func (m *Manager) ClaudeFallbackSupported() bool {
	return m != nil && !m.HomeEnabled() && !m.hasPluginScheduler()
}

func (m *Manager) claudeFallbackTarget(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (string, string) {
	if !m.ClaudeFallbackSupported() || pinnedAuthIDFromMetadata(opts.Metadata) != "" || !containsProvider(m.normalizeProviders(providers), "claude") {
		return "", ""
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	if eligibility.requiredKind != "" && eligibility.requiredKind != AuthKindOAuth {
		return "", ""
	}
	profile := routingProfileFromContext(ctx)
	if profile == nil || !profile.fallback.Enabled {
		return "", ""
	}
	mode := profile.fallback.Mode
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "always" {
		return "", ""
	}
	source := authSelectionModelFromOptions(opts, req.Model)
	target := profile.fallback.Target(thinking.ParseSuffix(source).ModelName)
	if strings.TrimSpace(target) == "" {
		return "", ""
	}
	return mode, preserveRequestedModelSuffix(source, target)
}

func shouldFallbackClaude(err error) bool {
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
// max-retry-credentials). Prefer fallback to sleeping for a primary cooldown.
func (m *Manager) hasClaudeFallbackAfterError(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, err error) bool {
	mode, target := m.claudeFallbackTarget(ctx, providers, req, opts)
	return mode == "auto" && target != "" && shouldFallbackClaude(err)
}

func executeWithClaudeFallback[T any](m *Manager, ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, execute func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (T, error)) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode, target := m.claudeFallbackTarget(ctx, providers, req, opts)
	var primaryErr error
	if mode != "always" || target == "" {
		result, errPrimary := execute(ctx, providers, req, opts)
		// The private paths keep the request-stop marker so a matched stop rule
		// blocks the fallback; strip it once the decision is made.
		fallback := target != "" && shouldFallbackClaude(errPrimary)
		errPrimary = unwrapRequestStopError(errPrimary)
		if !fallback || (ctx != nil && ctx.Err() != nil) {
			return result, errPrimary
		}
		primaryErr = errPrimary
	}
	if ctx != nil && ctx.Err() != nil {
		var zero T
		return zero, ctx.Err()
	}
	logEntryWithRequestID(ctx).WithFields(log.Fields{
		"source_model": req.Model, "fallback_model": target, "fallback_mode": mode,
		"primary_status": statusCodeFromError(primaryErr),
	}).Info("Routing Claude request to Codex fallback")
	opts = ensureRequestedModelMetadata(opts, req.Model)
	delete(opts.Metadata, cliproxyexecutor.AuthSelectionModelMetadataKey)
	req.Model = target
	// Keep the original payload, client protocol, session, tool history, and
	// requested-model metadata. The Codex executor translates using req.Model.
	// The leg keeps the request's routing profile, so the key's Codex preference and
	// selection apply; its selections are logged under the fallback stage.
	result, errFallback := execute(withRoutingFallbackLeg(withRequiredAuthKind(ctx, AuthKindOAuth)), []string{"codex"}, req, opts)
	errFallback = unwrapRequestStopError(errFallback)
	if primaryErr != nil && isFallbackSelectionError(errFallback) {
		// Codex had no usable credential, so the Claude failure (with its status and
		// Retry-After) is the more useful answer for the client.
		var zero T
		return zero, primaryErr
	}
	return result, errFallback
}

// isFallbackSelectionError reports whether the Codex leg failed before reaching an
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
// the fallback decision and the Codex leg use the same profile.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = m.ensureRoutingProfile(ctx)
	return executeWithClaudeFallback(m, ctx, providers, req, opts, m.execute)
}

func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx = m.ensureRoutingProfile(ctx)
	return executeWithClaudeFallback(m, ctx, providers, req, opts, m.executeCount)
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
	result, errStream := executeWithClaudeFallback(m, ctx, providers, req, opts, execute)
	var bootstrapErr *streamBootstrapError
	if !returnedAsError && errors.As(errStream, &bootstrapErr) && bootstrapErr != nil {
		return streamErrorResult(bootstrapErr.Headers(), errStream), nil
	}
	return result, errStream
}
