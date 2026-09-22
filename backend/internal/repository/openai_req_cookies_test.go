package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestOpenAIReqCookiesFactoriesIsolateManagersAndDisableSharedJar(t *testing.T) {
	first, second := openaicookies.NewManager(), openaicookies.NewManager()
	for _, factory := range []func(string, *openaicookies.Manager) (*req.Client, error){createOpenAIReqClientWithCookies, CreatePrivacyReqClientWithCookies} {
		a, err := factory("", first)
		require.NoError(t, err)
		reused, err := factory("", first)
		require.NoError(t, err)
		b, err := factory("", second)
		require.NoError(t, err)
		require.Same(t, a, reused)
		require.NotSame(t, a, b)
		require.Nil(t, a.GetClient().Jar)
		require.Nil(t, b.GetClient().Jar)
		require.Nil(t, a.Clone().GetClient().Jar, "later req clones cannot recreate a default jar")
		require.IsNotType(t, &codexnative.Dispatcher{}, a.GetClient().Transport, "cookies wrap the actual native dispatcher")
	}
	ordinary, err := getSharedReqClient(reqClientOptions{})
	require.NoError(t, err)
	require.NotNil(t, ordinary.GetClient().Jar, "other providers retain their cookie behavior")
}

func TestOpenAIReqCookiesWrapEveryNativeRedirectAfterPolicyClone(t *testing.T) {
	manager := openaicookies.NewManager()
	client := req.C().ImpersonateFirefox()
	sharedJar := client.GetClient().Jar
	var observed []*http.Request
	dispatcher := codexnative.NewDispatcher(nil, func(codexnative.Platform) (http.RoundTripper, error) {
		return req.HttpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			observed = append(observed, request.Clone(request.Context()))
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}
			switch len(observed) {
			case 1:
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://chatgpt.com/second")
				response.Header.Set("Set-Cookie", "__cf_bm=private; Secure; Path=/")
			case 2:
				response.StatusCode = http.StatusFound
				response.Header.Set("Location", "https://unrelated.example/end")
			}
			return response, nil
		}), nil
	})
	client.GetClient().Transport = manager.Wrap(dispatcher)
	client.SetCommonHeader("Cookie", "user_cookie=must-not-forward")
	ctx := openaicookies.WithScope(context.Background(), openaicookies.Scope{EphemeralID: "flow"})
	ctx = codexnative.WithScope(ctx, codexnative.Scope{AccountID: 1, Purpose: "auth"})
	derived := openai.ReqClientWithRequestPolicy(client, ctx)
	require.Same(t, client.GetClient().Transport, derived.GetClient().Transport)
	require.Nil(t, derived.GetClient().Jar)
	response, err := derived.R().SetContext(ctx).Get("https://chatgpt.com/first")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Len(t, observed, 3)
	require.Empty(t, observed[0].Header.Get("Cookie"))
	require.Equal(t, "__cf_bm=private", observed[1].Header.Get("Cookie"))
	require.Empty(t, observed[2].Header.Get("Cookie"))
	require.Empty(t, sharedJar.Cookies(observed[0].URL))
	manager.ClearEphemeral("flow")
	_, err = derived.R().SetContext(ctx).Get("https://chatgpt.com/after-completion")
	require.NoError(t, err)
	require.Empty(t, observed[3].Header.Get("Cookie"))
}

func TestOpenAIReqCookiesDirectTokenScopeLifetime(t *testing.T) {
	service := &openaiOAuthService{cookies: openaicookies.NewManager()}
	bound := openaicookies.Scope{OwnerAccountID: 7, OSFamily: "linux", AuthorizationGeneration: "generation"}
	ctx := openaicookies.WithScope(context.Background(), bound)
	refreshed, release := service.ensureCookieScope(ctx, false)
	defer release()
	actual, _ := openaicookies.ScopeFromContext(refreshed)
	require.Equal(t, bound, actual)
	authorized, endAuthorization := service.ensureCookieScope(ctx, true)
	defer endAuthorization()
	temporary, _ := openaicookies.ScopeFromContext(authorized)
	require.True(t, temporary.Valid())
	require.NotEmpty(t, temporary.EphemeralID)
	nested, endNested := service.ensureCookieScope(authorized, true)
	defer endNested()
	actual, _ = openaicookies.ScopeFromContext(nested)
	require.Equal(t, temporary, actual)
	other, endOther := service.ensureCookieScope(nil, false)
	defer endOther()
	actual, _ = openaicookies.ScopeFromContext(other)
	require.NotEqual(t, temporary.EphemeralID, actual.EphemeralID)
}
