package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type mergedWSWindowCache struct{ passthroughCompactWindowCache }

func (c *mergedWSWindowCache) ResolveOpenAICodexClientWindow(ctx context.Context, mappingKey string, transition OpenAICodexClientWindowTransition, ttl time.Duration) (OpenAICodexClientWindowResult, error) {
	c.mu.Lock()
	store := c.compactWindowStore()
	c.mu.Unlock()
	return store.ResolveOpenAICodexClientWindow(ctx, mappingKey, transition, ttl)
}

// Exercise the ingress loop, rather than only the raw boundary helper: the
// local identity projector replaces client window IDs, compact commits advance
// that projected window, and retries must continue using its frozen snapshot.
func TestOpenAIWSMergedContextWindowLifecycle(t *testing.T) {
	for _, scenario := range []string{"same_window_tool_output", "client_rollover", "compact_rollover", "rollover_reconnect"} {
		t.Run(scenario, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &config.Config{}
			cfg.JWT.Secret = "merged-ws-window-" + scenario
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
			cfg.Gateway.OpenAIWS.StoreDisabledConnMode = openAIWSStoreDisabledConnModeOff
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 5

			firstComplete := []byte(`{"type":"response.completed","response":{"id":"resp_window_first","status":"completed","model":"gpt-5.4","output":[{"type":"function_call","name":"shell","call_id":"call_window","arguments":"{}"}]}}`)
			if scenario == "compact_rollover" {
				firstComplete = []byte(`{"type":"response.completed","response":{"id":"resp_window_first","status":"completed","model":"gpt-5.4","output":[]}}`)
			}
			secondComplete := []byte(`{"type":"response.completed","response":{"id":"resp_window_second","status":"completed","model":"gpt-5.4","output":[]}}`)
			firstConn := &openAIWSCaptureConn{events: [][]byte{firstComplete, secondComplete}}
			conns := []openAIWSClientConn{firstConn}
			var reconnect *openAIWSCaptureConn
			if scenario == "rollover_reconnect" {
				// EOF after the first successful turn forces the rolled-over second
				// turn to reconnect before it has delivered anything downstream.
				firstConn.events = [][]byte{firstComplete}
				reconnect = &openAIWSCaptureConn{events: [][]byte{secondComplete}}
				conns = append(conns, reconnect)
			}
			dialer := &openAIWSQueueDialer{conns: conns}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(dialer)
			defer pool.Close()
			windowCache := &mergedWSWindowCache{}
			svc := &OpenAIGatewayService{
				cfg: cfg,
				settingService: NewSettingService(&openAIUUIDv7RuntimeRepo{values: map[string]string{
					SettingKeyEnableOpenAIUUIDv7SessionIdentity: "true",
				}}, nil),
				httpUpstream: &httpUpstreamRecorder{}, cache: windowCache,
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool,
			}
			account := &Account{
				ID: 945550, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"access_token": "synthetic-oauth-token"},
				Extra: map[string]any{
					"responses_websockets_v2_enabled":           true,
					"openai_passthrough":                        true,
					"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
					openAIPinnedInstallationIDKey:               transportTestPinnedInstallationID,
				},
			}
			svc.accountRepo = newAuthorizedOpenAIOAuthTestRepo(account)

			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r.Clone(r.Context())
				c.Request.Header = r.Header.Clone()
				c.Request.Header.Set("User-Agent", "codex_cli_rs/0.150.1 (Windows NT 10.0; x86_64)")
				c.Request.URL.Path = "/v1/responses"
				c.Set("api_key", &APIKey{ID: 945551})
				readCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				_, first, err := conn.Read(readCtx)
				cancel()
				if err == nil {
					err = svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "synthetic-oauth-token", first, nil)
				}
				serverErr <- err
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = client.CloseNow() }()
			send := func(body []byte, responseID string) {
				t.Helper()
				require.NoError(t, client.Write(ctx, coderws.MessageText, body))
				_, event, err := client.Read(ctx)
				if err != nil {
					select {
					case ingressErr := <-serverErr:
						t.Fatalf("read response: %v; ingress: %v", err, ingressErr)
					default:
					}
				}
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
				require.Equal(t, responseID, gjson.GetBytes(event, "response.id").String())
			}

			session := codexClientWindowPathUUID(t)
			windowA, windowB := codexClientWindowPathUUID(t), codexClientWindowPathUUID(t)
			first := codexClientWindowPathBody(t, true, session, 0, windowA, windowA, "", true)
			second := codexClientWindowPathBody(t, true, session, 1, windowA, windowB, windowA, true)
			second, err = sjson.SetBytes(second, "previous_response_id", "resp_window_first")
			require.NoError(t, err)
			switch scenario {
			case "same_window_tool_output":
				// Omitted client window info inherits the projected socket window;
				// the tool output must keep its previous-response continuation.
				second = []byte(`{"type":"response.create","model":"gpt-5.4","stream":true,"previous_response_id":"resp_window_first","input":[{"type":"function_call_output","call_id":"call_window","output":"ok"}]}`)
			case "compact_rollover":
				first = []byte(`{"type":"response.create","model":"gpt-5.4","stream":true,"input":[{"role":"user","content":"compact locally"}],"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"compaction\",\"compaction\":{\"trigger\":\"manual\",\"reason\":\"user_requested\",\"implementation\":\"responses\",\"phase\":\"standalone_turn\",\"strategy\":\"memento\"}}"}}`)
				second = []byte(`{"type":"response.create","model":"gpt-5.4","stream":true,"previous_response_id":"resp_window_first","input":[{"role":"user","content":"new compacted history"}]}`)
			}
			send(first, "resp_window_first")
			send(second, "resp_window_second")
			require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			select {
			case err := <-serverErr:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("websocket ingress did not finish")
			}

			require.Len(t, firstConn.writes, 2)
			firstSent := []byte(requestToJSONString(firstConn.writes[0]))
			secondSent := []byte(requestToJSONString(firstConn.writes[1]))
			firstWindow := openAIWSPayloadCodexWindowID(firstSent)
			secondWindow := openAIWSPayloadCodexWindowID(secondSent)
			require.NotEmpty(t, firstWindow)
			require.NotContains(t, firstWindow, session, "boundary must use the server-projected window")
			if scenario == "same_window_tool_output" {
				require.Equal(t, firstWindow, secondWindow)
				require.Equal(t, "resp_window_first", gjson.GetBytes(secondSent, "previous_response_id").String(), "forwarded input: %s", gjson.GetBytes(secondSent, "input").Raw)
			} else {
				require.NotEqual(t, firstWindow, secondWindow)
				require.False(t, gjson.GetBytes(secondSent, "previous_response_id").Exists())
				require.False(t, gjson.GetBytes(secondSent, `input.#(type=="function_call")`).Exists(), "old-window replay must not be appended")
				require.NotContains(t, string(secondSent), "compact locally")
			}
			for _, key := range []string{"session_id", "thread_id", "installation_id"} {
				firstMeta := gjson.GetBytes(firstSent, "client_metadata.x-codex-turn-metadata").String()
				secondMeta := gjson.GetBytes(secondSent, "client_metadata.x-codex-turn-metadata").String()
				require.Equal(t, gjson.Get(firstMeta, key).String(), gjson.Get(secondMeta, key).String(), "window rollover must not change %s", key)
			}
			if reconnect != nil {
				require.Equal(t, 2, dialer.DialCount())
				require.Len(t, reconnect.writes, 1)
				retried := []byte(requestToJSONString(reconnect.writes[0]))
				require.Equal(t, secondWindow, openAIWSPayloadCodexWindowID(retried))
				require.False(t, gjson.GetBytes(retried, "previous_response_id").Exists())
				require.JSONEq(t, gjson.GetBytes(secondSent, "input").Raw, gjson.GetBytes(retried, "input").Raw)
			} else {
				require.Equal(t, 1, dialer.DialCount())
			}
			if scenario == "compact_rollover" {
				windowCache.mu.Lock()
				commits := windowCache.commitCalls
				windowCache.mu.Unlock()
				require.Equal(t, 1, commits, "only delivered compact completion advances the window")
			}
		})
	}
}
