package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Use a complete animation, rather than a GIF header or static image, so the
// forwarding assertions detect accidental first-frame decoding/re-encoding.
func encodeOpenAIImagesTestAnimation(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 16, 32), palette)
	second := image.NewPaletted(first.Rect, palette)
	second.SetColorIndex(7, 9, 1)
	animation := &gif.GIF{
		Image:     []*image.Paletted{first, second},
		Delay:     []int{20, 20},
		LoopCount: 0,
		Disposal:  []byte{gif.DisposalNone, gif.DisposalBackground},
	}
	var encoded bytes.Buffer
	require.NoError(t, gif.EncodeAll(&encoded, animation))
	return encoded.Bytes()
}

func TestOpenAIImagesGIFForwarding(t *testing.T) {
	animation := encodeOpenAIImagesTestAnimation(t)
	encoded := base64.StdEncoding.EncodeToString(animation)
	// GIF is not version-gated by the gateway. Exercise the existing native
	// Images routes as well as 2.6; each upstream decides actual availability.
	for _, model := range []string{"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
			for _, explicitFrames := range []bool{false, true} {
				formats := []string{"b64_json"}
				if accountType == AccountTypeOAuth {
					formats = append(formats, "url")
				}
				for _, responseFormat := range formats {
					name := fmt.Sprintf("%s/%s/explicit_frames_%t/%s", model, accountType, explicitFrames, responseFormat)
					t.Run(name, func(t *testing.T) {
						payload := map[string]any{
							"model": "gpt-image-1", "prompt": "  a looping lantern  ",
							"output_format": "gif", "response_format": responseFormat, "n": 2,
						}
						if explicitFrames {
							payload["frame_count"] = 2
							payload["frame_duration_ms"] = 203 // The upstream performs 10 ms rounding.
						}
						body, err := json.Marshal(payload)
						require.NoError(t, err)
						c, rec := newOpenAIImagesTestContext(t, body)
						responseBody := fmt.Sprintf(`{"created":1710000000,"model":%q,"output_format":"gif","size":"auto","frame_count":2,"data":[{"b64_json":%q,"frame_duration_ms":200},{"b64_json":%q,"frame_duration_ms":200}],"usage":{"input_tokens":12,"output_tokens":24,"output_tokens_details":{"image_tokens":24}}}`, model, encoded, encoded)
						upstream := &httpUpstreamRecorder{resp: &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {"application/json"}},
							Body:       io.NopCloser(strings.NewReader(responseBody)),
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
						// Exercise channel mapping and account mapping before selecting the
						// route. The original request model is not in the native allowlist.
						account.Credentials["model_mapping"] = map[string]any{"gpt-image-2": model}
						result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "gpt-image-2")
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Equal(t, http.StatusOK, rec.Code)
						require.Equal(t, model, result.UpstreamModel)
						require.Equal(t, wantPath, upstream.lastReq.URL.Path)
						require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
						require.Equal(t, "  a looping lantern  ", gjson.GetBytes(upstream.lastBody, "prompt").String())
						require.Equal(t, "gif", gjson.GetBytes(upstream.lastBody, "output_format").String())
						for _, field := range []string{"frame_count", "frame_duration_ms"} {
							require.Equal(t, explicitFrames, gjson.GetBytes(upstream.lastBody, field).Exists(), field)
							if explicitFrames {
								require.Equal(t, gjson.GetBytes(body, field).Value(), gjson.GetBytes(upstream.lastBody, field).Value(), field)
							}
						}
						require.False(t, gjson.GetBytes(upstream.lastBody, "stream").Exists())
						require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
						require.Equal(t, 2, result.ImageCount, "two animations are two outputs, not four frames")
						require.Equal(t, 12, result.Usage.InputTokens)
						require.Equal(t, 24, result.Usage.OutputTokens)
						require.Equal(t, 24, result.Usage.ImageOutputTokens)
						require.EqualValues(t, 2, gjson.GetBytes(rec.Body.Bytes(), "frame_count").Int())
						items := gjson.GetBytes(rec.Body.Bytes(), "data").Array()
						require.Len(t, items, 2)
						for _, item := range items {
							value := item.Get("b64_json").String()
							if responseFormat == "url" {
								value = item.Get("url").String()
								require.True(t, strings.HasPrefix(value, "data:image/gif;base64,"))
								value = strings.TrimPrefix(value, "data:image/gif;base64,")
								require.False(t, item.Get("b64_json").Exists())
							}
							require.Equal(t, encoded, value, "the complete encoded animation must be preserved")
							decoded, err := base64.StdEncoding.DecodeString(value)
							require.NoError(t, err)
							require.Equal(t, animation, decoded)
							gifResult, err := gif.DecodeAll(bytes.NewReader(decoded))
							require.NoError(t, err)
							require.Len(t, gifResult.Image, 2)
							require.Equal(t, []int{20, 20}, gifResult.Delay)
							require.EqualValues(t, 200, item.Get("frame_duration_ms").Int())
							if accountType == AccountTypeOAuth {
								require.Equal(t, "16x32", item.Get("size").String())
							}
						}
						if accountType == AccountTypeOAuth {
							require.Equal(t, "16x32", gjson.GetBytes(rec.Body.Bytes(), "size").String())
							require.Equal(t, []string{"16x32", "16x32"}, result.ImageOutputSizes)
						}
					})
				}
			}
		}
	}
}

func TestOpenAIImagesGIFErrorDoesNotFallbackToResponses(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed} {
			t.Run(fmt.Sprintf("%s/%d", model, status), func(t *testing.T) {
				calls := 0
				upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
					calls++
					require.Equal(t, "/backend-api/codex/images/generations", req.URL.Path)
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_gif_rejected"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"gif_not_supported","message":"GIF generation is unavailable on this endpoint","param":"output_format"}}`)),
					}, nil
				}}
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"a looping lantern","output_format":"gif","frame_count":2,"frame_duration_ms":200}`, model))
				c, rec := newOpenAIImagesTestContext(t, body)
				svc := newOpenAIImagesTestService(upstream)
				parsed, err := svc.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, 1, calls, "GIF must not be silently replaced with a Responses image tool request")
				if status == http.StatusMethodNotAllowed {
					// Existing account failover policy handles 405 at the outer
					// handler; the image adapter must retain that error, without
					// switching to a Responses request or writing a success.
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.Equal(t, status, failover.StatusCode)
					require.Equal(t, "req_gif_rejected", failover.ResponseHeaders.Get("x-request-id"))
					require.JSONEq(t, `{"error":{"type":"invalid_request_error","code":"gif_not_supported","message":"GIF generation is unavailable on this endpoint","param":"output_format"}}`, string(failover.ResponseBody))
					require.False(t, c.Writer.Written())
					return
				}
				require.Equal(t, status, rec.Code)
				require.Equal(t, "gif_not_supported", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
				require.Equal(t, "GIF generation is unavailable on this endpoint", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
				require.Equal(t, "output_format", gjson.GetBytes(rec.Body.Bytes(), "error.param").String())
			})
		}
	}
}

func TestOpenAIImagesGIFDimensionsAndMIME(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(encodeOpenAIImagesTestAnimation(t))
	for _, value := range []string{encoded, strings.TrimRight(encoded, "="), "data:image/gif;base64," + encoded} {
		require.Equal(t, "16x32", detectOpenAIImageResultSize(value))
	}
	for _, format := range []string{"gif", "GIF", " gif ", "image/gif"} {
		require.Equal(t, "image/gif", openAIImageOutputMIMEType(format))
	}
}
