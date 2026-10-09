package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func daybreakHTTPSettings(enabled bool) *SettingService {
	return &SettingService{settingRepo: &daybreakHTTPSettingRepo{fakeSettingRepo: fakeSettingRepo{vals: map[string]string{
		SettingKeyOpenAIDaybreakEnabled: strconv.FormatBool(enabled),
	}}}}
}

type daybreakHTTPSettingRepo struct{ fakeSettingRepo }

func (r *daybreakHTTPSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string)
	for _, key := range keys {
		if value, ok := r.vals[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func TestDaybreakHTTPPhysicalSendClearsAllOpenAIEndpoints(t *testing.T) {
	const original = `{"model":"test-model","access_programs":{"cyber":null,"future":900719925474099312345},"input":[{"access_programs":{"cyber":"history"}}]}`
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens", "/v1/responses/input_tokens", "/v1/alpha/search", "/v1/images/generations", "/v1/images/edits"} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey, AccountTypeSetupToken} {
			t.Run(path+"/"+accountType, func(t *testing.T) {
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}}
				svc := &OpenAIGatewayService{settingService: daybreakHTTPSettings(false), httpUpstream: upstream}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType}
				body := []byte(original)
				req := httptest.NewRequest(http.MethodPost, "https://local-upstream.example"+path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				_, err := svc.doOpenAIUpstream(req, "", account)
				require.NoError(t, err)
				require.False(t, gjson.GetBytes(upstream.lastBody, "access_programs.cyber").Exists())
				require.Equal(t, "900719925474099312345", gjson.GetBytes(upstream.lastBody, "access_programs.future").Raw)
				require.Equal(t, "history", gjson.GetBytes(upstream.lastBody, "input.0.access_programs.cyber").String())
				require.Equal(t, original, string(body))
				require.Equal(t, "global_disabled_stripped", openAIDaybreakDecisionFromRequest(upstream.lastReq))
				require.EqualValues(t, len(upstream.lastBody), upstream.lastReq.ContentLength)
				replay, err := upstream.lastReq.GetBody()
				require.NoError(t, err)
				replayed, err := io.ReadAll(replay)
				require.NoError(t, err)
				require.NoError(t, replay.Close())
				require.Equal(t, upstream.lastBody, replayed)
			})
		}
	}
}

func TestDaybreakHTTPGuardPreservesOtherPlatformsAndEnabledRequests(t *testing.T) {
	const body = `{"access_programs":{"cyber":"invalid-but-client-owned"}}`
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini} {
		for _, enabled := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			originalReader := req.Body
			require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, &Account{Platform: platform}, daybreakHTTPSettings(enabled)))
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			if enabled || platform != PlatformOpenAI {
				require.Equal(t, body, string(wire))
				require.Equal(t, originalReader, req.Body)
			} else {
				require.JSONEq(t, `{"access_programs":{}}`, string(wire))
			}
		}
	}
}

func TestDaybreakHTTPGuardRejectsUncleanableBodyBeforeSend(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{settingService: daybreakHTTPSettings(false), httpUpstream: upstream}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"access_programs":{"cyber":"blue"}`))
	req.Header.Set("Content-Type", "application/json")
	_, err := svc.doOpenAIUpstream(req, "", &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	require.Error(t, err)
	require.Empty(t, upstream.requests)
}

func TestDaybreakHTTPGuardIgnoresOverriddenJSONContentType(t *testing.T) {
	for _, path := range []string{"/v1/responses/input_tokens", "/prefix/v1/responses", "/unknown-json-endpoint"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"access_programs":{"cyber":"standard"}}`))
		req.Header.Set("Content-Type", "text/plain")
		require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, &Account{Platform: PlatformOpenAI}, daybreakHTTPSettings(false)))
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(body, "access_programs.cyber").Exists())
		require.Equal(t, "text/plain", req.Header.Get("Content-Type"))
	}
}

func TestDaybreakHTTPFinalizerSharesOneAttemptSnapshot(t *testing.T) {
	settings := daybreakHTTPSettings(false)
	svc := &OpenAIGatewayService{settingService: settings}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-6-astra","instructions":"existing","access_programs":{"cyber":"standard"}}`)
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/v1/responses/input_tokens"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		settings.publishOpenAIDaybreakEnabled("false")
		out, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(out, "access_programs.cyber").Exists())
		settings.publishOpenAIDaybreakEnabled("true")
		require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, account, settings))
		require.False(t, openAIDaybreakPolicyEnabled(req.Context(), settings), "one physical attempt retains the decision already observed")
		require.Equal(t, "global_disabled_stripped", openAIDaybreakDecisionFromRequest(req))
		require.True(t, gjson.GetBytes(body, "access_programs.cyber").Exists(), "caller and later attempts remain untouched")
	}
}

func daybreakMultipartFixture(t *testing.T, program string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.SetBoundary("daybreak-stable-boundary"))
	require.NoError(t, w.WriteField("model", "gpt-image-1"))
	require.NoError(t, w.WriteField("access_programs", program))
	file, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="image"; filename="first.png"`}, "Content-Type": {"image/png"}, "X-Image-Metadata": {"original"}})
	require.NoError(t, err)
	_, err = file.Write([]byte("\x00\x01image\xff\r\n{\"cyber\":\"file-content\"}"))
	require.NoError(t, err)
	require.NoError(t, w.WriteField("n", "2"))
	require.NoError(t, w.WriteField("quality", "high"))
	require.NoError(t, w.WriteField("access_programs", `{"cyber":"daybreak_red","second":true}`))
	file, err = w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="access_programs"; filename="metadata.json"`}, "Content-Type": {"application/json"}})
	require.NoError(t, err)
	_, err = file.Write([]byte(`{"cyber":"file-not-field"}`))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return body.Bytes(), w.FormDataContentType()
}

func TestDaybreakMultipartGuardPreservesFilesParametersAndOrder(t *testing.T) {
	body, contentType := daybreakMultipartFixture(t, `{"cyber":null,"future":900719925474099312345}`)
	original := bytes.Clone(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	require.NoError(t, prepareOpenAIDaybreakHTTPRequest(req, &Account{Platform: PlatformOpenAI}, daybreakHTTPSettings(false)))
	cleaned, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, contentType, req.Header.Get("Content-Type"))
	before := multipart.NewReader(bytes.NewReader(original), "daybreak-stable-boundary")
	after := multipart.NewReader(bytes.NewReader(cleaned), "daybreak-stable-boundary")
	for {
		first, err := before.NextRawPart()
		second, secondErr := after.NextRawPart()
		if err == io.EOF {
			require.ErrorIs(t, secondErr, io.EOF)
			break
		}
		require.NoError(t, err)
		require.NoError(t, secondErr)
		require.Equal(t, first.Header, second.Header)
		oldValue, err := io.ReadAll(first)
		require.NoError(t, err)
		newValue, err := io.ReadAll(second)
		require.NoError(t, err)
		if first.FormName() == "access_programs" && first.FileName() == "" {
			require.False(t, gjson.GetBytes(newValue, "cyber").Exists())
			if gjson.GetBytes(oldValue, "future").Exists() {
				require.Equal(t, "900719925474099312345", gjson.GetBytes(newValue, "future").Raw)
			}
		} else {
			require.Equal(t, oldValue, newValue)
		}
	}
	require.Equal(t, original, body)
	unchanged, changed, err := stripOpenAIMultipartCyber(cleaned, "daybreak-stable-boundary")
	require.NoError(t, err)
	require.False(t, changed)
	require.Same(t, &cleaned[0], &unchanged[0])
}

func TestDaybreakAccountDiagnosticsPhysicalSendClearsCyber(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}}
	svc := &AccountTestService{settingService: daybreakHTTPSettings(false), httpUpstream: upstream}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-astra","access_programs":{"cyber":"daybreak_blue"}}`))
	_, err := svc.doOpenAIAccountTestUpstream(req, "", &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(upstream.lastBody, "access_programs.cyber").Exists())
}

func TestDaybreakEnginePhysicalSendClearsCyber(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1/responses/compact", "/v1/alpha/search", "/v1/messages/count_tokens", "/v1/images/generations", "/v1/images/edits"} {
		t.Run(endpoint, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: daybreakHTTPSettings(false), httpUpstream: upstream}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, nil)
			body := []byte(`{"model":"alias","access_programs":{"cyber":"daybreak_blue","other":900719925474099312345},"unknown":true}`)
			_, err := svc.forwardCodexEngine(context.Background(), c, engineAccount(), body, endpoint, "")
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(upstream.lastBody, "access_programs.cyber").Exists())
			require.Equal(t, "900719925474099312345", gjson.GetBytes(upstream.lastBody, "access_programs.other").Raw)
			require.Equal(t, "native", gjson.GetBytes(upstream.lastBody, "model").String())
			require.True(t, gjson.GetBytes(body, "access_programs.cyber").Exists())
		})
	}
}

func TestDaybreakWSPrewarmClearsCyberWithoutChangingSource(t *testing.T) {
	settings := daybreakHTTPSettings(false)
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = true
	connection := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_prewarm"}}`)}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	lease := &openAIWSConnLease{accountID: account.ID, conn: newOpenAIWSConn("daybreak-prewarm", account.ID, connection, nil)}
	svc := &OpenAIGatewayService{cfg: cfg, settingService: settings}
	payload := map[string]any{"type": "response.create", "model": "gpt-6-astra", "access_programs": map[string]any{"cyber": "standard", "other": json.Number("900719925474099312345")}}
	require.NoError(t, svc.performOpenAIWSGeneratePrewarm(context.Background(), lease, OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, payload, "", nil, account, nil, 0))
	require.Len(t, connection.writes, 1)
	wire := requestToJSONString(connection.writes[0])
	require.False(t, gjson.Get(wire, "access_programs.cyber").Exists())
	require.False(t, gjson.Get(wire, "generate").Bool())
	require.Equal(t, "900719925474099312345", gjson.Get(wire, "access_programs.other").Raw)
	programs, ok := payload["access_programs"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "standard", programs["cyber"])
}

func TestDaybreakLiveControlFramesFollowSwitchAndPreserveBinary(t *testing.T) {
	svc := &OpenAIGatewayService{settingService: daybreakHTTPSettings(false)}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"type":"response.create","access_programs":{"cyber":"standard","other":900719925474099312345},"input":[{"cyber":"content"}]}`)
	for _, messageType := range []coderws.MessageType{coderws.MessageText, coderws.MessageBinary} {
		cleaned, err := svc.prepareOpenAILiveDaybreakFrame(context.Background(), account, messageType, body)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(cleaned, "access_programs.cyber").Exists())
		require.Equal(t, "900719925474099312345", gjson.GetBytes(cleaned, "access_programs.other").Raw)
		require.Equal(t, "content", gjson.GetBytes(cleaned, "input.0.cyber").String())
	}
	binary := []byte("\x00\xff{\"access_programs\":{\"cyber\":\"audio\"}}")
	unchanged, err := svc.prepareOpenAILiveDaybreakFrame(context.Background(), account, coderws.MessageBinary, binary)
	require.NoError(t, err)
	require.Equal(t, binary, unchanged)
	svc.settingService.publishOpenAIDaybreakEnabled("true")
	unchanged, err = svc.prepareOpenAILiveDaybreakFrame(context.Background(), account, coderws.MessageText, body)
	require.NoError(t, err)
	require.Equal(t, body, unchanged)
}
