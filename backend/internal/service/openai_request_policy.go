package service

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
)

// FreezeOpenAIRequestPolicy preserves a logical request's policy through retries.
func FreezeOpenAIRequestPolicy(ctx context.Context, settings *SettingService) context.Context {
	if _, ok := openai.RequestPolicyFromContext(ctx); ok {
		return ctx
	}
	policy := openai.DefaultRequestPolicy()
	if settings != nil {
		policy = settings.GetOpenAIRequestPolicy(ctx)
	}
	return openai.WithRequestPolicy(ctx, policy)
}

// ApplyOpenAIRequestPolicy is called after all upstream header copying and
// overrides, and before observation or dispatch, on explicit OpenAI routes only.
func ApplyOpenAIRequestPolicy(request *http.Request, settings *SettingService) *http.Request {
	if request == nil {
		return nil
	}
	request = request.WithContext(FreezeOpenAIRequestPolicy(request.Context(), settings))
	policy, _ := openai.RequestPolicyFromContext(request.Context())
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	openai.ApplyCodexResidencyHeader(request.Header, policy.CodexResidencyUS)
	return openai.MarkCodexResidencyRequest(request)
}

func OpenAIReqPolicyClient(client *req.Client, ctx context.Context) *req.Client {
	return openai.ReqClientWithRequestPolicy(client, ctx)
}

func (s *ContentModerationService) SetRequestPolicySettingService(settings *SettingService) {
	s.settingService = settings
}

func (s *TokenRefreshService) SetRequestPolicySettingService(settings *SettingService) {
	s.settingService = settings
}
