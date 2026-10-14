package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Attribution workers persist retries themselves. This internal marker never
// changes the legacy Pelican retry policy or ordinary gateway requests.
type attributionExecutionContextKey struct{}
type attributionProbeAuthorizationKey struct{}

func isAttributionExecution(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(attributionExecutionContextKey{}).(bool)
	return enabled
}

func attributionProbeAuthorization(business, credential *Account) string {
	identity := map[string]any{
		"business_type": business.Type, "business_platform": business.Platform,
		"business_base_url": business.GetCredential("base_url"), "engine": business.IsCodexEngine(),
		"credential_id": credential.ID, "credential_type": credential.Type, "credential_platform": credential.Platform,
		"owner": credential.OpenAIOAuthCredentialOwnerID, "generation": credential.OpenAIOAuthAuthorizationGeneration,
		"os": credential.OpenAIOAuthCredentialOS, "base_url": credential.GetCredential("base_url"),
	}
	for _, key := range []string{"chatgpt_account_id", "chatgpt_user_id", "organization_id", openAIAuthModeCredentialKey, openAIAuthModeLegacyCredentialKey} {
		identity[key] = credential.GetCredential(key)
	}
	if credential.OpenAIOAuthAuthorizationGeneration == "" || !credential.IsOpenAIOAuth() || credential.IsOpenAIPersonalAccessToken() || credential.IsOpenAIAgentIdentity() {
		identity["static"] = openAIRefreshAuthIdentity(credential.Credentials)
		identity["api_key"] = credential.GetOpenAIProtocolAPIKey()
		if credential.IsOpenAIAgentIdentity() {
			identity["agent_private_key"] = credential.GetCredential("agent_private_key")
			identity["agent_runtime_id"] = credential.GetCredential("agent_runtime_id")
		}
	}
	return AttributionDigest(identity)
}

func attributionPartialForwardResult(model, upstreamModel string, usage *OpenAIUsage, headers http.Header) *OpenAIForwardResult {
	result := &OpenAIForwardResult{Model: model, UpstreamModel: upstreamModel, ResponseHeaders: headers.Clone()}
	if usage != nil {
		result.Usage = *usage
	}
	return result
}

type attributionProbeHTTPError struct {
	status     int
	retryAfter time.Duration
}

func (e *attributionProbeHTTPError) Error() string { return e.CandyTestFailureCode() }
func (e *attributionProbeHTTPError) CandyTestFailureCode() string {
	return fmt.Sprintf("upstream_http_%d", e.status)
}

func candyTestHTTPError(ctx context.Context, resp *http.Response) error {
	if isAttributionExecution(ctx) {
		return &attributionProbeHTTPError{status: resp.StatusCode, retryAfter: attributionRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	return candyTestError(fmt.Sprintf("upstream_http_%d", resp.StatusCode))
}

// AttributionProbeRetryStatus returns only explicit protocol status evidence.
// In particular, a rate-limit code or human-readable message cannot turn a 503
// into a 429, or turn an unclassified stream failure into an endless retry.
func AttributionProbeRetryStatus(err error, execution *CandyTestExecution) (int, time.Duration) {
	if execution != nil && execution.UpstreamStatus >= 400 && execution.UpstreamStatus <= 599 {
		return execution.UpstreamStatus, execution.RetryAfter
	}
	var statusError *attributionProbeHTTPError
	if errors.As(err, &statusError) {
		return statusError.status, statusError.retryAfter
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		// The gateway's semantic fallback status may come from an error code.
		// Prefer original SSE evidence, never its inferred status here.
		if status := attributionStreamStatus(failover.ResponseBody); status != 0 {
			return status, attributionRetryAfter(failover.ResponseHeaders.Get("Retry-After"), time.Now())
		}
	}
	var failure CandyTestFailure
	if errors.As(err, &failure) {
		code := failure.CandyTestFailureCode()
		if strings.HasPrefix(code, "upstream_http_") {
			if status, parseErr := strconv.Atoi(strings.TrimPrefix(code, "upstream_http_")); parseErr == nil && status >= 400 && status <= 599 {
				return status, 0
			}
		}
	}
	return 0, 0
}

func safeAttributionProbeError(ctx context.Context, err error) error {
	var engine *codexEngineResponseError
	if errors.As(err, &engine) && engine.status == http.StatusServiceUnavailable {
		// HTTP and strict SSE evidence have already been read from the diagnostic
		// writer. Engine's permissive semantic parser also accepts fractional
		// status values; it must not authorize an unlimited attribution retry.
		return candyTestError("upstream_stream_failed")
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) && failover.StatusCode == http.StatusServiceUnavailable && attributionStreamStatus(failover.ResponseBody) == 0 {
		// Business forwarding may infer capacity shedding from an error code.
		// Attribution needs explicit 503 evidence for persistent retries.
		return candyTestError("upstream_stream_failed")
	}
	return safeCandyTestError(ctx, err)
}

func attributionStreamStatus(payload []byte) int {
	for _, path := range openAIStreamErrorStatusPaths {
		value := gjson.GetBytes(payload, path)
		if value.Type != gjson.Number && value.Type != gjson.String {
			continue
		}
		// A strict integer parse rejects fractional values and prose containing
		// status-like numbers. Some compatible providers encode integers as strings.
		if status, err := strconv.Atoi(value.String()); err == nil && status >= 400 && status <= 599 {
			return status
		}
	}
	return 0
}

func attributionRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds >= 300 {
			return 300 * time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		wait := date.Sub(now)
		if wait > 300*time.Second {
			return 300 * time.Second
		}
		if wait > 0 {
			return wait
		}
	}
	return 0
}
