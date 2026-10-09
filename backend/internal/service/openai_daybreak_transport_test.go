package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const daybreakTransportManifest = `{"models":[{"slug":"gpt-6-astra","available_access_programs":{"cyber":["standard","daybreak_blue"]}},{"slug":"gpt-5.6-cyber","available_access_programs":{"cyber":["daybreak_red"]}}]}`

func daybreakTransportGroup() *Group {
	return &Group{ID: 991, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
		OpenAIDaybreakBlueEnabled: true, OpenAIDaybreakRedEnabled: true}
}

func daybreakTransportContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxkey.Group, daybreakTransportGroup())
}

func seedDaybreakTransportCapabilities(t *testing.T, svc *OpenAIGatewayService, account *Account) *Account {
	t.Helper()
	for _, family := range OpenAIOAuthOSFamilies() {
		scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), svc.accountRepo, account, family)
		require.NoError(t, err)
		svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(scoped), []byte(daybreakTransportManifest), time.Now())
	}
	// The real gateway selects credentials before entering these forwarders.
	scoped, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), svc.accountRepo, account, "")
	require.NoError(t, err)
	return scoped
}

func TestOAuthDaybreakHTTPFinalizerKeepsRetrySourceAndAuthorization(t *testing.T) {
	enableDaybreakObservationTest(t)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 8110, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		OpenAIOAuthCredentialOS: "windows", OpenAIOAuthCredentialOwnerID: 8110, OpenAIOAuthAuthorizationGeneration: "old",
		Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}}
	plan := OpenAIOAuthIdentityPlan{CredentialOS: "linux", OSOwnerID: 8990, AuthorizationGeneration: "selected", ProjectionMode: OpenAIOAuthIdentityProjectionRegular}
	svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesPlanNamespace(plan), []byte(daybreakTransportManifest), time.Now())
	for _, test := range []struct{ name, path, body, kind, want string }{
		{"turn", "/backend-api/codex/responses", `{"model":"gpt-6-astra","input":[]}`, "turn", "daybreak_blue"},
		{"native_compaction", "/backend-api/codex/responses", `{"model":"gpt-6-astra","input":[{"type":"compaction_trigger"}]}`, "compaction", "daybreak_blue"},
		{"legacy_compact", "/backend-api/codex/responses/compact", `{"model":"gpt-6-astra","input":[]}`, "compaction", ""},
		{"count_tokens", "/backend-api/codex/responses/input_tokens", `{"model":"gpt-6-astra","input":[]}`, "turn", ""},
		{"explicit", "/backend-api/codex/responses", `{"model":"gpt-6-astra","access_programs":{"cyber":"standard"}}`, "turn", "standard"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.body)
			req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com"+test.path, bytes.NewReader(body))
			req = req.WithContext(daybreakTransportContext(req.Context()))
			out, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, body, OpenAIOAuthResponsesFinalizeOptions{Plan: plan, FinalModel: "gpt-6-astra", RequestKind: test.kind})
			require.NoError(t, err)
			require.Equal(t, test.want, gjson.GetBytes(out, "access_programs.cyber").String())
			require.Equal(t, test.body, string(body), "final wire injection cannot change the retry source")
			replay, err := req.GetBody()
			require.NoError(t, err)
			wire, err := io.ReadAll(replay)
			require.NoError(t, err)
			require.NoError(t, replay.Close())
			require.Equal(t, out, wire)
			decision := openAIDaybreakDecisionFromRequest(req)
			expectedSource, expectedReason := "automatic", "automatic"
			if test.want == "" {
				expectedSource, expectedReason = "not_added", "excluded_endpoint"
			} else if test.name == "explicit" {
				expectedSource, expectedReason = "client", "client_supplied"
			}
			require.Equal(t, expectedReason, decision)
			svc.recordFingerprintObservationWithBody(nil, account, installationIDResolution{}, req.Header, openAIUpstreamRequestBodySnapshot(req, body), decision)
			observed := SnapshotFingerprintObservations(1)[0].Daybreak
			require.Equal(t, expectedSource, observed.Source)
			require.Equal(t, test.want, observed.CyberValue)
			otherAccount := &Account{ID: 8111, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			otherReq := httptest.NewRequest(http.MethodPost, "https://chatgpt.com"+test.path, bytes.NewReader(body))
			other, err := svc.FinalizeOpenAIOAuthResponsesRequest(nil, otherAccount, otherReq, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra", RequestKind: test.kind})
			require.NoError(t, err)
			require.Equal(t, gjson.GetBytes(body, "access_programs").Raw, gjson.GetBytes(other, "access_programs").Raw, "next account starts from caller fields")
		})
	}
	require.Equal(t, "windows", account.OpenAIOAuthCredentialOS)
	require.Equal(t, "old", account.OpenAIOAuthAuthorizationGeneration)
}

func TestOAuthDaybreakHTTPToWSDoesNotInjectPrewarmOrSourceMap(t *testing.T) {
	enableDaybreakObservationTest(t)
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = true
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_daybreak","model":"gpt-6-astra","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	conn := &openAIWSCaptureConn{events: [][]byte{completed, completed}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
	t.Cleanup(pool.Close)
	account := &Account{ID: 8120, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "mock-oauth-token"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true, OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool,
		accountRepo: newAuthorizedOpenAIOAuthTestRepo(account)}
	account = seedDaybreakTransportCapabilities(t, svc, account)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(daybreakTransportContext(c.Request.Context()))
	source := map[string]any{"model": "gpt-6-astra", "input": []any{map[string]any{"role": "user", "content": "hello"}}}
	_, err := svc.forwardOpenAIWSV2(context.Background(), c, account, source, "", "", "mock-oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, false, "client-alias", "gpt-6-astra", time.Now(), 1, "", nil)
	require.NoError(t, err)
	require.Len(t, conn.writes, 2)
	prewarm := requestToJSONString(conn.writes[0])
	inference := requestToJSONString(conn.writes[1])
	require.Equal(t, gjson.False, gjson.Get(prewarm, "generate").Type)
	require.False(t, gjson.Get(prewarm, "access_programs").Exists())
	require.Equal(t, "daybreak_blue", gjson.Get(inference, "access_programs.cyber").String())
	require.NotContains(t, source, "access_programs")
	observed := SnapshotFingerprintObservations(1)[0].Daybreak
	require.Equal(t, "automatic", observed.Source)
	require.Equal(t, "daybreak_blue", observed.CyberValue)
}

func TestOAuthDaybreakWSPhysicalSendUsesEachTurnMappedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			enableDaybreakObservationTest(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			staged := newStagedPassthroughConn()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			svc := newPassthroughLifecycleService(cfg, staged)
			svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: profitControlGroupRepo{group: daybreakTransportGroup()}}
			dialer := &integrityWSDialer{traffic: staged}
			svc.openaiWSPassthroughDialer = dialer
			svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			svc.openaiWSPool.setClientDialerForTest(dialer)
			defer svc.openaiWSPool.Close()
			upstream := &httpUpstreamRecorder{}
			if mode == OpenAIWSIngressModeHTTPBridge {
				svc.httpUpstream = upstream
				for turn := 1; turn <= 3; turn++ {
					upstream.responses = append(upstream.responses, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_daybreak_%d\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", turn)))})
				}
			}
			account := &Account{ID: 8130, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "mock-oauth-token"},
				Extra: map[string]any{"responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": mode,
					OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}}
			svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
			account = seedDaybreakTransportCapabilities(t, svc, account)
			server, done := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
				c.Request = c.Request.WithContext(daybreakTransportContext(c.Request.Context()))
				setOpenAIClientRequestedStream(c, true)
				return &OpenAIWSIngressHooks{MapRequestModel: func(turn int, _ string) (string, error) {
					if turn == 2 {
						return "gpt-5.6-cyber", nil
					}
					return "gpt-6-astra", nil
				}}
			})
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			for turn, want := range []string{"daybreak_blue", "daybreak_red", "standard"} {
				explicit := ""
				if turn == 2 {
					explicit = `,"access_programs":{"cyber":"standard"}`
				}
				baseline := []byte(fmt.Sprintf(`{"type":"response.create","model":"client-alias","stream":true,"input":[{"role":"user","content":"turn %d"}]%s}`, turn+1, explicit))
				require.NoError(t, client.Write(ctx, coderws.MessageText, baseline))
				var wire []byte
				if mode != OpenAIWSIngressModeHTTPBridge {
					wire = requirePassthroughUpstreamWrite(t, staged, 3*time.Second)
					staged.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_daybreak_%d","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`, turn+1))
				}
				_, err = readPassthroughLifecycleFrame(t, client, 3*time.Second)
				require.NoError(t, err)
				if mode == OpenAIWSIngressModeHTTPBridge {
					wire = upstream.lastBody
				}
				require.Equal(t, want, gjson.GetBytes(wire, "access_programs.cyber").String())
				if turn < 2 {
					require.False(t, gjson.GetBytes(baseline, "access_programs").Exists())
				}
			}
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("local WS gateway did not exit")
			}
			var observations []*OpenAIDaybreakObservation
			for _, entry := range SnapshotFingerprintObservations(20) {
				if entry.Daybreak != nil {
					observations = append(observations, entry.Daybreak)
				}
			}
			require.Len(t, observations, 3)
			for index, want := range []string{"standard", "daybreak_red", "daybreak_blue"} {
				require.Equal(t, want, observations[index].CyberValue)
				source := "automatic"
				if index == 0 {
					source = "client"
				}
				require.Equal(t, source, observations[index].Source)
			}
		})
	}
}

func TestOAuthDaybreakCompatPreservesClientAccessPrograms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/messages"} {
		for _, value := range []string{
			`{"cyber":"standard"}`,
			`{"cyber":null}`,
			`{"cyber":"not-a-program","future":900719925474099312345}`,
			`{"future":900719925474099312345}`,
			`null`, `false`, `[]`, `"invalid-object"`,
		} {
			t.Run(endpoint+"/"+value, func(t *testing.T) {
				body := []byte(`{"model":"gpt-6-astra","messages":[{"role":"user","content":"hello"}],"max_tokens":32,"stream":false,"access_programs":` + value + `}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"mock validation error"}}`)),
				}}
				account := &Account{
					ID: 8101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
					Credentials: map[string]any{"access_token": "mock-oauth-token", "chatgpt_account_id": "mock-owner"},
				}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account)}
				var err error
				if endpoint == "/v1/chat/completions" {
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-6-astra")
				} else {
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-6-astra")
				}
				require.Error(t, err, "mock upstream deliberately rejects the request after capturing its body")
				require.NotNil(t, upstream.lastReq)
				require.Equal(t, value, gjson.GetBytes(upstream.lastBody, "access_programs").Raw)
				require.Equal(t, value, gjson.GetBytes(body, "access_programs").Raw, "original client body must be immutable")
			})
		}
	}
}

func TestDaybreakWSGlobalSwitchUpdatesOnEveryPhysicalTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModeCtxPool, OpenAIWSIngressModePassthrough, OpenAIWSIngressModeHTTPBridge} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			staged := newStagedPassthroughConn()
			cfg := passthroughLifecycleConfig()
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			svc := newPassthroughLifecycleService(cfg, staged)
			svc.settingService = daybreakHTTPSettings(true)
			dialer := &integrityWSDialer{traffic: staged}
			svc.openaiWSPassthroughDialer = dialer
			svc.openaiWSPool = newOpenAIWSConnPool(cfg)
			svc.openaiWSPool.setClientDialerForTest(dialer)
			defer svc.openaiWSPool.Close()
			upstream := &httpUpstreamRecorder{}
			if mode == OpenAIWSIngressModeHTTPBridge {
				svc.httpUpstream = upstream
				for turn := 1; turn <= 3; turn++ {
					upstream.responses = append(upstream.responses, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_policy_%d\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", turn)))})
				}
			}
			account := &Account{ID: 8131, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "mock-oauth-token"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true, "openai_oauth_responses_websockets_v2_mode": mode}}
			svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)
			server, done := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
				setOpenAIClientRequestedStream(c, true)
				return nil
			})
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			for turn, enabled := range []bool{true, false, true} {
				svc.settingService.publishOpenAIDaybreakEnabled(fmt.Sprint(enabled))
				baseline := []byte(fmt.Sprintf(`{"type":"response.create","model":"gpt-6-astra","stream":true,"input":[{"role":"user","content":"turn %d"}],"access_programs":{"cyber":"standard","other":900719925474099312345}}`, turn+1))
				require.NoError(t, client.Write(ctx, coderws.MessageText, baseline))
				var wire []byte
				if mode != OpenAIWSIngressModeHTTPBridge {
					wire = requirePassthroughUpstreamWrite(t, staged, 3*time.Second)
					staged.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_policy_%d","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`, turn+1))
				}
				_, err = readPassthroughLifecycleFrame(t, client, 3*time.Second)
				require.NoError(t, err)
				if mode == OpenAIWSIngressModeHTTPBridge {
					wire = upstream.lastBody
				}
				require.Equal(t, enabled, gjson.GetBytes(wire, "access_programs.cyber").Exists(), "turn %d must use the current switch", turn+1)
				require.Equal(t, "900719925474099312345", gjson.GetBytes(wire, "access_programs.other").Raw)
				require.Equal(t, "standard", gjson.GetBytes(baseline, "access_programs.cyber").String())
			}
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("local WS gateway did not exit")
			}
		})
	}
}

func TestOAuthDaybreakHTTPAdaptersInjectAfterMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"responses", "passthrough", "chat", "messages"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, body := "/v1/responses", []byte(`{"model":"client-alias","input":"hello","stream":true}`)
			if mode == "passthrough" {
				// This established mode deliberately preserves the caller's model.
				body = []byte(`{"model":"gpt-6-astra","input":"hello","stream":true}`)
			}
			if mode == "chat" || mode == "messages" {
				endpoint = "/v1/messages"
				if mode == "chat" {
					endpoint = "/v1/chat/completions"
				}
				body = []byte(`{"model":"client-alias","messages":[{"role":"user","content":"hello"}],"stream":true}`)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			c.Request = c.Request.WithContext(daybreakTransportContext(c.Request.Context()))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest,
				Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"mock validation error"}}`))}}
			account := &Account{ID: 8140, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "mock-oauth-token", "model_mapping": map[string]any{"client-alias": "gpt-6-astra"}},
				Extra:       map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true, "openai_passthrough": mode == "passthrough"}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: newAuthorizedOpenAIOAuthTestRepo(account), cache: &stubGatewayCache{}, toolCorrector: NewCodexToolCorrector()}
			seedDaybreakTransportCapabilities(t, svc, account)
			var err error
			switch mode {
			case "chat":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-6-astra")
			case "messages":
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "gpt-6-astra")
			default:
				_, err = svc.Forward(context.Background(), c, account, body)
			}
			require.Error(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "daybreak_blue", gjson.GetBytes(upstream.lastBody, "access_programs.cyber").String())
			require.False(t, gjson.GetBytes(body, "access_programs").Exists())
		})
	}
}

func TestRestoreOpenAIClientAccessProgramsPreservesRawJSON(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","sequence":900719925474099312345,"input":[]}`)
	unchanged, err := restoreOpenAIClientAccessPrograms(body, nil)
	require.NoError(t, err)
	require.Equal(t, body, unchanged)
	value := json.RawMessage(`{"cyber":null,"extension":900719925474099312346}`)
	out, err := restoreOpenAIClientAccessPrograms(body, value)
	require.NoError(t, err)
	require.Equal(t, string(value), gjson.GetBytes(out, "access_programs").Raw)
	require.Equal(t, "900719925474099312345", gjson.GetBytes(out, "sequence").Raw)
	require.False(t, gjson.GetBytes(body, "access_programs").Exists())
}

func TestOAuthDaybreakBridgeRetryRestoresOnlyCallerProgram(t *testing.T) {
	for _, source := range []string{
		`{"model":"gpt-6-astra"}`,
		`{"model":"gpt-6-astra","access_programs":{"future":900719925474099312345}}`,
		`{"model":"gpt-6-astra","access_programs":{"cyber":"standard"}}`,
		`{"model":"gpt-6-astra","access_programs":null}`,
	} {
		t.Run(source, func(t *testing.T) {
			wire := []byte(`{"model":"gpt-6-astra","client_metadata":{"thread_id":"projected-thread"},"access_programs":{"cyber":"daybreak_blue","future":123}}`)
			retry, err := restoreOpenAIAccessProgramsForRetry(wire, []byte(source))
			require.NoError(t, err)
			require.Equal(t, gjson.Get(source, "access_programs").Raw, gjson.GetBytes(retry, "access_programs").Raw)
			require.Equal(t, "projected-thread", gjson.GetBytes(retry, "client_metadata.thread_id").String(), "retain the bridge's finalized identity")
			require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String(), "wire snapshot stays immutable")
		})
	}
}
