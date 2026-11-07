package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesGIFOptionCombinationsReachUpstream(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, fields := range []string{
			`"output_format":"gif","frame_count":65`,
			`"output_format":"gif","frame_count":0,"frame_duration_ms":19`,
			`"output_format":"gif","frame_duration_ms":null`,
			`"output_format":"gif","stream":true`,
			`"output_format":"gif","partial_images":0,"output_compression":100`,
			`"output_format":"gif","prompt_rewriter":false`,
			`"output_format":"png","frame_count":9`,
		} {
			t.Run(accountType+"/"+fields, func(t *testing.T) {
				body := []byte(`{"model":"gpt-image-1","prompt":"animate",` + fields + `}`)
				original := string(body)
				c, _ := newOpenAIImagesTestContext(t, body)
				upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
				svc := newOpenAIImagesTestService(upstream)
				parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
				require.NoError(t, err)
				account := newOpenAIImagesAPIKeyAccount()
				if accountType == AccountTypeOAuth {
					account = directImagesTestAccount()
				}
				account.Credentials["model_mapping"] = map[string]any{"gpt-image-1": "gpt-image-2.6-sunburst"}
				result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, "gpt-image-2.6-sunburst", gjson.GetBytes(upstream.lastBody, "model").String())
				for _, field := range []string{"output_format", "frame_count", "frame_duration_ms", "stream", "partial_images", "output_compression", "prompt_rewriter"} {
					input := gjson.GetBytes(body, field)
					output := gjson.GetBytes(upstream.lastBody, field)
					if accountType == AccountTypeOAuth && (input.Type == gjson.Null || field == "prompt_rewriter") {
						require.False(t, output.Exists(), field)
					} else {
						require.Equal(t, input.Raw, output.Raw, field)
					}
				}
				require.Equal(t, original, string(body), "the retry source stays unchanged")
			})
		}
	}
}

func TestOpenAIImagesGIFAPIKeyMappingPreservesProviderFields(t *testing.T) {
	for _, models := range [][2]string{
		{"gpt-image-2.6-sunburst", "grok-imagine-image"},
		{"grok-imagine-image", "gpt-image-2.6-flare"},
	} {
		t.Run(models[0]+"/"+models[1], func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"animate","output_format":"gif","frame_count":65,"custom_option":{"provider":"specific"}}`, models[0]))
			c, _ := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			account := newOpenAIImagesAPIKeyAccount()
			account.Credentials["model_mapping"] = map[string]any{models[0]: models[1]}
			_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			require.NoError(t, err)
			require.EqualValues(t, 65, gjson.GetBytes(upstream.lastBody, "frame_count").Int())
			require.JSONEq(t, `{"provider":"specific"}`, gjson.GetBytes(upstream.lastBody, "custom_option").Raw)
			require.Equal(t, models[1], gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}

func TestOpenAIImagesGIFResponsesBridgeKeepsOutputButRequiresNativeFrameControls(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"animate","output_format":"gif"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	parsed, err := newOpenAIImagesTestService(nil).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	payload, err := buildOpenAIImagesResponsesRequest(parsed, parsed.Model)
	require.NoError(t, err)
	require.Equal(t, "gif", gjson.GetBytes(payload, "tools.0.output_format").String())
	count := 2
	parsed.FrameCount = &count
	_, err = buildOpenAIImagesResponsesRequest(parsed, parsed.Model)
	require.ErrorContains(t, err, "native Images endpoint")
}
