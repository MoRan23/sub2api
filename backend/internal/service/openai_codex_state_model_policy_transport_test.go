package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexStatePolicyTransportHarness struct {
	svc      *OpenAIGatewayService
	account  *Account
	upstream *stagedPassthroughConn
	request  chan http.Header
	response http.Header
	record   func(string) CodexTurnStateRecord
}

func newCodexStatePolicyTransportHarness(t *testing.T, passthrough bool) codexStatePolicyTransportHarness {
	t.Helper()
	if passthrough {
		svc, account, _, repo, dialer := newCodexStatePassthroughHarness(t, true)
		return codexStatePolicyTransportHarness{svc, account, dialer.conn, dialer.request, dialer.headers, func(model string) CodexTurnStateRecord {
			repo.mu.Lock()
			defer repo.mu.Unlock()
			for key, row := range repo.records {
				if key.Model == model {
					return row
				}
			}
			return CodexTurnStateRecord{}
		}}
	}
	svc, account, repo, _, dialer := newCodexWSStateIngressHarness(t)
	return codexStatePolicyTransportHarness{svc, account, dialer.conn.stagedPassthroughConn, dialer.request, dialer.response, repo.record}
}

func waitCodexModelPolicyTransportRow(t *testing.T, accountID int64, model string, afterSequence uint64, responseLength int) FingerprintObservationEntry {
	t.Helper()
	var result FingerprintObservationEntry
	require.Eventually(t, func() bool {
		for _, row := range SnapshotFingerprintObservations(0) {
			if row.AccountID == accountID && row.SequenceID > afterSequence && row.CodexTurnState != nil && row.CodexTurnState.Model == model && row.CodexTurnState.ResponseLength == responseLength {
				result = row
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond, "the actual final model must receive its own response observation")
	return result
}

func requireCodexModelPolicyExcludedObservation(t *testing.T, row FingerprintObservationEntry, source string, length int) {
	t.Helper()
	state := row.CodexTurnState
	require.NotNil(t, state)
	require.True(t, state.AccountEnabled)
	require.False(t, state.Enabled)
	require.Equal(t, "model_excluded", state.MaintenanceReason)
	require.Equal(t, "passthrough", state.Action)
	require.Equal(t, source, state.Source)
	require.Equal(t, length, state.OutboundLength)
	require.Empty(t, state.RenewalReason)
	require.Nil(t, state.ExpiresAt)
}

func TestCodexTurnStateModelPolicyWSBypassesHTTPCacheAcrossLiveRemoval(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough_%t", passthrough), func(t *testing.T) {
			enablePassiveWSFingerprintObservation(t)
			h := newCodexStatePolicyTransportHarness(t, passthrough)
			state := h.svc.codexTurnStateService
			firstFinalModel := "gpt-5.3-codex"
			if passthrough {
				firstFinalModel = "gpt-5.3"
			}
			policy := newCodexStateTestModelPolicy(firstFinalModel, "gpt-5.4")
			state.modelPolicy = policy
			initialMode := h.svc.openAICodexWSStateMode(context.Background(), h.account)
			require.False(t, initialMode.Enabled)
			firstCache := makeCodexWSStateTestToken(10, time.Now().Add(-3*time.Minute))
			secondCache := makeCodexWSStateTestToken(10, time.Now().Add(-150*time.Second))
			seedCodexWSState(t, h.svc, h.account, firstFinalModel, firstCache)
			seedCodexWSState(t, h.svc, h.account, "gpt-5.4", secondCache)
			beforeFirst, beforeSecond := h.record(firstFinalModel), h.record("gpt-5.4")
			policy.set("gpt-5.4")
			var collected atomic.Int64
			state.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				collected.Add(1)
				return CodexTurnStateCollectResult{}, nil
			})
			responseToken := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			h.response.Set(openAIWSTurnStateHeader, responseToken)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server, serverErr := startPassthroughLifecycleServer(t, ctx, h.svc, h.account)
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"guarded-first-header"}}})
			require.NoError(t, err)
			defer client.CloseNow()
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3","input":[]}`)))
			first := requirePassthroughUpstreamWrite(t, h.upstream, 3*time.Second)
			require.Equal(t, firstFinalModel, gjson.GetBytes(first, "model").String())
			require.False(t, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").Exists(), "native WS must never migrate HTTP ticket state into its frames")
			require.Equal(t, "guarded-first-header", (<-h.request).Get(openAIWSTurnStateHeader))
			h.upstream.Send(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_policy_first","model":%q,"usage":{"input_tokens":1,"output_tokens":1}}}`, firstFinalModel))
			readCodexStatePassthroughFrame(t, ctx, client)
			require.Equal(t, beforeFirst, h.record(firstFinalModel), "excluded traffic must not touch an existing cache or its last-business timestamp")
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)))
			second := requirePassthroughUpstreamWrite(t, h.upstream, 3*time.Second)
			require.False(t, gjson.GetBytes(second, "client_metadata.x-codex-turn-state").Exists(), "allowed models also bypass the HTTP-only cache on native WS")
			// HTTP maintenance policy changes cannot interrupt an active WS turn.
			policy.set()
			require.False(t, h.svc.openAICodexWSStateModeChanged(ctx, h.account, initialMode), "model policy changes must not change account-level handshake mode")
			h.upstream.Send(`{"type":"response.output_text.delta","delta":"already sent turn completes"}`)
			require.Equal(t, "response.output_text.delta", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
			h.upstream.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, responseToken))
			readCodexStatePassthroughFrame(t, ctx, client)
			h.upstream.Send(`{"type":"response.completed","response":{"id":"resp_policy_second","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
			require.Equal(t, "response.completed", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
			afterSecond := h.record("gpt-5.4")
			require.Equal(t, beforeSecond, afterSecond)
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)))
			third := requirePassthroughUpstreamWrite(t, h.upstream, 3*time.Second)
			require.False(t, gjson.GetBytes(third, "client_metadata.x-codex-turn-state").Exists(), "the next excluded turn must neither inject the old cache nor inherit the first handshake")
			extended := makeCodexWSStateTestToken(11, time.Now().Add(-time.Minute))
			h.upstream.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, extended))
			readCodexStatePassthroughFrame(t, ctx, client)
			h.upstream.Send(`{"type":"response.completed","response":{"id":"resp_policy_third","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
			readCodexStatePassthroughFrame(t, ctx, client)
			require.Equal(t, afterSecond, h.record("gpt-5.4"))
			_ = client.CloseNow()
			select {
			case <-serverErr:
			case <-ctx.Done():
				t.Fatal("the local connection did not finish")
			}
			require.Empty(t, h.request, "policy/model changes must not redial or replay sent turns")
			require.Empty(t, h.upstream.writes, "the three captured frames are the only upstream sends")
			state.collect(ctx, beforeFirst.Key())
			state.collect(ctx, beforeSecond.Key())
			require.Zero(t, collected.Load())
			requirePassiveWSNoMaintenance(t, state)
			observations := SnapshotFingerprintObservations(0)
			for _, row := range observations {
				require.Nil(t, row.CodexTurnState, "native WS does not create ticket observations")
			}
			serialized, err := json.Marshal(observations)
			require.NoError(t, err)
			for _, token := range []string{firstCache, secondCache, responseToken, extended, "guarded-first-header"} {
				require.NotContains(t, string(serialized), token)
			}
		})
	}
}

func TestCodexTurnStateModelPolicyHTTPExcludedCacheAllPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(path+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				enablePassiveWSFingerprintObservation(t)
				state, repo, account := newCodexStateTestService(t)
				policy := newCodexStateTestModelPolicy("gpt-5.4")
				state.modelPolicy = policy
				account.Concurrency = 1
				if path == "passthrough" {
					account.Extra["openai_passthrough"] = true
				}
				cached := codexStateTestToken(10, state.now().Add(-3*time.Minute))
				seed, err := state.Prepare(context.Background(), account, "gpt-5.4")
				require.NoError(t, err)
				state.Observe(seed, cached)
				require.NoError(t, state.Finish(context.Background(), seed, true))
				before, err := repo.Get(context.Background(), seed.key)
				require.NoError(t, err)
				policy.set()
				var collected atomic.Int64
				state.collector = codexStateTestCollector(func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
					collected.Add(1)
					return CodexTurnStateCollectResult{}, nil
				})
				returned := codexStateTestToken(10, state.now())
				upstream := &httpUpstreamRecorder{resp: codexStateHTTPIntegrationResponse(returned, "metadata", false)}
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
				result, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
				require.NoError(t, err, recorder.Body.String())
				require.NotNil(t, result)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Empty(t, upstream.lastReq.Header.Get(openAICodexTurnStateHeader))
				require.False(t, gjson.GetBytes(upstream.lastBody, "client_metadata.x-codex-turn-state").Exists())
				row := waitCodexModelPolicyTransportRow(t, account.ID, "gpt-5.4", 0, 292)
				requireCodexModelPolicyExcludedObservation(t, row, "", 0)
				require.Equal(t, "metadata", row.CodexTurnState.ResponseSource)
				after, err := repo.Get(context.Background(), seed.key)
				require.NoError(t, err)
				require.Equal(t, before, after, "excluded physical traffic must not refresh, invalidate, or relearn the stored token")
				state.collect(context.Background(), seed.key)
				require.Zero(t, collected.Load())
				requirePassiveWSNoMaintenance(t, state)
			})
		}
	}
}

func TestCodexTurnStateModelPolicyWSRejectedFieldRetryPreservesClientState(t *testing.T) {
	for _, clientState := range []string{"", "guarded-client-frame"} {
		t.Run(fmt.Sprintf("client_state_%t", clientState != ""), func(t *testing.T) {
			enablePassiveWSFingerprintObservation(t)
			h := newCodexStatePolicyTransportHarness(t, false)
			state := h.svc.codexTurnStateService
			policy := newCodexStateTestModelPolicy("gpt-5.4")
			state.modelPolicy = policy
			cached := makeCodexWSStateTestToken(10, time.Now().Add(-3*time.Minute))
			seedCodexWSState(t, h.svc, h.account, "gpt-5.4", cached)
			before := h.record("gpt-5.4")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server, serverErr := startPassthroughLifecycleServer(t, ctx, h.svc, h.account)
			defer server.Close()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], nil)
			require.NoError(t, err)
			defer client.CloseNow()
			body := map[string]any{
				"type": "response.create", "model": "gpt-5.4",
				"input": []map[string]any{
					{"type": "function_call", "name": "echo", "namespace": "retry_test", "call_id": "call_retry", "arguments": "{}"},
					{"type": "function_call_output", "call_id": "call_retry", "output": "ok"},
				},
			}
			if clientState != "" {
				body["client_metadata"] = map[string]any{openAICodexTurnStateHeader: clientState}
			}
			payload, err := json.Marshal(body)
			require.NoError(t, err)
			require.NoError(t, client.Write(ctx, coderws.MessageText, payload))
			first := requirePassthroughUpstreamWrite(t, h.upstream, 3*time.Second)
			require.Equal(t, clientState, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").String())
			require.Equal(t, "retry_test", gjson.GetBytes(first, "input.0.namespace").String())
			require.Empty(t, (<-h.request).Get(openAIWSTurnStateHeader))
			policy.set()
			h.upstream.Send(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"unknown_parameter","message":"Unknown parameter: 'input[0].namespace'.","param":"input[0].namespace"}}`)
			retry := requirePassthroughUpstreamWrite(t, h.upstream, 3*time.Second)
			require.False(t, gjson.GetBytes(retry, "input.0.namespace").Exists(), "the rejected-field retry must actually execute")
			require.Equal(t, clientState, gjson.GetBytes(retry, "client_metadata.x-codex-turn-state").String(), "the rejected-field retry retains only the client's guarded state")
			if clientState == "" {
				require.False(t, gjson.GetBytes(retry, "client_metadata.x-codex-turn-state").Exists())
			}
			returned := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			h.upstream.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, returned))
			require.Equal(t, "response.metadata", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String(), "the rejected error remains internal to the pre-output retry")
			h.upstream.Send(`{"type":"response.completed","response":{"id":"resp_policy_retry","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
			require.Equal(t, "response.completed", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
			after := h.record("gpt-5.4")
			require.Equal(t, before, after)
			_ = client.CloseNow()
			select {
			case <-serverErr:
			case <-ctx.Done():
				t.Fatal("the retry connection did not finish")
			}
			require.Empty(t, h.request, "field retry keeps the original physical socket")
			require.Empty(t, h.upstream.writes, "only the rejected attempt and normalized retry should be sent")
			requirePassiveWSNoMaintenance(t, state)
			for _, row := range SnapshotFingerprintObservations(0) {
				require.Nil(t, row.CodexTurnState)
			}
		})
	}
}
