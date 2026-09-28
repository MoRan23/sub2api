package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIExcelAPIBase         = "https://bps.openai.com/basispoints/api"
	openAIExcelImageInputLimit = 64
	openAIExcelImageWorkBytes  = 64 << 20
)

// Limits multipart output while it is built, including fields and boundaries.
type excelImageBuffer struct{ bytes.Buffer }

func (b *excelImageBuffer) Write(p []byte) (int, error) {
	if len(p) > openAIExcelImageWorkBytes-b.Len() {
		return 0, errors.New("excel image request exceeds total size limit")
	}
	return b.Buffer.Write(p)
}

type openAIExcelImageRequestKey struct{}

func isOpenAIExcelImageRequest(ctx context.Context) bool {
	marked, _ := ctx.Value(openAIExcelImageRequestKey{}).(bool)
	return marked
}

func buildOpenAIExcelImagePayload(parsed *OpenAIImagesRequest, model string) ([]byte, string, error) {
	if parsed == nil {
		return nil, "", errors.New("image request is required")
	}
	if model != "gpt-image-2" {
		return nil, "", errors.New("image model is not supported by Excel upstream")
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		return nil, "", errors.New("image prompt is required")
	}
	if parsed.N > 3 || parsed.N < 0 {
		return nil, "", errors.New("excel images support between one and three images")
	}
	if parsed.MaskUpload != nil || parsed.MaskImageURL != "" {
		return nil, "", errors.New("excel image masks are not supported")
	}
	payload := map[string]any{"model": model, "prompt": parsed.Prompt, "n": max(parsed.N, 1), "output_format": "png"}
	for _, field := range []struct {
		key, value string
		allowed    []string
	}{
		{"size", parsed.Size, []string{"auto", "1024x1024", "1536x1024", "1024x1536", "1280x720"}},
		{"quality", parsed.Quality, []string{"auto", "low", "medium", "high"}},
		{"background", parsed.Background, []string{"auto", "opaque"}},
		{"output_format", parsed.OutputFormat, []string{"png"}},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			if field.key == "output_format" {
				value = "png"
			} else {
				value = "auto"
			}
		}
		if !containsCandyEffort(field.allowed, value) {
			return nil, "", fmt.Errorf("%s is not supported by Excel images", field.key)
		}
		payload[field.key] = value
	}
	endpoint := openAIExcelAPIBase + "/images/generations"
	if parsed.IsEdits() {
		endpoint = openAIExcelAPIBase + "/images/edits"
		if len(parsed.InputImageURLs)+len(parsed.Uploads) > openAIExcelImageInputLimit {
			return nil, "", errors.New("too many Excel image inputs")
		}
		images := make([]map[string]string, 0, len(parsed.InputImageURLs)+len(parsed.Uploads))
		for _, source := range parsed.InputImageURLs {
			images = append(images, map[string]string{"image_url": source})
		}
		for _, upload := range parsed.Uploads {
			value, err := openAIImageUploadToDataURL(upload)
			if err != nil {
				return nil, "", err
			}
			images = append(images, map[string]string{"image_url": value})
		}
		if len(images) == 0 {
			return nil, "", errors.New("image input is required")
		}
		payload["images"] = images
	}
	body, err := json.Marshal(payload)
	return body, endpoint, err
}

func (s *OpenAIGatewayService) excelImageBytes(ctx context.Context, account *Account, source string) ([]byte, string, error) {
	encoded, err := s.fetchOpenAIImageURLBase64(ctx, account, source)
	if err != nil {
		return nil, "", errors.New("excel image input could not be read")
	}
	if len(encoded) > int(openAIImageMaxDownloadBytes)*4/3+8 {
		return nil, "", errors.New("excel image exceeds size limit")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || int64(len(data)) > openAIImageMaxDownloadBytes || !isBackfillImageContent(data) {
		return nil, "", errors.New("excel image input is not a supported image")
	}
	return data, detectedImageContentType(data), nil
}

func writeExcelImagePart(writer *multipart.Writer, name, filename, mime string, data []byte) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, filename))
	header.Set("Content-Type", mime)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func replaceExcelRequestBody(req *http.Request, body []byte, contentType string) *http.Request {
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	out.ContentLength = int64(len(body))
	out.Header.Set("Content-Type", contentType)
	out.Header.Del("Content-Length")
	return out
}

func (s *OpenAIGatewayService) prepareOpenAIExcelImageRequest(req *http.Request, account *Account) (*http.Request, error) {
	body, err := requestReplayBytes(req)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(context.WithValue(req.Context(), openAIExcelImageRequestKey{}, true))
	if !strings.HasSuffix(req.URL.Path, "/images/edits") {
		return req, nil
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("invalid Excel image request")
	}
	images, ok := payload["images"].([]any)
	if !ok || len(images) == 0 {
		return nil, errors.New("image input is required")
	}
	if len(images) > openAIExcelImageInputLimit {
		return nil, errors.New("too many Excel image inputs")
	}
	var output excelImageBuffer
	form := multipart.NewWriter(&output)
	for _, key := range []string{"model", "prompt", "size", "quality", "background", "output_format", "n"} {
		if value, ok := payload[key]; ok {
			if err := form.WriteField(key, fmt.Sprint(value)); err != nil {
				return nil, err
			}
		}
	}
	name := "image"
	if len(images) > 1 {
		name = "image[]"
	}
	for i, image := range images {
		item, ok := image.(map[string]any)
		if !ok {
			return nil, errors.New("invalid image input")
		}
		source, _ := item["image_url"].(string)
		data, mime, err := s.excelImageBytes(req.Context(), account, source)
		if err != nil {
			return nil, err
		}
		if err := writeExcelImagePart(form, name, "picture-"+strconv.Itoa(i)+".img", mime, data); err != nil {
			return nil, err
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	return replaceExcelRequestBody(req, output.Bytes(), form.FormDataContentType()), nil
}

// Text request images in messages must be uploaded before the one inference
// send. Tool-output images remain inline, as accepted by the Excel protocol.
func (s *OpenAIGatewayService) prepareOpenAIExcelAttachments(req *http.Request, proxyURL string, account *Account, scope string) (*http.Request, error) {
	body, err := requestReplayBytes(req)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return nil, errors.New("invalid Excel request")
	}
	items, _ := root["input"].([]any)
	// Bound work before uploading anything. URL inputs can be very small in the
	// request while expanding into many large remote images.
	imageCount := 0
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		for _, field := range []string{"content", "output"} {
			parts, _ := item[field].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if part["type"] == "input_image" {
					imageCount++
				}
			}
		}
	}
	if imageCount > openAIExcelImageInputLimit {
		return nil, errors.New("too many Excel image inputs")
	}
	decodedBytes := 0
	changed := false
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := item["type"].(string)
		content, _ := item["content"].([]any)
		toolOutput := kind == "function_call_output" || kind == "custom_tool_call_output"
		if toolOutput {
			if output, ok := item["output"].([]any); ok {
				content = append(append([]any(nil), content...), output...)
			}
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if ok && part["type"] == "input_file" {
				return nil, errors.New("excel upstream does not support document attachments")
			}
			if !ok || part["type"] != "input_image" {
				continue
			}
			if id, _ := part["file_id"].(string); id != "" {
				if part["image_url"] != nil {
					return nil, errors.New("excel image cannot specify both URL and file ID")
				}
				if _, err := s.excelState.get(req.Context(), scope, "attachment_id", id); err != nil {
					return nil, errors.New("excel attachment does not belong to this session")
				}
				continue
			}
			source, _ := part["image_url"].(string)
			data, mime, err := s.excelImageBytes(req.Context(), account, source)
			if err != nil {
				return nil, err
			}
			decodedBytes += len(data)
			if decodedBytes > openAIExcelImageWorkBytes {
				return nil, errors.New("excel image inputs exceed total size limit")
			}
			if toolOutput {
				part["image_url"] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
				changed = true
				continue
			}
			id, err := s.uploadOpenAIExcelAttachment(req, proxyURL, account, scope, data, mime)
			if err != nil {
				return nil, err
			}
			delete(part, "image_url")
			part["file_id"] = id
			changed = true
		}
	}
	if !changed {
		return req, nil
	}
	body, err = json.Marshal(root)
	if err != nil {
		return nil, err
	}
	if len(body) > openAIExcelImageWorkBytes {
		return nil, errors.New("excel image request exceeds total size limit")
	}
	return replaceExcelRequestBody(req, body, "application/json"), nil
}

func (s *OpenAIGatewayService) uploadOpenAIExcelAttachment(original *http.Request, proxyURL string, account *Account, scope string, data []byte, mime string) (string, error) {
	digest := excelStateDigest(string(data))
	if cached, err := s.excelState.get(original.Context(), scope, "attachment", digest); err == nil {
		if id := gjson.GetBytes(cached, "file_id").String(); id != "" {
			if _, ownerErr := s.excelState.get(original.Context(), scope, "attachment_id", id); ownerErr == nil {
				return id, nil
			} else if !errors.Is(ownerErr, ErrOpenAIExcelStateNotFound) {
				return "", ownerErr
			}
			// The two bounded indexes can evict independently. Re-upload rather
			// than hand out an ID whose ownership can no longer be proved.
		}
	} else if !errors.Is(err, ErrOpenAIExcelStateNotFound) {
		return "", err
	}
	var output bytes.Buffer
	form := multipart.NewWriter(&output)
	if err := writeExcelImagePart(form, "file", "picture.img", mime, data); err != nil {
		return "", err
	}
	if err := form.Close(); err != nil {
		return "", err
	}
	upload, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(original.Context()), http.MethodPost, openAIExcelAPIBase+"/attachments", bytes.NewReader(output.Bytes()))
	if err != nil {
		return "", err
	}
	upload.Header, err = PrepareOpenAIExcelHeaders(original.Header)
	if err != nil {
		return "", err
	}
	upload.Header.Set("Content-Type", form.FormDataContentType())
	upload.Header.Set("Accept", "application/json")
	upload = withOpenAINativeHTTPRequestScope(upload, account, s.accountRepo, "excel_attachment")
	resp, err := s.httpUpstream.Do(upload, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", errors.New("excel attachment upload failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("excel attachment upload returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return "", errors.New("invalid Excel attachment response")
	}
	id := gjson.GetBytes(body, "openai_file_id").String()
	if id == "" || len(id) > 256 {
		return "", errors.New("excel attachment response has no file ID")
	}
	payload, _ := json.Marshal(map[string]string{"file_id": id, "mime": mime})
	if err := s.excelState.put(original.Context(), scope, "attachment_id", id, json.RawMessage(`{"observed":true}`), openAIExcelAttachmentTTL, 256); err != nil {
		return "", err
	}
	if err := s.excelState.put(original.Context(), scope, "attachment", digest, payload, openAIExcelAttachmentTTL, 256); err != nil {
		return "", err
	}
	return id, nil
}

func (s *OpenAIGatewayService) handleOpenAIExcelImagesStream(resp *http.Response, c *gin.Context, start time.Time, parsed *OpenAIImagesRequest) (OpenAIUsage, int, []string, *int, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return OpenAIUsage{}, 0, nil, nil, err
	}
	results, err := parseCodexDirectImagesResponse(body)
	if err != nil {
		return OpenAIUsage{}, 0, nil, nil, err
	}
	usage, _ := codexDirectImagesUsage(body)
	if observer := upstreamResponseModelObserverFromContext(c); observer != nil {
		observer.Observe(gjson.GetBytes(body, "model").String(), true)
		for _, result := range results {
			observer.Observe(result.Model, true)
		}
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Status(resp.StatusCode)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return usage, 0, nil, nil, errors.New("streaming unavailable")
	}
	count := 0
	for i, result := range results {
		event := map[string]any{"type": openAIImagesStreamPrefix(parsed) + ".completed", "b64_json": result.Result, "output_format": "png", "size": result.Size, "model": parsed.Model}
		if i == len(results)-1 {
			if raw := gjson.GetBytes(body, "usage"); raw.Exists() {
				event["usage"] = json.RawMessage(raw.Raw)
			}
		}
		if parsed.ResponseFormat == "url" {
			event["url"] = "data:image/png;base64," + result.Result
		}
		payload, _ := json.Marshal(event)
		if err := s.writeOpenAIImagesStreamEvent(c, flusher, openAIImagesStreamPrefix(parsed)+".completed", payload); err != nil {
			return usage, count, openAIResponsesImageResultSizes(results[:count]), nil, err
		}
		count++
	}
	elapsed := int(time.Since(start).Milliseconds())
	return usage, count, openAIResponsesImageResultSizes(results), &elapsed, nil
}

func setOpenAIExcelImageTarget(req *http.Request, target string) error {
	parsed, err := url.Parse(target)
	if err != nil {
		return err
	}
	req.URL = parsed
	req.Host = parsed.Host
	req.Header.Del("OpenAI-Beta")
	req.Header.Del(responsesLiteHeaderKey)
	req.Header.Set("Accept", "application/json")
	return nil
}

// Converts public image URLs using the existing restricted downloader. It
// never forwards OAuth credentials to the returned URL.
func (s *OpenAIGatewayService) backfillOpenAIExcelImageURLs(ctx context.Context, account *Account, body []byte) ([]byte, error) {
	for i, item := range gjson.GetBytes(body, "data").Array() {
		if item.Get("b64_json").String() != "" {
			continue
		}
		if source := item.Get("url").String(); source != "" {
			value, err := s.fetchOpenAIImageURLBase64(ctx, account, source)
			if err != nil {
				return nil, errors.New("excel image result could not be retrieved")
			}
			body, err = sjson.SetBytes(body, fmt.Sprintf("data.%d.b64_json", i), value)
			if err != nil {
				return nil, err
			}
		}
	}
	return body, nil
}
