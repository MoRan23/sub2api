package service

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const backfillSVGDocument = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="128" height="64"><title>A &amp; B</title><path d="M0 0h64v32H0z" fill="#123456"/></svg>`

func TestBackfillOpenAIImagesB64JSON_SVGOptIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parsed *OpenAIImagesRequest
		want   bool
	}{
		{"no request", nil, false},
		{"format omitted", &OpenAIImagesRequest{}, false},
		{"png requested", &OpenAIImagesRequest{OutputFormat: "png"}, false},
		{"svg requested", &OpenAIImagesRequest{OutputFormat: "svg"}, true},
		{"case normalized", &OpenAIImagesRequest{OutputFormat: " SVG "}, true},
		{"url requested", &OpenAIImagesRequest{OutputFormat: "svg", ResponseFormat: "url"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "application/octet-stream", []byte(backfillSVGDocument))}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"data":[{"url":"https://cdn.example.com/icon.svg"}]}`)
			got := svc.backfillOpenAIImagesB64JSON(context.Background(), b64BackfillAccount(true), tc.parsed, body)
			if !tc.want {
				require.Equal(t, body, got)
				return
			}
			require.Equal(t, base64.StdEncoding.EncodeToString([]byte(backfillSVGDocument)), gjson.GetBytes(got, "data.0.b64_json").String())
			require.Equal(t, "https://cdn.example.com/icon.svg", gjson.GetBytes(got, "data.0.url").String())
			require.True(t, HTTPUpstreamPublicHostsOnly(upstream.lastReq.Context()))
		})
	}
}

func TestIsBackfillSVGContent(t *testing.T) {
	for _, document := range []string{
		backfillSVGDocument,
		"\xef\xbb\xbf" + backfillSVGDocument,
		" \n<!-- logo -->" + `<svg xmlns="http://www.w3.org/2000/svg"/>` + "\n<!-- done -->",
		`<v:svg xmlns:v="http://www.w3.org/2000/svg"><v:path d="M0 0h2"/></v:svg>`,
	} {
		require.True(t, isBackfillSVGContent([]byte(document)), document)
	}
	for _, document := range []string{
		"",
		`<html><body>error</body></html>`,
		`<html><svg xmlns="http://www.w3.org/2000/svg"/></html>`,
		`<svg/>`,
		`<svg xmlns="https://example.com/not-svg"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg">`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`,
		`error <svg xmlns="http://www.w3.org/2000/svg"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg"/>error`,
		`<!DOCTYPE svg [<!ENTITY remote SYSTEM "https://example.com/file">]><svg xmlns="http://www.w3.org/2000/svg">&remote;</svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg">&unknown;</svg>`,
	} {
		require.False(t, isBackfillSVGContent([]byte(document)), document)
	}
}

func TestBackfillOpenAIImagesB64JSON_SVGRejectsNonSVGBytes(t *testing.T) {
	for _, content := range []string{
		`<html>login required</html>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path>`,
		`{"error":"unauthorized"}`,
	} {
		upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "image/svg+xml", []byte(content))}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		body := []byte(`{"data":[{"url":"https://cdn.example.com/icon.svg"}]}`)
		got := svc.backfillOpenAIImagesB64JSON(context.Background(), b64BackfillAccount(true), &OpenAIImagesRequest{OutputFormat: "svg"}, body)
		require.Equal(t, body, got)
	}
}

func TestOpenAIImagesSVGAPIKeyBackfill(t *testing.T) {
	body := []byte(`{"model":"gpt-image-2.6-flare","prompt":"draw a logo","output_format":"svg"}`)
	c, recorder := newOpenAIImagesTestContext(t, body)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"url":"https://cdn.example.com/logo.svg"}],"usage":{"input_tokens":10,"output_tokens":20}}`)),
		},
		b64BackfillImageResponse(http.StatusOK, "image/svg+xml", []byte(backfillSVGDocument)),
	}}
	svc := newOpenAIImagesTestService(upstream)
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	result, err := svc.ForwardImages(context.Background(), c, b64BackfillAccount(true), body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, 20, result.Usage.OutputTokens)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte(backfillSVGDocument)), gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
}

func TestFetchOpenAIImageURLBase64_SVGPreservesLegacyOptOut(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "image/svg+xml", []byte(backfillSVGDocument))}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	_, err := svc.fetchOpenAIImageURLBase64(context.Background(), b64BackfillAccount(true), "https://cdn.example.com/logo.svg")
	require.ErrorContains(t, err, "content is not an allowed image format")
}
