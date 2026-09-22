package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCodexStateHTTPBundleFallbackRestoresBothOriginalCarriers(t *testing.T) {
	for _, name := range []string{"invalid_scope", "disallowed_host", "plain_http", "expired_after_guard", "disabled_before_send"} {
		t.Run(name, func(t *testing.T) {
			state, _, account := newCodexStateTestService(t)
			now := time.Now().UTC().Truncate(time.Second)
			if name == "expired_after_guard" {
				// The service guard still observes a valid bundle, but the physical
				// transport's later clock has already reached its expiry.
				now = now.Add(-CodexTurnStateLifetime - time.Second)
			}
			state.now = func() time.Time { return now }
			seed, err := state.Prepare(context.Background(), account, "gpt-5")
			require.NoError(t, err)
			token := codexStateTestToken(10, now)
			state.Observe(seed, token)
			require.NoError(t, state.Finish(context.Background(), seed, true))
			gateway := &OpenAIGatewayService{codexTurnStateService: state}
			baseline := `{"model":"gpt-5","input":"hello","client_metadata":{"x-codex-turn-state":"original-body-ticket","other":"before"}}`
			request := codexStateHTTPRequest(t, baseline)
			request.Header["x-codex-turn-state"] = []string{"original-header-ticket"}
			request = gateway.prepareOpenAICodexStateHTTPRequest(nil, account, request)
			collector := request.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
			require.Equal(t, token, request.Header.Get(openAICodexTurnStateHeader))
			request = withOpenAINativeHTTPRequestScope(request, account, nil, "business")
			// Subsequent normalization must survive restoration of the two ticket
			// carriers; replaying the entire old body or headers would lose it.
			reader, err := request.GetBody()
			require.NoError(t, err)
			current, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			current, err = sjson.SetBytes(current, "client_metadata.other", "normalized-after-prepare")
			require.NoError(t, err)
			request.Body = io.NopCloser(bytes.NewReader(current))
			request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(current)), nil }
			request.ContentLength = int64(len(current))
			request.Header.Set("X-Later-Normalized", "preserved")
			switch name {
			case "invalid_scope":
				request = request.WithContext(openaicookies.WithoutScope(request.Context()))
			case "disallowed_host":
				request.URL.Host = "unrelated.example"
			case "plain_http":
				request.URL.Scheme = "http"
			case "disabled_before_send":
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
				request.Header.Set("Authorization", "Bearer normalized-after-prepare")
			}
			if name != "disabled_before_send" {
				require.True(t, state.ValidateCredentialHeaders(request.Context(), collector.attempt, request.Header), "the later manager rejection must exercise fallback after a successful service guard")
			}
			wantAuthorization := request.Header.Get("Authorization")
			transport := openaicookies.NewManager().Wrap(openAIPluginRoundTripFunc(func(outbound *http.Request) (*http.Response, error) {
				require.NotContains(t, outbound.Header, http.CanonicalHeaderKey(openAICodexTurnStateHeader))
				require.Equal(t, []string{"original-header-ticket"}, outbound.Header["x-codex-turn-state"])
				require.Empty(t, outbound.Header.Get("Cookie"))
				require.Equal(t, wantAuthorization, outbound.Header.Get("Authorization"))
				require.Equal(t, "preserved", outbound.Header.Get("X-Later-Normalized"))
				body, readErr := io.ReadAll(outbound.Body)
				require.NoError(t, readErr)
				require.Equal(t, "original-body-ticket", gjson.GetBytes(body, "client_metadata.x-codex-turn-state").String())
				require.Equal(t, "normalized-after-prepare", gjson.GetBytes(body, "client_metadata.other").String())
				require.Equal(t, int64(len(body)), outbound.ContentLength)
				retry, bodyErr := outbound.GetBody()
				require.NoError(t, bodyErr)
				retryBody, bodyErr := io.ReadAll(retry)
				require.NoError(t, bodyErr)
				require.NoError(t, retry.Close())
				require.Equal(t, body, retryBody)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=must-not-capture; Path=/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}))
			response, err := transport.RoundTrip(request)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			_, err = collector.attempt.cookieAttempt.Snapshot(now.Add(CodexTurnStateLifetime))
			require.ErrorIs(t, err, openaicookies.ErrNoSnapshot)
			require.True(t, collector.attempt.cookieResponseFailed, "the fallback response cannot publish a replacement bundle")
			finishCodexTurnStateHTTPAttempt(state, collector.attempt, false)
		})
	}
}
