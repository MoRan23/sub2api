//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type grokSafetyHealthRepo struct {
	AccountRepository
	calls int
}

func (r *grokSafetyHealthRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.calls++
	return nil
}

func TestGrokSafetyMediaAndVoiceFailureRemainBillable(t *testing.T) {
	const refusal = `{"error":{"code":"safety_review_rejected","message":"blocked"},"usage":{"input_tokens":1000,"output_tokens":200}}`
	for _, test := range []struct {
		name  string
		media GrokMediaEndpoint
		voice string
	}{
		{name: "image", media: GrokMediaEndpointImagesGenerations},
		{name: "video", media: GrokMediaEndpointVideosGenerations},
		{name: "tts", voice: "tts"},
		{name: "stt", voice: "stt"},
	} {
		for _, status := range []int{http.StatusForbidden, http.StatusOK} {
			t.Run(test.name+"/"+http.StatusText(status), func(t *testing.T) {
				upstream := &grokMediaContentUpstreamStub{response: &http.Response{
					StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"upstream-refusal"}},
					Body: io.NopCloser(strings.NewReader(refusal)),
				}}
				health := &grokSafetyHealthRepo{}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: health}
				account := grokMediaContentTestAccount()
				account.Schedulable = true
				c, recorder := grokMediaContentTestContext(http.MethodPost, "https://gateway.example/v1/request", nil)
				ctx := WithGrokSafetyFee(c.Request.Context(), .4)
				c.Request = c.Request.WithContext(ctx)
				body := []byte(`{"model":"grok-test","prompt":"fixture","text":"fixture"}`)
				var result *OpenAIForwardResult
				var err error
				if test.voice != "" {
					result, err = svc.ForwardGrokVoice(ctx, c, account, test.voice, body, "application/json")
				} else {
					result, err = svc.ForwardGrokMedia(ctx, c, account, test.media, "", body, "application/json")
				}
				require.ErrorIs(t, err, ErrGrokSafetyRejected)
				require.NotNil(t, result)
				require.NotNil(t, result.GrokSafetyRejection)
				require.NotEmpty(t, result.GrokSafetyRejection.CallID)
				require.Equal(t, .4, result.GrokSafetyRejection.Fee)
				require.Equal(t, status, result.GrokSafetyRejection.HTTPStatus)
				require.Equal(t, "upstream-refusal", result.GrokSafetyRejection.UpstreamRequestID)
				require.Equal(t, 1000, result.Usage.InputTokens)
				require.Equal(t, 200, result.Usage.OutputTokens)
				require.Zero(t, result.ImageCount)
				require.Zero(t, result.VideoCount)
				require.Zero(t, result.SearchCount)
				require.Nil(t, result.AudioUsage)
				require.Len(t, upstream.requests, 1, "precise refusal never retries")
				require.Zero(t, health.calls)
				require.True(t, account.Schedulable)
				require.Nil(t, account.TempUnschedulableUntil)
				require.Contains(t, recorder.Body.String(), GrokSafetyRejectionCode)
				billing, input, durable, ordinary := grokSafetyBillingFixture(t)
				input.Result = result
				// A request for media has no trustworthy successful unit to charge.
				// Only the final token usage and fixed fee survive this error.
				input.APIKey.Group.ModelPricing[0].Models = []string{result.Model}
				require.NoError(t, billing.RecordUsage(ctx, input))
				require.Len(t, durable.logs, 1)
				require.InDelta(t, 1, durable.logs[0].ActualCost, 1e-12)
				require.Zero(t, ordinary.calls)
			})
		}
	}
}

type grokSafetyRealtimeFixture struct {
	reads  atomic.Int32
	closes atomic.Int32
}

func (*grokSafetyRealtimeFixture) WriteJSON(context.Context, any) error { return nil }
func (*grokSafetyRealtimeFixture) Ping(context.Context) error           { return nil }
func (f *grokSafetyRealtimeFixture) Close() error                       { f.closes.Add(1); return nil }
func (f *grokSafetyRealtimeFixture) ReadMessage(context.Context) ([]byte, error) {
	if f.reads.Add(1) == 1 {
		return []byte(`{"type":"error","error":{"code":"safety_review_rejected","message":"blocked"}}`), nil
	}
	return []byte(`{"type":"response.done","response":{"usage":{"input_tokens":99999,"output_tokens":99999}}}`), nil
}

func TestGrokSafetyRealtimeStopsAtFirstRejection(t *testing.T) {
	type outcome struct {
		result *OpenAIForwardResult
		err    error
		audio  bool
	}
	done := make(chan outcome, 1)
	upstream := &grokSafetyRealtimeFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer client.CloseNow()
		ctx := WithGrokSafetyFee(r.Context(), .25)
		StartGrokSafetyAttempt(ctx, http.StatusSwitchingProtocols, nil)
		svc := &OpenAIGatewayService{}
		audio, err := svc.ProxyGrokRealtimeConn(ctx, nil, client, &GrokRealtimeUpstream{conn: upstream})
		result, err := FinalizeGrokSafetyRejection(ctx, nil, err)
		done <- outcome{result: result, err: err, audio: audio}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	_, payload, err := client.Read(ctx)
	require.NoError(t, err)
	require.Contains(t, string(payload), GrokSafetyRejectionCode)
	select {
	case observed := <-done:
		require.ErrorIs(t, observed.err, ErrGrokSafetyRejected)
		require.False(t, observed.audio)
		require.NotNil(t, observed.result.GrokSafetyRejection)
		require.Equal(t, .25, observed.result.GrokSafetyRejection.Fee)
		require.Zero(t, observed.result.Usage, "do not read the later success or usage frame")
		require.Equal(t, int32(1), upstream.reads.Load())
		require.Positive(t, upstream.closes.Load())
	case <-ctx.Done():
		t.Fatal("realtime readers did not stop after rejection")
	}
}

func TestGrokSafetyNativeSearchErrorKeepsFinalTokens(t *testing.T) {
	const payload = `{"error":{"code":"safety_review_rejected","message":"blocked"},"usage":{"input_tokens":1000,"output_tokens":200},"output":[{"type":"web_search_call","status":"completed"}]}`
	upstream := &grokMediaContentUpstreamStub{response: &http.Response{
		StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(payload)),
	}}
	svc := &GatewayService{httpUpstream: upstream}
	ctx := WithGrokSafetyFee(context.Background(), .4)
	got, err := svc.DoGrokNativeResponsesJSON(ctx, grokMediaContentTestAccount(), []byte(`{"model":"grok-test","input":"fixture"}`))
	require.ErrorIs(t, err, ErrGrokSafetyRejected)
	require.JSONEq(t, payload, string(got))
	require.Len(t, upstream.requests, 1)
	result, err := FinalizeGrokSafetyRejection(ctx, &OpenAIForwardResult{Model: "grok-test", SearchCount: 1, WebSearchCalls: 1}, err)
	require.ErrorIs(t, err, ErrGrokSafetyRejected)
	require.Zero(t, result.SearchCount)
	require.Zero(t, result.WebSearchCalls)
	accounting, input, durable, ordinary := grokSafetyBillingFixture(t)
	input.Result = result
	require.NoError(t, accounting.RecordUsage(ctx, input))
	require.Len(t, durable.logs, 1)
	require.InDelta(t, 1, durable.logs[0].ActualCost, 1e-12)
	require.Zero(t, ordinary.calls)
}
