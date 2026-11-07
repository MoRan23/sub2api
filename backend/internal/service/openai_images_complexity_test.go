package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func openAIImagesComplexityTestBody(t *testing.T, form, edit, complexity bool) ([]byte, string) {
	t.Helper()
	fields := map[string]string{"model": "gpt-image-1", "prompt": "draw a lantern", "quality": "max"}
	if complexity {
		fields["complexity"] = "high"
	}
	if !form {
		payload := make(map[string]any, len(fields)+1)
		for key, value := range fields {
			payload[key] = value
		}
		if edit {
			payload["images"] = []map[string]string{{"image_url": "data:image/png;base64,cG5n"}}
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		return body, "application/json"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range fields {
		require.NoError(t, w.WriteField(key, value))
	}
	if edit {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="image"; filename="source.png"`)
		header.Set("Content-Type", "image/png")
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write([]byte("png"))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

func TestOpenAIImagesComplexityForwarding(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
			for _, form := range []bool{false, true} {
				for _, edit := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s/multipart_%t/edit_%t", model, accountType, form, edit)
					t.Run(name, func(t *testing.T) {
						body, contentType := openAIImagesComplexityTestBody(t, form, edit, true)
						original := bytes.Clone(body)
						c, _ := newOpenAIImagesTestContext(t, body)
						c.Request.Header.Set("Content-Type", contentType)
						if edit {
							c.Request.URL.Path = openAIImagesEditsEndpoint
						}
						upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
						svc := newOpenAIImagesTestService(upstream)
						parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
						require.NoError(t, err)
						require.Equal(t, "high", parsed.Complexity)
						account := newOpenAIImagesAPIKeyAccount()
						pathPrefix := "/v1/images/"
						if accountType == AccountTypeOAuth {
							account = directImagesTestAccount()
							pathPrefix = "/backend-api/codex/images/"
						}
						account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": model}
						result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "gpt-image-2")
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Equal(t, model, result.UpstreamModel)
						endpoint := "generations"
						if edit {
							endpoint = "edits"
						}
						require.Equal(t, pathPrefix+endpoint, upstream.lastReq.URL.Path)
						if form && accountType == AccountTypeAPIKey {
							_, params, err := mime.ParseMediaType(upstream.lastReq.Header.Get("Content-Type"))
							require.NoError(t, err)
							forwarded, err := multipart.NewReader(bytes.NewReader(upstream.lastBody), params["boundary"]).ReadForm(1 << 20)
							require.NoError(t, err)
							t.Cleanup(func() { _ = forwarded.RemoveAll() })
							require.Equal(t, []string{model}, forwarded.Value["model"])
							require.Equal(t, []string{"high"}, forwarded.Value["complexity"])
							require.Equal(t, []string{"max"}, forwarded.Value["quality"])
							if edit {
								require.Len(t, forwarded.File["image"], 1)
							}
						} else {
							require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
							require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "complexity").String())
							require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "quality").String(), "quality and complexity are independent, with no conversion")
							require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
							if edit {
								require.Equal(t, "data:image/png;base64,cG5n", gjson.GetBytes(upstream.lastBody, "images.0.image_url").String())
							}
						}
						require.Equal(t, original, body, "forwarding must preserve the retry source")
					})
				}
			}
		}
	}
}

func TestOpenAIImagesComplexityMissingIsNotDefaulted(t *testing.T) {
	for _, form := range []bool{false, true} {
		t.Run(fmt.Sprintf("multipart_%t", form), func(t *testing.T) {
			body, contentType := openAIImagesComplexityTestBody(t, form, false, false)
			c, _ := newOpenAIImagesTestContext(t, body)
			c.Request.Header.Set("Content-Type", contentType)
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			require.Empty(t, parsed.Complexity)
			payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-sunburst")
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(payload, "complexity").Exists())
			require.Equal(t, "max", gjson.GetBytes(payload, "quality").String())
		})
	}
}

func TestOpenAIImagesComplexityValuesRemainIndependentOfQuality(t *testing.T) {
	for _, value := range []string{"low", "medium", "high", " high "} {
		for _, edit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/edit_%t", value, edit), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"prompt":"draw a lantern","complexity":%q,"quality":" max ","images":[{"image_url":"data:image/png;base64,cG5n"}]}`, value))
				c, _ := newOpenAIImagesTestContext(t, body)
				if edit {
					c.Request.URL.Path = openAIImagesEditsEndpoint
				}
				parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, body)
				require.NoError(t, err)
				for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
					payload, _, err := buildOpenAIImagesOAuthPayload(parsed, model)
					require.NoError(t, err)
					require.Equal(t, strings.TrimSpace(value), gjson.GetBytes(payload, "complexity").String())
					require.Equal(t, "max", gjson.GetBytes(payload, "quality").String())
				}
			})
		}
	}
}

func TestOpenAIImagesComplexityRequiresNativeCapability(t *testing.T) {
	for _, form := range []bool{false, true} {
		t.Run(fmt.Sprintf("multipart_%t", form), func(t *testing.T) {
			body := []byte(`{"prompt":"draw a lantern","complexity":"high"}`)
			contentType := "application/json"
			if form {
				var buf bytes.Buffer
				w := multipart.NewWriter(&buf)
				require.NoError(t, w.WriteField("prompt", "draw a lantern"))
				require.NoError(t, w.WriteField("complexity", "high"))
				require.NoError(t, w.Close())
				body, contentType = buf.Bytes(), w.FormDataContentType()
			}
			c, _ := newOpenAIImagesTestContext(t, body)
			c.Request.Header.Set("Content-Type", contentType)
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			require.Equal(t, "high", parsed.Complexity)
			require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
			payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-sunburst")
			require.NoError(t, err)
			require.Equal(t, "high", gjson.GetBytes(payload, "complexity").String())
			require.False(t, gjson.GetBytes(payload, "quality").Exists(), "complexity must not synthesize a quality field")
		})
	}
}

func TestOpenAIImagesComplexityErrorDoesNotDropOptionsOnFallback(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			const rejected = `{"error":{"type":"invalid_request_error","code":"native_images_unavailable","message":"Images endpoint unavailable"}}`
			upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				require.Equal(t, "/backend-api/codex/images/generations", req.URL.Path)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, "high", gjson.GetBytes(body, "complexity").String())
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(rejected))}, nil
			}}
			body := []byte(`{"model":"gpt-image-2.6-flare","prompt":"draw a lantern","complexity":"high"}`)
			c, rec := newOpenAIImagesTestContext(t, body)
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, 1, calls, "do not silently drop complexity via a Responses fallback")
			if status == http.StatusMethodNotAllowed {
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Equal(t, status, failover.StatusCode)
				require.JSONEq(t, rejected, string(failover.ResponseBody))
			} else {
				require.Equal(t, status, rec.Code)
				require.JSONEq(t, rejected, rec.Body.String())
			}
		})
	}
}

func TestOpenAIImagesLegacyFallbackKeepsComplexityAndOutputFormat(t *testing.T) {
	for _, outputFormat := range []string{"png", "gif", "svg"} {
		t.Run(outputFormat, func(t *testing.T) {
			calls := 0
			upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				if calls == 1 {
					require.Equal(t, "/backend-api/codex/images/generations", req.URL.Path)
					require.Equal(t, "low", gjson.GetBytes(body, "complexity").String())
					return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Images endpoint unavailable"}}`))}, nil
				}
				require.Equal(t, "/backend-api/codex/responses", req.URL.Path)
				require.Equal(t, "low", gjson.GetBytes(body, "tools.0.complexity").String())
				require.Equal(t, "high", gjson.GetBytes(body, "tools.0.quality").String())
				require.Equal(t, outputFormat, gjson.GetBytes(body, "tools.0.output_format").String())
				require.Equal(t, "medium", gjson.GetBytes(body, "reasoning.effort").String())
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}]}}\n\n"))}, nil
			}}
			body := []byte(fmt.Sprintf(`{"model":"gpt-image-2.5-flare","prompt":"draw","complexity":"low","quality":"high","output_format":%q}`, outputFormat))
			c, _ := newOpenAIImagesTestContext(t, body)
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.Equal(t, 1, result.ImageCount)
		})
	}
}
