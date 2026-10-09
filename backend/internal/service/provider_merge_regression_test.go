//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestModelRoutedAccountConnectionMapsModelExactlyOnce(t *testing.T) {
	for _, platform := range []string{PlatformOpenCodeGo, PlatformCommandCode} {
		for _, tc := range []struct {
			protocol string
			target   string
			path     string
			response func() *http.Response
		}{
			{APIProtocolChatCompletions, "deepseek-v4-flash", "/v1/chat/completions", adaptiveCNChatTestResponse},
			{APIProtocolAnthropic, "claude-sonnet-4-6", "/v1/messages", adaptiveCNAnthropicTestResponse},
			{APIProtocolResponses, "gpt-5.5", "/v1/responses", adaptiveCNResponsesTestResponse},
		} {
			t.Run(platform+"/"+tc.protocol, func(t *testing.T) {
				account := adaptiveCNAccountTestAccount(941, platform)
				account.Credentials["protocol_rules"] = []any{map[string]any{
					"pattern": tc.target, "protocol": tc.protocol,
				}}
				account.Credentials["model_mapping"] = map[string]any{
					"public-model": tc.target,
					tc.target:      "unwanted-second-mapping",
				}
				svc, upstream := adaptiveCNAccountTestService(account, tc.response())
				c, _ := newTestContext()

				err := svc.TestAccountConnection(c, account.ID, "public-model", "hi", AccountTestModeDefault)

				require.NoError(t, err)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, tc.path, upstream.requests[0].URL.Path)
				require.Equal(t, tc.target, gjson.GetBytes(upstream.lastBody, "model").String())
			})
		}
	}
}

func TestNewProvidersPreserveCompositeParentAndDaybreakBoundaries(t *testing.T) {
	for _, platform := range []string{PlatformCommandCode, PlatformCline} {
		t.Run(platform, func(t *testing.T) {
			parent := daybreakTestGroup(true, true)
			parent.Platform = PlatformComposite
			ctx := context.WithValue(context.Background(), ctxkey.Group, parent)
			ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{
				Matched: true, TargetPlatform: platform, PublicModel: "public-model", UpstreamModel: "gpt-5.5",
			})
			require.True(t, isConcreteRequestPlatform(platform))
			require.Equal(t, platform, NormalizeOpenAICompatiblePlatform(platform))
			require.Same(t, parent, ctx.Value(ctxkey.Group), "routing must preserve the trusted Composite parent")
			account := adaptiveCNAccountTestAccount(942, platform)
			account.Groups = []*Group{daybreakTestGroup(false, false)}
			body := []byte(`{"model":"gpt-5.5","access_programs":{"cyber":"daybreak_blue"}}`)
			wire, reason, err := daybreakPolicyTestService(false).applyOpenAIDaybreakWithDecision(ctx, account, body)
			require.NoError(t, err)
			require.Equal(t, body, wire, "the global OpenAI switch must not alter a different provider")
			require.Equal(t, "not_oauth", reason)
		})
	}
}
