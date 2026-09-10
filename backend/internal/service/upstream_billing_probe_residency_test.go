package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBillingProbeResidencyRespectsPlatformAndSetting(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGrok, PlatformDeepseek} {
		for _, enabled := range []bool{false, true} {
			t.Run(platform+"/"+strconv.FormatBool(enabled), func(t *testing.T) {
				account := &Account{
					ID: 17, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive,
					Credentials: map[string]any{"api_key": "test-key", "base_url": "https://custom-upstream.example"},
				}
				repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       upstreamBillingProbeValidBody(),
				}}
				settings := &upstreamBillingProbeSettingRepo{values: map[string]string{
					SettingKeyEnableOpenAICodexResidencyUS: strconv.FormatBool(enabled),
				}}
				svc := newUpstreamBillingProbeTestService(repo, upstream, settings)
				_, err := svc.ProbeAccount(context.Background(), account.ID)
				require.NoError(t, err)
				require.Equal(t, "custom-upstream.example", upstream.lastReq.URL.Host)
				want := ""
				if platform == PlatformOpenAI && enabled {
					want = "us"
				}
				require.Equal(t, want, upstream.lastReq.Header.Get(openai.CodexResidencyHeaderName))
			})
		}
	}
}
