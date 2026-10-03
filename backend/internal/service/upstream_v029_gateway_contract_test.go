package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the merged upstream beta behavior at the physical HTTP boundary,
// together with the local identity and opaque continuation contracts.
func TestUpstreamV029MultiAgentBetaPreservesThreeOSHTTPIdentity(t *testing.T) {
	for _, family := range OpenAIOAuthOSFamilies() {
		for _, path := range []string{"responses", "passthrough"} {
			t.Run(family+"/"+path, func(t *testing.T) {
				account := mergeHTTPAccount()
				profiles, err := BuildOpenAIOAuthOSProfiles(account, &OpenAIOAuthOSProfiles{DefaultOS: family})
				require.NoError(t, err)
				account.OpenAIOAuthOSProfiles = profiles
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_merge_beta", "gpt-5.4")}
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), httpUpstream: upstream}
				headers := http.Header{
					"OpenAI-Beta":              {"responses=experimental, responses_multi_agent=v1", "future_feature=v2"},
					openAICodexTurnStateHeader: {"external-opaque-state"},
				}
				_, recorder, err := mergeHTTPForward(t, gateway, account, path, mergeHTTPBody(path, true), headers)
				require.NoError(t, err, recorder.Body.String())
				require.Len(t, upstream.requests, 1)
				require.Equal(t, []string{"responses_multi_agent=v1", "future_feature=v2"}, upstream.lastReq.Header.Values("OpenAI-Beta"))
				require.Equal(t, family, openai.DetectOSFamilyFromUserAgent(upstream.lastReq.Header.Get("User-Agent")))
				require.Equal(t, profiles.Profiles[family].InstallationID, gjson.Get(upstream.lastReq.Header.Get(openAIWSTurnMetadataHeader), "installation_id").String())
				require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.lastReq.Context()))
				require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
				require.Equal(t, "external-opaque-state", upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
				require.Empty(t, upstream.lastReq.Header.Get("Cookie"))
			})
		}
	}
}

// Text recovery must not manufacture a successful candy result from a partial
// or failed upstream response. Check the forwarding path, not only its writer.
func TestUpstreamV029CandyStreamRecoveryRequiresSuccessfulTerminal(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat"} {
		for _, scenario := range []string{"partial", "done_only", "failed_then_completed", "truncated_then_completed"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				account := newOpenAIRejectedFieldTestAccount()
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
					account.Extra["openai_passthrough_enabled"] = true
				}
				if path == "chat" {
					account.Extra[openai_compat.ExtraKeyResponsesSupported] = false
					account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeAuto)
				}
				repo := &stubOpenAIAccountRepo{accounts: []Account{*account}}
				stream := upstreamV029CandyFailedStream(path, scenario)
				upstream := &httpUpstreamRecorder{}
				for range 3 {
					upstream.responses = append(upstream.responses, &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader(stream)),
					})
				}
				gateway := newOpenAIRejectedFieldTestService(upstream)
				gateway.accountRepo = repo
				// Any health mutation reaches an unimplemented repository method.
				gateway.rateLimitService = &RateLimitService{accountRepo: repo}
				result, err := newCandySyntheticCatalogTransport(repo, gateway).Execute(context.Background(), &CandyTestItem{
					AccountID: account.ID, Model: "gpt-6-astra", PromptVersion: CandyTestPromptVersion,
				})
				require.Error(t, err)
				require.NotNil(t, result)
				require.False(t, result.Completed)
				require.Len(t, upstream.requests, 3, "failed streams allow only two explicit retries")
				require.Equal(t, 2, result.Retries)
				require.True(t, repo.accounts[0].Schedulable)
				require.Equal(t, StatusActive, repo.accounts[0].Status)
			})
		}
	}
}

func upstreamV029CandyFailedStream(path, scenario string) string {
	if path == "chat" {
		partial := "data: {\"id\":\"synthetic\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"32 29 40 38\"},\"finish_reason\":null}]}\n\n"
		complete := "data: {\"id\":\"synthetic\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		switch scenario {
		case "partial":
			return partial
		case "done_only":
			return partial + "data: [DONE]\n\n"
		case "failed_then_completed":
			return partial + "data: {\"error\":{\"message\":\"synthetic failure\"}}\n\n" + complete
		default:
			return partial + "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" + complete
		}
	}
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"32 29 40 38\"}\n\n"
	complete := "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\",\"output\":[]}}\n\n"
	switch scenario {
	case "partial":
		return partial
	case "done_only":
		return partial + "data: [DONE]\n\n"
	case "failed_then_completed":
		return partial + "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"synthetic failure\"}}}\n\n" + complete
	default:
		return partial + "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n" + complete
	}
}
