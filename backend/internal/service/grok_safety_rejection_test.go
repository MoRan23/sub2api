package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/groksafetyobserver"
	"github.com/stretchr/testify/require"
)

func TestGrokSafetyRejectionExactPaths(t *testing.T) {
	for _, tt := range []struct {
		body string
		want bool
	}{
		{`{"error":{"code":"safety_review_rejected"}}`, true},
		{`{"response":{"error":{"code":"safety_review_rejected"}}}`, true},
		{`{"error":{"code":"safety_review_\u0072ejected"}}`, true},
		{`{"error":{"code":"SAFETY_REVIEW_REJECTED"}}`, false},
		{`{"error":{"code":" safety_review_rejected"}}`, false},
		{`{"code":"safety_review_rejected"}`, false},
		{`{"message":"safety_review_rejected"}`, false},
		{`{"output":[{"error":{"code":"safety_review_rejected"}}]}`, false},
		{`{"Error":{"code":"safety_review_rejected"}}`, false},
		{`{"error":{"code":"safety_review_rejected"}`, false},
	} {
		require.Equal(t, tt.want, IsGrokSafetyRejectionJSON([]byte(tt.body)), tt.body)
	}
}

func TestGrokSafetyRejectionFinalUsageStickyAndUnits(t *testing.T) {
	ctx := WithGrokSafetyFee(context.Background(), 0.25)
	StartGrokSafetyAttempt(ctx, 200, http.Header{"X-Request-Id": []string{"upstream-1"}})
	ObserveGrokSafetyMessage(ctx, []byte(`{"type":"response.output_text.delta","usage":{"input_tokens":100,"output_tokens":50}}`))
	ObserveGrokSafetyMessage(ctx, []byte(`{"type":"response.failed","response":{"error":{"code":"safety_review_rejected"},"usage":{"input_tokens":12,"output_tokens":3}}}`))
	// A later malformed provider success cannot overturn the rejection or its usage.
	ObserveGrokSafetyMessage(ctx, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":900,"output_tokens":500}}}`))
	result, err := FinalizeGrokSafetyRejection(ctx, &OpenAIForwardResult{ImageCount: 3, VideoCount: 2, SearchCount: 4, WebSearchCalls: 5}, nil)
	require.ErrorIs(t, err, ErrGrokSafetyRejected)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 0, result.ImageCount)
	require.Equal(t, 0, result.VideoCount)
	require.Equal(t, 0, result.SearchCount)
	require.Equal(t, 0, result.WebSearchCalls)
	require.Equal(t, 0.25, result.GrokSafetyRejection.Fee)
	require.Equal(t, "upstream-1", result.GrokSafetyRejection.UpstreamRequestID)
	require.NotEmpty(t, result.RequestID)
	require.Equal(t, result.RequestID, result.GrokSafetyRejection.CallID)
}

func TestGrokSafetyRejectionNoProvisionalOrInvalidUsage(t *testing.T) {
	for _, usage := range []string{"", `,"usage":{"input_tokens":-1}`, `,"usage":{"input_tokens":"12"}`, `,"usage":{"input_tokens":1.5}`} {
		ctx := WithGrokSafetyFee(context.Background(), 0)
		StartGrokSafetyAttempt(ctx, 200, nil)
		ObserveGrokSafetyMessage(ctx, []byte(`{"type":"response.output_text.delta","usage":{"input_tokens":100}}`))
		ObserveGrokSafetyMessage(ctx, []byte(`{"error":{"code":"safety_review_rejected"}`+usage+`}`))
		result, err := FinalizeGrokSafetyRejection(ctx, nil, nil)
		require.ErrorIs(t, err, ErrGrokSafetyRejected)
		require.Zero(t, result.Usage)
		require.Zero(t, result.GrokSafetyRejection.Fee)
	}
}

func TestGrokSafetyBodyTransparentChunkedSSE(t *testing.T) {
	body := "event: response.failed\r\ndata: {\"response\":{\"error\":{\"code\":\"safety_review_rejected\"},\n" +
		"data: \"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\r\n\r\n"
	for _, chunk := range []int{1, 7, 64, len(body)} {
		ctx := WithGrokSafetyFee(context.Background(), 0.1)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://test.invalid/v1/responses", nil)
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
		groksafetyobserver.Attach(req, resp, grokSafetyHolderFromContext(ctx).begin())
		var got bytes.Buffer
		buf := make([]byte, chunk)
		for {
			n, err := resp.Body.Read(buf)
			got.Write(buf[:n])
			if err != nil {
				require.ErrorIs(t, err, io.EOF)
				break
			}
		}
		require.NoError(t, resp.Body.Close())
		require.Equal(t, body, got.String())
		result, err := FinalizeGrokSafetyRejection(ctx, nil, nil)
		require.ErrorIs(t, err, ErrGrokSafetyRejected)
		require.Equal(t, 7, result.Usage.InputTokens)
	}
}

func TestGrokSafetyJSONObservedBeforeEOFAndCloseDoesNotDrain(t *testing.T) {
	ctx := WithGrokSafetyFee(context.Background(), 0.1)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://test.invalid/v1/responses", nil)
	payload := `{"error":{"code":"safety_review_rejected"},"usage":{"prompt_tokens":4,"completion_tokens":1}}`
	reader := &grokSafetyCountingBody{Reader: strings.NewReader(payload)}
	resp := &http.Response{StatusCode: 403, Header: make(http.Header), Body: reader}
	groksafetyobserver.Attach(req, resp, grokSafetyHolderFromContext(ctx).begin())
	data := make([]byte, len(payload))
	n, err := resp.Body.Read(data)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.True(t, GrokSafetyRejected(ctx))
	calls := reader.reads
	require.NoError(t, resp.Body.Close())
	require.Equal(t, calls, reader.reads)
}

type grokSafetyCountingBody struct {
	*strings.Reader
	reads int
}

func (b *grokSafetyCountingBody) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (b *grokSafetyCountingBody) Close() error               { return nil }

func TestGrokSafetyLargePayloadUsesExistingParserAndAttemptsAreIsolated(t *testing.T) {
	ctx := WithGrokSafetyFee(context.Background(), 0.2)
	StartGrokSafetyAttempt(ctx, 200, nil)
	data := []byte(`{"padding":"` + strings.Repeat("x", grokSafetyObservationLimit+1) + `","error":{"code":"safety_review_rejected"},"usage":{"input_tokens":5}}`)
	ObserveGrokSafetyPayload(ctx, data, true)
	rejected := GrokSafetyRejectionFromContext(ctx)
	require.NotNil(t, rejected)
	StartGrokSafetyAttempt(ctx, 200, nil)
	require.False(t, GrokSafetyRejected(ctx))
	ObserveGrokSafetyPayload(ctx, []byte(`{"error":{"code":"safety_review_rejected"}}`), true)
	require.NotEqual(t, rejected.CallID, GrokSafetyRejectionFromContext(ctx).CallID)
}

func TestGrokSafetyFreshGroupAndConcurrentEvidence(t *testing.T) {
	group := &Group{Platform: PlatformGrok, GrokSafetyFeeEnabled: false}
	ctx := WithGrokSafetyGroup(context.Background(), group)
	StartGrokSafetyAttempt(ctx, 200, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ObserveGrokSafetyMessage(ctx, []byte(`{"error":{"code":"safety_review_rejected"}}`))
			_, _ = FinalizeGrokSafetyRejection(ctx, nil, errors.New("client disconnected"))
		}()
	}
	wg.Wait()
	require.True(t, GrokSafetyRejected(ctx))
	require.False(t, GrokSafetyRejected(freshGrokSafetyGroup(ctx, group)))
	require.Nil(t, grokSafetyHolderFromContext(WithGrokSafetyGroup(context.Background(), &Group{Platform: PlatformOpenAI})))
}

func TestGrokSafetyParserTakeoverAvoidsDuplicateBodyObservation(t *testing.T) {
	ctx := WithGrokSafetyFee(context.Background(), 0.2)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://test.invalid/v1/responses", nil)
	reader := io.NopCloser(strings.NewReader(`{"error":{"code":"safety_review_rejected"}}`))
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}
	groksafetyobserver.Attach(req, resp, grokSafetyHolderFromContext(ctx).begin())
	before := grokSafetyHolderFromContext(ctx).current()
	ObserveGrokSafetyHTTPResponse(ctx, req, resp)
	ObserveGrokSafetyHTTPResponse(ctx, req, resp)
	require.Same(t, before, grokSafetyHolderFromContext(ctx).current())
	require.Equal(t, reader, resp.Body)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.False(t, GrokSafetyRejected(ctx))
	ObserveGrokSafetyPayload(ctx, data, true)
	require.True(t, GrokSafetyRejected(ctx))
}

func TestGrokSafetyAuxiliaryRequestDoesNotStartAttempt(t *testing.T) {
	ctx := WithGrokSafetyFee(context.Background(), 0.1)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://test.invalid/oauth/token", nil)
	require.Nil(t, groksafetyobserver.Begin(req))
	require.Nil(t, grokSafetyHolderFromContext(ctx).current())
	require.NotNil(t, groksafetyobserver.Begin(PrepareGrokSafetyRequest(req)))
}

func TestGrokSafetyAlreadyReadUsageOnlyAfterRejection(t *testing.T) {
	ctx := WithGrokSafetyFee(context.Background(), 0.2)
	StartGrokSafetyAttempt(ctx, 200, nil)
	ObserveGrokSafetyMessage(ctx, []byte(`{"error":{"code":"safety_review_rejected"}}`))
	ObserveGrokSafetyMessage(ctx, []byte(`{"choices":[],"usage":{"prompt_tokens":13,"completion_tokens":2}}`))
	result, err := FinalizeGrokSafetyRejection(ctx, nil, nil)
	require.ErrorIs(t, err, ErrGrokSafetyRejected)
	require.Equal(t, 13, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	ObserveGrokSafetyMessage(ctx, []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":99}}}`))
	result, err = FinalizeGrokSafetyRejection(ctx, nil, nil)
	require.ErrorIs(t, err, ErrGrokSafetyRejected)
	require.Equal(t, 13, result.Usage.InputTokens)
}

func BenchmarkGrokSafetyNormalFrame(b *testing.B) {
	for _, size := range []int{128, 32 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			data := []byte(`{"choices":[{"delta":{"content":"` + strings.Repeat("x", size) + `"}}]}`)
			ctx := WithGrokSafetyFee(context.Background(), 0.1)
			StartGrokSafetyAttempt(ctx, 200, nil)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ObserveGrokSafetyPayload(ctx, data, false)
			}
		})
	}
}
