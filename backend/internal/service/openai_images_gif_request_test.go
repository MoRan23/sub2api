package service

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesGIFRequestJSON(t *testing.T) {
	for _, model := range []string{"gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		for _, controls := range []struct {
			count, duration int
		}{{2, 20}, {64, 2000}, {9, 205}} {
			t.Run(fmt.Sprintf("%s/%d/%d", model, controls.count, controls.duration), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"animate a lantern","output_format":"gif","frame_count":%d,"frame_duration_ms":%d,"size":"auto"}`, model, controls.count, controls.duration))
				original := bytes.Clone(body)
				c, _ := newOpenAIImagesTestContext(t, body)
				parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, body)
				require.NoError(t, err)
				require.True(t, parsed.IsGIF())
				require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
				require.Equal(t, controls.count, *parsed.FrameCount)
				require.Equal(t, controls.duration, *parsed.FrameDurationMS)
				payload, endpoint, err := buildOpenAIImagesOAuthPayload(parsed, model)
				require.NoError(t, err)
				require.Contains(t, endpoint, "/images/generations")
				require.EqualValues(t, controls.count, gjson.GetBytes(payload, "frame_count").Int())
				require.EqualValues(t, controls.duration, gjson.GetBytes(payload, "frame_duration_ms").Int(), "leave duration rounding to upstream")
				require.Equal(t, "gif", gjson.GetBytes(payload, "output_format").String())
				require.Equal(t, original, body)
			})
		}
	}
}

func TestOpenAIImagesGIFRequestDefaults(t *testing.T) {
	body := []byte(`{"prompt":"animate a lantern","output_format":"gif"}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.Equal(t, OpenAIImagesCapabilityNative, parsed.RequiredCapability)
	require.Nil(t, parsed.FrameCount)
	require.Nil(t, parsed.FrameDurationMS)
	payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(payload, "frame_count").Exists())
	require.False(t, gjson.GetBytes(payload, "frame_duration_ms").Exists())
}

func TestOpenAIImagesGIFRequestRejectsInvalidControlTypes(t *testing.T) {
	for _, tc := range []struct {
		name, extra, errorText string
	}{
		{"count fraction", `"frame_count":2.5`, "frame_count"},
		{"count string", `"frame_count":"9"`, "frame_count"},
		{"count boolean", `"frame_count":true`, "frame_count"},
		{"count array", `"frame_count":[]`, "frame_count"},
		{"count object", `"frame_count":{}`, "frame_count"},
		{"count overflow", `"frame_count":999999999999999999999999999999`, "frame_count"},
		{"duration fraction", `"frame_duration_ms":200.5`, "frame_duration_ms"},
		{"duration string", `"frame_duration_ms":"200"`, "frame_duration_ms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2.6-sunburst","prompt":"animate","output_format":"gif",` + tc.extra + `}`)
			c, _ := newOpenAIImagesTestContext(t, body)
			_, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
			require.ErrorContains(t, err, tc.errorText)
		})
	}
}

func TestOpenAIImagesFrameControlsDoNotRequireGIF(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-image-2.6-flare","prompt":"draw","output_format":"png","frame_count":9}`,
		`{"model":"gpt-image-2.6-flare","prompt":"draw","frame_duration_ms":200}`,
	} {
		c, _ := newOpenAIImagesTestContext(t, []byte(body))
		parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRoutingRequest(c, []byte(body))
		require.NoError(t, err)
		payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
		require.NoError(t, err)
		for _, field := range []string{"frame_count", "frame_duration_ms"} {
			require.Equal(t, gjson.Get(body, field).Raw, gjson.GetBytes(payload, field).Raw)
		}
	}
	body := []byte(`{"model":"gpt-image-2.6-flare","prompt":"edit","output_format":"gif","images":[{"image_url":"data:image/png;base64,cG5n"}]}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	c.Request.URL.Path = openAIImagesEditsEndpoint
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	payload, endpoint, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
	require.NoError(t, err)
	require.Contains(t, endpoint, "/images/edits")
	require.Equal(t, "gif", gjson.GetBytes(payload, "output_format").String())
	require.Equal(t, "data:image/png;base64,cG5n", gjson.GetBytes(payload, "images.0.image_url").String())
}

func TestOpenAIImagesGIFRequestMultipart(t *testing.T) {
	for _, tc := range []struct {
		name, field, value, errorText string
	}{
		{"valid", "frame_count", "64", ""},
		{"invalid count", "frame_count", "2.5", "frame_count"},
		{"duration left to upstream", "frame_duration_ms", "2001", ""},
		{"unknown rewriter", "prompt_rewriter", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			for key, value := range map[string]string{"model": "gpt-image-2.6-flare", "prompt": "animate", "output_format": "gif"} {
				require.NoError(t, w.WriteField(key, value))
			}
			require.NoError(t, w.WriteField(tc.field, tc.value))
			require.NoError(t, w.Close())
			c, _ := newOpenAIImagesTestContext(t, body.Bytes())
			c.Request.Header.Set("Content-Type", w.FormDataContentType())
			parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body.Bytes())
			if tc.errorText != "" {
				require.ErrorContains(t, err, tc.errorText)
				return
			}
			require.NoError(t, err)
			payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-flare")
			require.NoError(t, err)
			if tc.field == "prompt_rewriter" {
				require.False(t, gjson.GetBytes(payload, tc.field).Exists(), "OAuth sends the upstream supported option allowlist")
			} else {
				require.Equal(t, tc.value, gjson.GetBytes(payload, tc.field).Raw)
			}
		})
	}
}

func TestOpenAIImagesGIFRequestLeavesRangesToUpstream(t *testing.T) {
	count := 65
	payload, _, err := buildOpenAIImagesOAuthPayload(&OpenAIImagesRequest{
		Prompt: "animate", OutputFormat: "gif", FrameCount: &count,
	}, "gpt-image-2.6-sunburst")
	require.NoError(t, err)
	require.EqualValues(t, 65, gjson.GetBytes(payload, "frame_count").Int())
	require.Equal(t, OpenAIImagesCapabilityNative, classifyOpenAIImagesCapability(&OpenAIImagesRequest{Model: "gpt-image-2.6-flare", N: 1, OutputFormat: "gif"}))
}

func TestOpenAIImagesGIFRequestDoesNotChangePNG(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2.6-sunburst","prompt":"draw","output_format":"png","quality":"high","size":"1024x1024","stream":true,"partial_images":2,"output_compression":80}`)
	c, _ := newOpenAIImagesTestContext(t, body)
	parsed, err := (&OpenAIGatewayService{}).ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	payload, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2.6-sunburst")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(payload, "frame_count").Exists())
	require.False(t, gjson.GetBytes(payload, "frame_duration_ms").Exists())
	require.Equal(t, "high", gjson.GetBytes(payload, "quality").String())
	require.Equal(t, "1024x1024", gjson.GetBytes(payload, "size").String())
	require.True(t, gjson.GetBytes(payload, "stream").Bool())
	require.EqualValues(t, 2, gjson.GetBytes(payload, "partial_images").Int())
	require.EqualValues(t, 80, gjson.GetBytes(payload, "output_compression").Int())
}
