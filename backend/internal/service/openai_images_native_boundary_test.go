package service

import (
	"bytes"
	"context"
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

func TestOpenAIImagesNativeMarkupPreservesTransportBoundaries(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken} {
		for _, form := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/multipart=%t", accountType, form), func(t *testing.T) {
				const prompt = "  edit the transparent image\n"
				body := []byte(fmt.Sprintf(`{"model":"gpt-image-2.6-flare","prompt":%q,"background":"transparent","complexity":"high","images":[{"image_url":"data:image/png;base64,aW1hZ2U="}],"markup":{"image_url":"data:image/png;base64,bWFya3Vw"},"access_programs":{"cyber":"client-choice","future":true}}`, prompt))
				contentType := "application/json"
				if form {
					var buf bytes.Buffer
					writer := multipart.NewWriter(&buf)
					for _, field := range [][2]string{{"model", "gpt-image-2.6-flare"}, {"prompt", prompt}, {"background", "transparent"}, {"complexity", "high"}, {"access_programs", `{"cyber":"client-choice","future":true}`}} {
						require.NoError(t, writer.WriteField(field[0], field[1]))
					}
					for _, name := range []string{"image", "markup"} {
						header := textproto.MIMEHeader{}
						header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, name+".png"))
						header.Set("Content-Type", "image/png")
						part, err := writer.CreatePart(header)
						require.NoError(t, err)
						_, err = io.WriteString(part, name)
						require.NoError(t, err)
					}
					require.NoError(t, writer.Close())
					body, contentType = buf.Bytes(), writer.FormDataContentType()
				}
				original := bytes.Clone(body)
				c, _ := newOpenAIImagesTestContext(t, body)
				c.Request.URL.Path = openAIImagesEditsEndpoint
				c.Request.Header.Set("Content-Type", contentType)
				upstream := &httpUpstreamRecorder{resp: openAIImagesJSONResponse()}
				svc := newOpenAIImagesTestService(upstream)
				svc.settingService = daybreakHTTPSettings(false)
				account := newOpenAIImagesAPIKeyAccount()
				if accountType != AccountTypeAPIKey {
					account = directImagesTestAccount()
					account.Type = accountType
				}
				proxyID := int64(73)
				account.ProxyID = &proxyID
				account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "image-proxy.invalid", Port: 8080}
				parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
				require.NoError(t, err)
				require.Len(t, gjson.GetBytes(parsed.ModerationBody(), "images").Array(), 2, "markup must enter image moderation")
				result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
				require.NoError(t, err)
				require.Equal(t, 1, result.ImageCount)
				require.Equal(t, account.Proxy.URL(), upstream.lastProxyURL)
				require.Equal(t, original, body, "retry source remains immutable")
				if form && accountType == AccountTypeAPIKey {
					_, params, err := mime.ParseMediaType(upstream.lastReq.Header.Get("Content-Type"))
					require.NoError(t, err)
					forwarded, err := multipart.NewReader(bytes.NewReader(upstream.lastBody), params["boundary"]).ReadForm(1 << 20)
					require.NoError(t, err)
					t.Cleanup(func() { _ = forwarded.RemoveAll() })
					require.Equal(t, []string{prompt}, forwarded.Value["prompt"])
					require.Equal(t, []string{"transparent"}, forwarded.Value["background"])
					require.Len(t, forwarded.File["markup"], 1)
					file, err := forwarded.File["markup"][0].Open()
					require.NoError(t, err)
					data, err := io.ReadAll(file)
					require.NoError(t, err)
					require.NoError(t, file.Close())
					require.Equal(t, "markup", string(data))
					programs := gjson.Parse(forwarded.Value["access_programs"][0])
					require.False(t, programs.Get("cyber").Exists())
					require.True(t, programs.Get("future").Bool())
				} else {
					require.Equal(t, prompt, gjson.GetBytes(upstream.lastBody, "prompt").String())
					require.Equal(t, "transparent", gjson.GetBytes(upstream.lastBody, "background").String())
					require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "complexity").String())
					require.True(t, strings.HasSuffix(gjson.GetBytes(upstream.lastBody, "markup.image_url").String(), ";base64,bWFya3Vw"))
					require.False(t, gjson.GetBytes(upstream.lastBody, "access_programs.cyber").Exists())
					if accountType == AccountTypeAPIKey {
						require.True(t, gjson.GetBytes(upstream.lastBody, "access_programs.future").Bool())
					}
				}
			})
		}
	}
}

func TestOpenAIImagesRoutingDefersNativeOptionValidationForEngine(t *testing.T) {
	for _, option := range []string{`"frame_count":"engine-value"`, `"frame_duration_ms":1.5`, `"svg":{"reasoning":"engine-value"}`, `"markup":{"file_id":"file-engine"}`, `"markup":"engine-value"`} {
		t.Run(option, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2.6-flare","prompt":"edit","images":[{"image_url":"data:image/png;base64,AA=="}],` + option + `}`)
			c, _ := newOpenAIImagesTestContext(t, body)
			c.Request.URL.Path = openAIImagesEditsEndpoint
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"AA=="}]}`))}}
			svc := newOpenAIImagesTestService(upstream)
			parsed, err := svc.ParseOpenAIImagesRoutingRequest(c, body)
			require.NoError(t, err)
			_, err = svc.ParseOpenAIImagesRequest(c, body)
			require.Error(t, err, "ordinary accounts use the upstream Images parser")
			_, err = svc.ForwardImages(context.Background(), c, engineAccount(), body, parsed, "")
			require.NoError(t, err)
			require.JSONEq(t, string(body), string(upstream.lastBody))
			require.Equal(t, "/prefix/v1/images/edits", upstream.lastReq.URL.Path)
		})
	}
}
