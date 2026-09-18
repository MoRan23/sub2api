package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestEnvironmentMetadataTraceHTTPUsesFrozenIngressOnce(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "metadata-trace-request")))
	body := timezoneTestBody(t, map[string]any{"input": []any{map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "input_text", "text": timezoneTestEnvironment("Asia/Shanghai", "2026-09-18")}},
		"internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user_message"}},
	}}})
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 10)
	c.Request = c.Request.WithContext(ctx)
	svc := &OpenAIGatewayService{}
	svc.CaptureOpenAIRequestTimezone(c, body)
	// Simulate later adaptation: diagnostics must describe the original
	// conflicting marker, not a marker replaced after capture.
	adapted, err := sjson.SetBytes(body, "input.0.internal_chat_message_metadata_passthrough.content_item_kinds", []string{"environments.environment_context"})
	require.NoError(t, err)
	result := svc.prepareOpenAIRequestTimezone(ctx, c, newOpenAIIdentityPathOAuthAccount(91), adapted, false)
	require.Equal(t, adapted, result, "temporary diagnostics cannot change the original eligibility decision")
	require.Len(t, logs.FilterMessage("openai.environment_metadata_trace").All(), 1)
	entry := logs.FilterMessage("openai.environment_metadata_trace").All()[0]
	require.Equal(t, "metadata-trace-request", entry.ContextMap()["request_id"])
	require.EqualValues(t, 91, entry.ContextMap()["account_id"])
	require.Equal(t, "ingress_before_timezone", entry.ContextMap()["stage"])
	encoded, err := json.Marshal(entry.ContextMap())
	require.NoError(t, err)
	require.Contains(t, string(encoded), "marker_mismatch")
	for _, passthrough := range []bool{false, true} {
		retry := svc.prepareOpenAIRequestTimezone(ctx, c, newOpenAIIdentityPathOAuthAccount(92), adapted, passthrough)
		require.Equal(t, result, retry)
	}
	require.Len(t, logs.FilterMessage("openai.environment_metadata_trace").All(), 1, "account and passthrough retries share one ingress trace")
}

func TestEnvironmentMetadataTraceHTTPGates(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core))
	body := timezoneTestBody(t, map[string]any{"input": []any{timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))}})
	svc := &OpenAIGatewayService{}
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 11)
	svc.prepareOpenAIRequestTimezone(ctx, c, newOpenAIIdentityPathAPIKeyAccount(91), body, false)
	require.Zero(t, logs.Len(), "API key traffic does not emit OAuth diagnostics")
	globalFingerprintObserver.enabled.Store(false)
	c, _ = newOpenAIIdentityPathContext(t, "/responses", body, 12)
	svc.prepareOpenAIRequestTimezone(ctx, c, newOpenAIIdentityPathOAuthAccount(91), body, false)
	require.Zero(t, logs.Len(), "turning fingerprint capture off also stops temporary logs")
}

func TestEnvironmentMetadataTraceWSOncePerAcceptedFrame(t *testing.T) {
	enableOpenAIIdentityPathFingerprintObservation(t)
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "metadata-trace-ws")))
	ctx = openai.WithRequestPolicy(ctx, timezoneTestPolicy())
	body := timezoneTestBody(t, map[string]any{"type": "response.create", "input": []any{timezoneTestMessage(timezoneTestEnvironment("Asia/Shanghai", "2026-09-18"))}})
	c, _ := newOpenAIIdentityPathContext(t, "/responses", body, 13)
	svc := &OpenAIGatewayService{}
	account := newOpenAIIdentityPathOAuthAccount(93)
	accepted := timezoneTestAcceptedAt()
	first, _ := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted)
	retry, _ := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, true, accepted.Add(time.Hour))
	require.Equal(t, first, retry)
	require.Len(t, logs.FilterMessage("openai.environment_metadata_trace").All(), 1)
	next, _ := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, false, accepted)
	require.Equal(t, first, next)
	require.Len(t, logs.FilterMessage("openai.environment_metadata_trace").All(), 2)
	require.Equal(t, "ws_frame_before_timezone", logs.All()[0].ContextMap()["stage"])
	globalFingerprintObserver.enabled.Store(false)
	withoutTrace, _ := svc.prepareOpenAIWSFrameTimezone(ctx, c, account, body, false, false, accepted)
	require.Equal(t, first, withoutTrace, "diagnostics must not affect bytes forwarded upstream")
	require.Len(t, logs.FilterMessage("openai.environment_metadata_trace").All(), 2)
}
