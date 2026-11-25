//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPassthroughReadFailurePreservesTransportCause(t *testing.T) {
	for _, tc := range []struct {
		name          string
		prefix        string
		cause         error
		wantTransport bool
		wantOutput    bool
	}{
		{name: "unexpected EOF", cause: io.ErrUnexpectedEOF, wantTransport: true},
		{name: "HTTP2 reset", cause: errors.New("stream error: stream ID 7; INTERNAL_ERROR; received from peer"), wantTransport: true},
		{name: "protocol error", cause: errors.New("malformed stream")},
		{
			name:       "after output does not replay",
			prefix:     "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n",
			cause:      io.ErrUnexpectedEOF,
			wantOutput: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"X-Request-Id": {"request-id"}},
				Body: io.NopCloser(io.MultiReader(strings.NewReader(tc.prefix),
					&openAICompatBufferedReadErrorCloser{err: tc.cause})),
			}
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c,
				&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, time.Now(), "model", "model")
			require.Error(t, err)
			var failover *UpstreamFailoverError
			if tc.wantOutput {
				require.NotErrorAs(t, err, &failover)
				require.ErrorIs(t, err, tc.cause)
				require.Contains(t, rec.Body.String(), "hello")
				return
			}
			require.ErrorAs(t, err, &failover)
			require.Equal(t, tc.wantTransport, failover.TransportFailure)
			require.Equal(t, !tc.wantTransport, failover.ShouldReportAccountScheduleFailure())
			_, _, healthFailure := classifyOpenAIAPIKeyHealthFailure(err)
			require.Equal(t, !tc.wantTransport, healthFailure)
			require.Empty(t, rec.Body.String())
		})
	}
}
