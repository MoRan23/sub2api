package service

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Exercise the entire recovery cycle through the gateway and an isolated mock
// collector. No business response is replayed and no real upstream is contacted.
func TestCodexTurnStateRecoveryWireTargetThenExtendedThenRecollect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{"personal", "team_business"} {
		for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
			for _, carrier := range []string{"header", "metadata"} {
				t.Run(accountType+"/"+path+"/"+carrier, func(t *testing.T) {
					isolateCodexTurnStateSummaryStore(t)
					state, repo, account := newCodexStateTestService(t)
					now := time.Now().UTC().Truncate(time.Second)
					state.now = func() time.Time { return now }
					account.Concurrency = 1
					account.Extra[CodexTurnStateExtraKey].(map[string]any)["account_type"] = accountType
					if path == "passthrough" {
						account.Extra["openai_passthrough"] = true
					}
					blocks := 10
					if accountType == "team_business" {
						blocks = 12
					}
					old := codexStateTestToken(blocks, now.Add(-10*time.Minute))
					extended := codexStateTestToken(blocks+1, now.Add(-time.Second))
					fresh := codexStateTestToken(blocks, now)
					seed, err := state.Prepare(context.Background(), account, "gpt-5.4")
					require.NoError(t, err)
					state.Observe(seed, old)
					require.NoError(t, state.Finish(context.Background(), seed, true))

					calls := 0
					state.collector = NewCodexTurnStateHTTPCollector(func(_ context.Context, input CodexTurnStateCollectRequest, request *http.Request) (*http.Response, error) {
						calls++
						require.Equal(t, account.ID, input.Account.ID)
						require.Equal(t, int64(2), input.ProxyID)
						require.Equal(t, "gpt-5.4", input.Model)
						require.Empty(t, request.Header.Get(openAICodexTurnStateHeader))
						body, readErr := io.ReadAll(request.Body)
						require.NoError(t, readErr)
						require.Equal(t, "Reply with OK.", gjson.GetBytes(body, "instructions").String())
						require.False(t, gjson.GetBytes(body, "client_metadata.x-codex-turn-state").Exists())
						require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
						require.NotContains(t, string(body), "hello")
						return codexStateHTTPIntegrationResponse(fresh, "metadata", false), nil
					})
					// Enable enqueue without starting workers so the invalidated state can
					// be inspected before executing the already-enqueued collector task.
					state.ctx = context.Background()
					upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(extended, carrier, false)}
					gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
					result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, true))
					require.NoError(t, err, recorder.Body.String())
					require.NotNil(t, result)
					require.Len(t, upstream.requests, 1, "the delivered business request is never replayed")
					require.Equal(t, old, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
					require.Zero(t, calls)
					record, err := repo.Get(context.Background(), seed.key)
					require.NoError(t, err)
					require.Empty(t, record.EncryptedToken)
					require.True(t, record.ExpiresAt.IsZero())
					require.Equal(t, "extended_shape", record.DemandReason)
					require.Zero(t, record.CollectorExtendedCount, "business anomalies never count toward proxy rotation")
					summary := requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", extended, carrier, "business", len(old))
					require.Equal(t, "suspect", summary.ResponseShape)
					select {
					case key := <-state.queue:
						require.Equal(t, seed.key, key)
						state.collect(context.Background(), key)
					default:
						t.Fatal("delivered anomaly did not enqueue recovery")
					}
					require.Equal(t, 1, calls)
					recovered, err := repo.Get(context.Background(), seed.key)
					require.NoError(t, err)
					plain, err := state.encryptor.Decrypt(recovered.EncryptedToken)
					require.NoError(t, err)
					require.Equal(t, fresh, plain)
					require.Empty(t, recovered.DemandReason)
					collected := requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", fresh, "metadata", "collector", 0)

					upstream.resp = codexStateHTTPIntegrationResponse("", "header", false)
					_, recorder, err = codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, true))
					require.NoError(t, err, recorder.Body.String())
					require.Equal(t, fresh, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
					unchanged := requireCodexEnabledSummary(t, state, account.ID, "gpt-5.4", fresh, "metadata", "collector", 0)
					require.Equal(t, collected.ObservedAt, unchanged.ObservedAt, "a later business send with no returned state must not relabel old collector evidence")
					require.Equal(t, 1, calls)
				})
			}
		}
	}
}
