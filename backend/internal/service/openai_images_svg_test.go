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

func TestOpenAIImagesSVGOptionCombinationsReachUpstream(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, fields := range []string{
			`"n":2`, `"stream":true`, `"partial_images":0`,
			`"output_compression":100`, `"prompt_rewriter":true`,
			`"prompt_rewriter":false`, `"prompt_rewriter":null`, `"frame_count":9`,
		} {
			t.Run(accountType+"/"+fields, func(t *testing.T) {
				body := []byte(`{"model":"gpt-image-1","prompt":"draw","output_format":"svg",` + fields + `}`)
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
				_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, "svg", gjson.GetBytes(upstream.lastBody, "output_format").String())
				for _, field := range []string{"n", "stream", "partial_images", "output_compression", "prompt_rewriter", "frame_count"} {
					input := gjson.GetBytes(body, field)
					if !input.Exists() {
						continue
					}
					output := gjson.GetBytes(upstream.lastBody, field)
					if accountType == AccountTypeOAuth && field == "prompt_rewriter" {
						require.False(t, output.Exists(), "OAuth does not forward unknown options")
					} else {
						require.Equal(t, input.Raw, output.Raw, field)
					}
				}
			})
		}
	}
}

func TestOpenAIImagesSVGMultipartAndEdits(t *testing.T) {
	for _, option := range []string{"", "partial_images", "output_compression", "prompt_rewriter"} {
		t.Run(option, func(t *testing.T) {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			for key, value := range map[string]string{"model": "gpt-image-2.6-flare", "prompt": "draw", "output_format": "svg"} {
				require.NoError(t, w.WriteField(key, value))
			}
			if option != "" {
				require.NoError(t, w.WriteField(option, "0"))
			}
			image, err := w.CreateFormFile("image", "source.png")
			require.NoError(t, err)
			_, err = image.Write([]byte("png"))
			require.NoError(t, err)
			require.NoError(t, w.Close())
			c, _ := newOpenAIImagesTestContext(t, body.Bytes())
			c.Request.Header.Set("Content-Type", w.FormDataContentType())
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, body.Bytes())
			require.NoError(t, err)
			payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
			require.NoError(t, err)
			require.Equal(t, "svg", gjson.GetBytes(payload, "output_format").String())
			require.False(t, gjson.GetBytes(payload, "n").Exists())
			if option == "partial_images" || option == "output_compression" {
				require.Equal(t, "0", gjson.GetBytes(payload, option).Raw)
			}
			parsed.Endpoint = openAIImagesEditsEndpoint
			payload, endpoint, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
			require.NoError(t, err)
			require.Contains(t, endpoint, "/images/edits")
			require.Equal(t, "svg", gjson.GetBytes(payload, "output_format").String())
			require.Len(t, gjson.GetBytes(payload, "images").Array(), 1)
		})
	}
}

func TestOpenAIImagesSVGResponsesBridgeKeepsOutputButRequiresNativeAuthorOptions(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"draw","output_format":"svg"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	parsed, err := newOpenAIImagesTestService(nil).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	payload, err := buildOpenAIImagesResponsesRequest(parsed, parsed.Model)
	require.NoError(t, err)
	require.Equal(t, "svg", gjson.GetBytes(payload, "tools.0.output_format").String())
	parsed.SVG = &OpenAIImagesSVGOptions{Model: "gpt-6-astra"}
	_, err = buildOpenAIImagesResponsesRequest(parsed, parsed.Model)
	require.ErrorContains(t, err, "native Images endpoint")
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
	// Compatible providers retain their own options across model mapping.
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
