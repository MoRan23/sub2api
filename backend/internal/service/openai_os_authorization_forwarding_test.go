package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type osAuthorizationDefaultChangingRepo struct {
	*schedulerOSAuthorizationTestRepo
	changed bool
}

func (r *osAuthorizationDefaultChangingRepo) GetOpenAIOAuthOSCredential(ctx context.Context, id int64, os string) (*OpenAIOAuthOSCredential, error) {
	if !r.changed {
		r.changed = true
		r.accounts[0].OpenAIOAuthOSProfiles.DefaultOS = OpenAIOSLinux
	}
	return r.schedulerOSAuthorizationTestRepo.GetOpenAIOAuthOSCredential(ctx, id, os)
}

func TestOpenAIOSAuthorizationDirectForwardingKeepsTokenAndIdentityTogether(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, unknown := range []bool{false, true} {
			name := path + "/known-environment"
			if unknown {
				name = path + "/unknown-default-changes"
			}
			t.Run(name, func(t *testing.T) {
				account := schedulerOSAuthorizedAccount(981, OpenAIOSWindows, OpenAIOSWindows, OpenAIOSLinux)
				account.Credentials["access_token"] = "canonical-default-token"
				profiles, err := BuildOpenAIOAuthOSProfiles(&account, account.OpenAIOAuthOSProfiles)
				require.NoError(t, err)
				account.OpenAIOAuthOSProfiles = profiles
				if path == "passthrough" {
					account.Extra["openai_oauth_passthrough"] = true
				}
				baseRepo := newSchedulerOSAuthorizationTestRepo(account)
				var repo AccountRepository = baseRepo
				if unknown {
					repo = &osAuthorizationDefaultChangingRepo{schedulerOSAuthorizationTestRepo: baseRepo}
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest,
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"test response"}}`))}}
				svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{}, httpUpstream: upstream}
				c := osIdentityTestContext(t, "unknown-client/1.0")
				text := "<environment_context><os>Linux</os></environment_context>"
				expectedOS := OpenAIOSLinux
				if unknown {
					text, expectedOS = "hello", OpenAIOSWindows
				}
				body := []byte(`{"model":"gpt-5.1","instructions":"test","stream":false,"input":[{"role":"user","content":"` + text + `"}]}`)
				if path == "chat" || path == "messages" {
					body = []byte(`{"model":"gpt-5.1","stream":false,"max_tokens":20,"messages":[{"role":"user","content":"` + text + `"}]}`)
				}
				// Retain the pre-capture context, as direct service callers do.
				ctx := c.Request.Context()
				switch path {
				case "chat":
					_, _ = svc.ForwardAsChatCompletions(ctx, c, &account, body, "", "")
				case "messages":
					_, _ = svc.ForwardAsAnthropic(ctx, c, &account, body, "", "")
				default:
					_, _ = svc.Forward(ctx, c, &account, body)
				}
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, "Bearer access-981/"+expectedOS, upstream.lastReq.Header.Get("Authorization"))
				require.Equal(t, expectedOS, openai.DetectOSFamilyFromUserAgent(upstream.lastReq.UserAgent()))
				plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
				require.True(t, ok)
				require.Equal(t, expectedOS, plan.CredentialOS)
				require.Equal(t, "generation-981/"+expectedOS, plan.AuthorizationGeneration)
				require.Empty(t, account.OpenAIOAuthCredentialOS, "a canonical shared account must not become request-scoped")
				require.Equal(t, "canonical-default-token", account.GetCredential("access_token"))
			})
		}
	}
}
