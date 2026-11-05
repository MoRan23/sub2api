package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesGIFValidationBeforeSend(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, fields := range []string{
			`"output_format":"gif","frame_count":65`,
			`"output_format":"gif","frame_duration_ms":null`,
			`"output_format":"gif","stream":true`,
			`"output_format":"gif","prompt_rewriter":false`,
			`"output_format":"png","frame_count":9`,
		} {
			t.Run(accountType+"/"+fields, func(t *testing.T) {
				body := []byte(`{"model":"gpt-image-1","prompt":"animate",` + fields + `}`)
				c, rec := newOpenAIImagesTestContext(t, body)
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
				require.Error(t, err)
				require.Nil(t, result)
				require.Nil(t, upstream.lastReq)
				require.Equal(t, 400, rec.Code)
				require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			})
		}
	}
}

func TestOpenAIImagesGIFValidationUsesFinalAPIKeyModel(t *testing.T) {
	for _, models := range [][2]string{
		{"gpt-image-2.6-sunburst", "grok-imagine-image"},
		{"grok-imagine-image", "gpt-image-2.6-flare"},
	} {
		t.Run(models[0]+"/"+models[1], func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"animate","output_format":"gif","frame_count":"provider-specific"}`, models[0]))
			c, rec := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			account := newOpenAIImagesAPIKeyAccount()
			account.Credentials["model_mapping"] = map[string]any{models[0]: models[1]}
			_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			if IsGPTImageGenerationModel(models[1]) {
				require.ErrorContains(t, err, "frame_count")
				require.Equal(t, 400, rec.Code)
				require.Nil(t, upstream.lastReq)
			} else {
				require.NoError(t, err)
				require.Equal(t, "provider-specific", gjson.GetBytes(upstream.lastBody, "frame_count").String())
				require.Equal(t, models[1], gjson.GetBytes(upstream.lastBody, "model").String())
			}
		})
	}
}

func TestOpenAIImagesGIFRejectsResponsesBeforeSend(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprint(forced), func(t *testing.T) {
			model := "gpt-image-1"
			ctx := context.Background()
			if forced {
				model = "gpt-image-2.6-sunburst"
				ctx = withOpenAIImagesForceResponses(ctx)
			}
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"animate","output_format":"gif"}`, model))
			c, rec := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			_, err = buildOpenAIImagesResponsesRequest(parsed, model)
			require.ErrorContains(t, err, "GIF output requires")
			_, err = svc.ForwardImages(ctx, c, directImagesTestAccount(), body, parsed, "")
			require.ErrorContains(t, err, "GIF output requires")
			require.Equal(t, 400, rec.Code)
			require.Nil(t, upstream.lastReq)
		})
	}
}
