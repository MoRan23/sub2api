package httputil

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/stretchr/testify/require"
)

func TestRequestBodyBudgetPreservesBufferedObservation(t *testing.T) {
	for _, lenient := range []bool{false, true} {
		name, raw := "decoded", `{"text":"`+strings.Repeat("x", 4096)+`"}`
		if lenient {
			name, raw = "normalized", "{\"text\":\""+strings.Repeat("\n", 1024)+"\"}"
		}
		t.Run(name, func(t *testing.T) {
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			_, err := writer.Write([]byte(raw))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			var counters runtimeobs.Counters
			budget := NewBodyMemoryBudget(1 << 20)
			req := httptest.NewRequest("POST", "/", bytes.NewReader(compressed.Bytes()))
			ctx, releaseBudget := WithRequestBodyMemoryBudget(req.Context(), budget)
			defer releaseBudget()
			req, releaseObservation := counters.TrackRequestBody(req.WithContext(ctx))
			defer releaseObservation()
			req.Header.Set("Content-Encoding", "gzip")
			var body []byte
			if lenient {
				body, err = ReadLenientJSONRequestBodyWithPrealloc(req, 1<<20)
			} else {
				body, err = ReadRequestBodyWithPrealloc(req)
			}
			require.NoError(t, err)
			expected := raw
			if lenient {
				expected = strings.ReplaceAll(raw, "\n", `\u000a`)
			}
			require.Equal(t, expected, string(body))
			require.EqualValues(t, len(body), counters.Snapshot().ActiveBodyBytes)
			require.EqualValues(t, 1, counters.Snapshot().ActiveBodies)
			reserved := budget.used
			req.Body = NewPrereadBody(body)
			again, err := ReadLenientJSONRequestBodyWithPrealloc(req, 1<<20)
			require.NoError(t, err)
			require.Equal(t, body, again)
			require.Equal(t, reserved, budget.used)
			require.EqualValues(t, len(body), counters.Snapshot().ActiveBodyBytes)
			releaseBudget()
			require.Zero(t, budget.used)
			// Budget release and the logical-body lifetime are independent.
			require.EqualValues(t, len(body), counters.Snapshot().ActiveBodyBytes)
			releaseObservation()
			require.Zero(t, counters.Snapshot().ActiveBodies)
			require.Zero(t, counters.Snapshot().ActiveBodyBytes)
		})
	}
}
