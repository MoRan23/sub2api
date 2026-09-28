package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIExcelImagesValidationAndPayload(t *testing.T) {
	parsed := &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "draw a pear", N: 2, Size: "1280x720"}
	body, target, err := buildOpenAIExcelImagePayload(parsed, parsed.Model)
	require.NoError(t, err)
	require.Equal(t, openAIExcelAPIBase+"/images/generations", target)
	require.Equal(t, "png", gjson.GetBytes(body, "output_format").String())
	require.False(t, gjson.GetBytes(body, "stream").Exists())
	parsed.Background = "transparent"
	_, _, err = buildOpenAIExcelImagePayload(parsed, parsed.Model)
	require.Error(t, err)
	parsed.Background = ""
	parsed.N = 4
	_, _, err = buildOpenAIExcelImagePayload(parsed, parsed.Model)
	require.Error(t, err)
	parsed.N = 1
	_, _, err = buildOpenAIExcelImagePayload(parsed, "gpt-image-future")
	require.Error(t, err)
}

func TestOpenAIExcelImageEditsMultipart(t *testing.T) {
	image := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, make([]byte, 40)...)
	payload := `{"model":"gpt-image-2","prompt":"edit","images":[{"image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(image) + `"}]}`
	req, err := http.NewRequest(http.MethodPost, openAIExcelAPIBase+"/images/edits", strings.NewReader(payload))
	require.NoError(t, err)
	svc := &OpenAIGatewayService{}
	req, err = svc.prepareOpenAIExcelImageRequest(req, &Account{})
	require.NoError(t, err)
	require.True(t, isOpenAIExcelImageRequest(req.Context()))
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)
	reader := multipart.NewReader(req.Body, params["boundary"])
	found := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		if part.FormName() == "image" {
			found = true
			require.Equal(t, image, data)
			require.Equal(t, "image/png", part.Header.Get("Content-Type"))
		}
	}
	require.True(t, found)
}

func TestOpenAIExcelImageStreamRequiresRealData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{"data":[]}`, `{"error":{"message":"failed"}}`} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		svc := &OpenAIGatewayService{}
		_, count, _, _, err := svc.handleOpenAIExcelImagesStream(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, c, time.Now(), &OpenAIImagesRequest{N: 3})
		require.Error(t, err)
		require.Zero(t, count)
		require.NotContains(t, w.Body.String(), ".completed")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	svc := &OpenAIGatewayService{}
	_, count, _, _, err := svc.handleOpenAIExcelImagesStream(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"cGljdHVyZQ==","model":"gpt-image-2"}],"usage":{"input_tokens":3,"output_tokens":4}}`))}, c, time.Now(), &OpenAIImagesRequest{Model: "gpt-image-2", N: 1})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Contains(t, w.Body.String(), "image_generation.completed")
	require.NotContains(t, w.Body.String(), "partial_image")
}

func TestOpenAIExcelUnknownAttachmentIsRejectedBeforeInference(t *testing.T) {
	store, _ := excelStateFixture()
	svc := &OpenAIGatewayService{excelState: store}
	req, err := http.NewRequestWithContext(context.Background(), "POST", openAIExcelAPIBase+"/responses", bytes.NewBufferString(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"another-accounts-file"}]}]}`))
	require.NoError(t, err)
	_, err = svc.prepareOpenAIExcelAttachments(req, "", &Account{}, "scope")
	require.ErrorContains(t, err, "does not belong")
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		req, err := http.NewRequest("POST", openAIExcelAPIBase+"/responses", strings.NewReader(`{"input":[{"type":"`+kind+`","output":[{"type":"input_image","file_id":"another-accounts-file"}]}]}`))
		require.NoError(t, err)
		_, err = svc.prepareOpenAIExcelAttachments(req, "", &Account{}, "scope")
		require.ErrorContains(t, err, "does not belong")
	}
}

func TestOpenAIExcelImagesActualEndpointAndResultCount(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"draw","n":3,"stream":%t}`, stream))
			c, rec := newOpenAIImagesTestContext(t, body)
			upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
			svc := newOpenAIImagesTestService(upstream)
			account := excelTransportAccount()
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			require.NoError(t, err, rec.Body.String())
			require.Len(t, upstream.requests, 1)
			require.Equal(t, openAIExcelAPIBase+"/images/generations", upstream.lastReq.URL.String())
			require.Equal(t, "bps.openai.com", upstream.lastReq.Host)
			require.Equal(t, "Bearer synthetic-token", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, "application/json", upstream.lastReq.Header.Get("Accept"))
			require.Equal(t, openAIExcelUserAgent, upstream.lastReq.UserAgent())
			require.Equal(t, int64(3), gjson.GetBytes(upstream.lastBody, "n").Int())
			require.False(t, gjson.GetBytes(upstream.lastBody, "tools").Exists())
			require.Equal(t, 1, result.ImageCount, "billing must use returned images, not requested n")
			if stream {
				require.Contains(t, rec.Body.String(), "image_generation.completed")
				require.NotContains(t, rec.Body.String(), "partial_image")
			}
		})
	}
	for _, status := range []int{404, 405} {
		body := []byte(`{"model":"gpt-image-2","prompt":"draw"}`)
		c, _ := newOpenAIImagesTestContext(t, body)
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"not available"}}`))}}
		svc := newOpenAIImagesTestService(upstream)
		parsed, err := svc.ParseOpenAIImagesRequest(c, body)
		require.NoError(t, err)
		_, err = svc.ForwardImages(context.Background(), c, excelTransportAccount(), body, parsed, "")
		require.Error(t, err)
		require.Len(t, upstream.requests, 1, "Excel cannot fall back to Codex")
	}
}

func TestOpenAIExcelAttachmentUploadAndReuse(t *testing.T) {
	image := append([]byte{137, 80, 78, 71, 13, 10, 26, 10}, make([]byte, 40)...)
	calls := 0
	upstream := &codexModelsHTTPUpstreamStub{do: func(req *http.Request, proxy string, accountID int64, _ int) (*http.Response, error) {
		calls++
		require.Equal(t, openAIExcelAPIBase+"/attachments", req.URL.String())
		require.Equal(t, "http://test-proxy.invalid:8080", proxy)
		require.Equal(t, int64(71), accountID)
		require.Equal(t, "Bearer synthetic-token", req.Header.Get("Authorization"))
		require.Equal(t, "synthetic-account", req.Header.Get("ChatGPT-Account-ID"))
		require.Equal(t, openAIExcelUserAgent, req.UserAgent())
		require.NoError(t, req.ParseMultipartForm(1<<20))
		defer func() { _ = req.MultipartForm.RemoveAll() }()
		file, _, err := req.FormFile("file")
		require.NoError(t, err)
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(file)
		require.NoError(t, err)
		require.Equal(t, image, data)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"openai_file_id":"file-excel-test"}`))}, nil
	}}
	store, backend := excelStateFixture()
	svc := &OpenAIGatewayService{httpUpstream: upstream, excelState: store}
	body := `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(image) + `"}]}]}`
	for i := 0; i < 4; i++ {
		if i == 2 {
			// A different bounded index can evict the ownership proof first.
			backend.mu.Lock()
			delete(backend.values, excelStateDigest("same-session|attachment_id")+excelStateDigest("file-excel-test"))
			backend.mu.Unlock()
		}
		req, err := http.NewRequest("POST", openAIExcelAPIBase+"/responses", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer synthetic-token")
		req.Header.Set("ChatGPT-Account-ID", "synthetic-account")
		req, err = svc.prepareOpenAIExcelAttachments(req, "http://test-proxy.invalid:8080", excelTransportAccount(), "same-session")
		require.NoError(t, err)
		data, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, "file-excel-test", gjson.GetBytes(data, "input.0.content.0.file_id").String())
		require.False(t, gjson.GetBytes(data, "input.0.content.0.image_url").Exists())
	}
	require.Equal(t, 2, calls, "reuse verified entries; re-upload an entry whose ownership proof was evicted")
}

func TestOpenAIExcelImageWorkLimitsBeforeAnyUpload(t *testing.T) {
	parts := make([]any, openAIExcelImageInputLimit+1)
	for i := range parts {
		parts[i] = map[string]any{"type": "input_image", "image_url": "https://example.invalid/image.png"}
	}
	for _, edits := range []bool{false, true} {
		payload := map[string]any{"input": []any{map[string]any{"role": "user", "content": parts}}}
		endpoint := "/responses"
		if edits {
			payload = map[string]any{"images": parts}
			endpoint = "/images/edits"
		}
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		req, err := http.NewRequest("POST", openAIExcelAPIBase+endpoint, bytes.NewReader(body))
		require.NoError(t, err)
		svc := &OpenAIGatewayService{}
		if edits {
			_, err = svc.prepareOpenAIExcelImageRequest(req, excelTransportAccount())
		} else {
			_, err = svc.prepareOpenAIExcelAttachments(req, "", excelTransportAccount(), "scope")
		}
		require.ErrorContains(t, err, "too many")
	}
	buffer := &excelImageBuffer{Buffer: *bytes.NewBuffer(make([]byte, openAIExcelImageWorkBytes-1))}
	_, err := buffer.Write([]byte("xx"))
	require.ErrorContains(t, err, "total size")
	require.Equal(t, openAIExcelImageWorkBytes-1, buffer.Len())
}
