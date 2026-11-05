package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesSVGForwarding(t *testing.T) {
	svg := `<?xml version="1.0" encoding="UTF-8"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><title>小鱼</title><path d="M2 16L28 2V30Z" fill="#f80"/></svg>`
	encoded := base64.StdEncoding.EncodeToString([]byte(svg))
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
			for _, responseFormat := range []string{"b64_json", "url"} {
				for _, formatMetadata := range []string{"root", "item", "omitted"} {
					t.Run(model+"/"+accountType+"/"+responseFormat+"/"+formatMetadata, func(t *testing.T) {
						body := []byte(fmt.Sprintf(`{"model":"gpt-image-1","prompt":"draw a fish","n":1,"output_format":"svg","quality":"max","complexity":"high","response_format":%q}`, responseFormat))
						original := bytes.Clone(body)
						c, rec := newOpenAIImagesTestContext(t, body)
						rootFormat, itemFormat := "", ""
						if formatMetadata == "root" {
							rootFormat = `,"output_format":"svg"`
						} else if formatMetadata == "item" {
							itemFormat = `,"output_format":"svg"`
						}
						responseBody := fmt.Sprintf(`{"created":1%s,"data":[{"b64_json":%q%s}],"usage":{"input_tokens":12,"output_tokens":24}}`, rootFormat, encoded, itemFormat)
						upstream := &httpUpstreamRecorder{resp: &http.Response{
							StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
							Body: io.NopCloser(strings.NewReader(responseBody)),
						}}
						svc := newOpenAIImagesTestService(upstream)
						parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
						require.NoError(t, err)
						account := newOpenAIImagesAPIKeyAccount()
						wantPath := "/v1/images/generations"
						if accountType == AccountTypeOAuth {
							account = directImagesTestAccount()
							wantPath = "/backend-api/codex/images/generations"
						}
						account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": model}
						result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "gpt-image-2")
						require.NoError(t, err)
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, wantPath, upstream.lastReq.URL.Path)
						require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
						for _, field := range []string{"output_format", "quality", "complexity"} {
							require.Equal(t, gjson.GetBytes(body, field).String(), gjson.GetBytes(upstream.lastBody, field).String())
						}
						require.Equal(t, 1, result.ImageCount)
						require.Equal(t, 12, result.Usage.InputTokens)
						require.Equal(t, 24, result.Usage.OutputTokens)
						item := gjson.GetBytes(rec.Body.Bytes(), "data.0")
						got := item.Get("b64_json").String()
						if responseFormat == "url" && accountType == AccountTypeOAuth {
							require.False(t, item.Get("b64_json").Exists())
							require.Equal(t, "data:image/svg+xml;base64,"+encoded, item.Get("url").String())
							got = strings.TrimPrefix(item.Get("url").String(), "data:image/svg+xml;base64,")
						}
						require.Equal(t, encoded, got)
						decoded, err := base64.StdEncoding.DecodeString(got)
						require.NoError(t, err)
						require.Equal(t, svg, string(decoded))
						require.Equal(t, original, body)
					})
				}
			}
		}
	}
}

func TestOpenAIImagesSVGValidationBeforeSend(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, tc := range []struct{ fields, want string }{
			{`"n":2`, "n=1"}, {`"n":0`, "n must be greater than 0"}, {`"n":-1`, "n must be greater than 0"}, {`"n":1.5`, "n=1"},
			{`"stream":true`, "streaming"}, {`"partial_images":0`, "partial_images"},
			{`"output_compression":100`, "output_compression"},
			{`"prompt_rewriter":true`, "prompt_rewriter"}, {`"prompt_rewriter":false`, "prompt_rewriter"}, {`"prompt_rewriter":null`, "prompt_rewriter"},
			{`"frame_count":9`, "only supported with output_format gif"},
		} {
			t.Run(accountType+"/"+tc.fields, func(t *testing.T) {
				body := []byte(`{"model":"gpt-image-1","prompt":"draw","output_format":"svg",` + tc.fields + `}`)
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
				_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
				require.ErrorContains(t, err, tc.want)
				require.Equal(t, 400, rec.Code)
				require.Nil(t, upstream.lastReq)
			})
		}
	}
}

func TestOpenAIImagesSVGMultipartAndEdits(t *testing.T) {
	for _, forbidden := range []string{"", "partial_images", "output_compression", "prompt_rewriter"} {
		t.Run(forbidden, func(t *testing.T) {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			for key, value := range map[string]string{"model": "gpt-image-2.6-flare", "prompt": "draw", "output_format": "svg"} {
				require.NoError(t, w.WriteField(key, value))
			}
			if forbidden != "" {
				require.NoError(t, w.WriteField(forbidden, "0"))
			}
			require.NoError(t, w.Close())
			c, _ := newOpenAIImagesTestContext(t, body.Bytes())
			c.Request.Header.Set("Content-Type", w.FormDataContentType())
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, body.Bytes())
			require.NoError(t, err)
			payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
			if forbidden != "" {
				require.ErrorContains(t, err, forbidden)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "svg", gjson.GetBytes(payload, "output_format").String())
			require.False(t, gjson.GetBytes(payload, "n").Exists())
			parsed.Endpoint = openAIImagesEditsEndpoint
			_, _, err = buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
			require.ErrorContains(t, err, "only supported by images/generations")
		})
	}
}

func TestOpenAIImagesSVGRejectsResponsesBeforeSend(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprint(forced), func(t *testing.T) {
			model := "gpt-image-1"
			ctx := context.Background()
			if forced {
				model = "gpt-image-2.6-sunburst"
				ctx = withOpenAIImagesForceResponses(ctx)
			}
			body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"draw","output_format":"svg"}`, model))
			c, rec := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			_, err = buildOpenAIImagesResponsesRequest(parsed, model)
			require.ErrorContains(t, err, "SVG output requires")
			_, err = svc.ForwardImages(ctx, c, directImagesTestAccount(), body, parsed, "")
			require.ErrorContains(t, err, "SVG output requires")
			require.Equal(t, 400, rec.Code)
			require.Nil(t, upstream.lastReq)
		})
	}
}

func TestOpenAIImagesSVGErrorsAndProviderIsolation(t *testing.T) {
	for _, status := range []int{400, 404, 405} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			responseBody := `{"error":{"type":"invalid_request_error","code":"svg_unavailable","message":"SVG unavailable","param":"output_format"}}`
			upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				require.Equal(t, "/backend-api/codex/images/generations", req.URL.Path)
				return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"svg-error"}}, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			}}
			body := []byte(`{"model":"gpt-image-2.6-sunburst","prompt":"draw","output_format":"svg"}`)
			c, rec := newOpenAIImagesTestContext(t, body)
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			_, err = svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
			require.Error(t, err)
			require.Equal(t, 1, calls)
			if status == 405 {
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, status, failover.StatusCode)
				require.JSONEq(t, responseBody, string(failover.ResponseBody))
				return
			}
			require.Equal(t, status, rec.Code)
			require.Equal(t, "svg_unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
		})
	}
	// GPT-specific SVG restrictions apply to the final model only.
	body := []byte(`{"model":"gpt-image-2.6-flare","prompt":"draw","output_format":"svg","n":2,"prompt_rewriter":false}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
	require.NoError(t, err)
	account := newOpenAIImagesAPIKeyAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-image-2.6-flare": "grok-imagine-image"}
	_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	require.NoError(t, err)
	require.EqualValues(t, 2, gjson.GetBytes(upstream.lastBody, "n").Int())
	require.True(t, gjson.GetBytes(upstream.lastBody, "prompt_rewriter").Exists())
	for _, format := range []string{"svg", "SVG", " svg ", "image/svg+xml"} {
		require.Equal(t, "image/svg+xml", openAIImageOutputMIMEType(format))
	}
}
