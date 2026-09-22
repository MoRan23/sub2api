package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOriginalSyncIdentityUsesUntouchedCapture(t *testing.T) {
	const session = "01989f44-7c00-7000-8000-000000000110"
	const thread = "01989f44-7c00-7000-8000-000000000111"
	for _, source := range []string{"body", "header", "absent", "alias"} {
		t.Run(source, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
			body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
			switch source {
			case "body":
				body = []byte(`{"model":"gpt-5.4","client_metadata":{"session_id":"` + session + `","thread_id":"` + thread + `"}}`)
				c.Request.Header.Set("thread-id", "01989f44-7c00-7000-8000-000000000112")
			case "header":
				c.Request.Header.Set("session-id", session)
				c.Request.Header.Set("thread-id", thread)
			case "alias":
				body = []byte(`{"client_metadata":{"session_id":"legacy-session","thread_id":"legacy-thread"}}`)
			}
			SetOpenAIOAuthIdentityCapture(c, CaptureOpenAIOAuthIdentityWithEndpointAlias(c, body, "legacy-seed"))
			// Compact removes metadata; later rewrite artifacts are not ingress IDs.
			c.Request.Header.Set("session-id", "01989f44-7c00-7000-8000-000000000113")
			c.Request.Header.Set("thread-id", "01989f44-7c00-7000-8000-000000000114")
			if source == "body" || source == "header" {
				require.Equal(t, session, originalOpenAISyncSession(c, nil))
				require.Equal(t, thread, originalOpenAISyncThread(c, nil))
			} else {
				require.Empty(t, originalOpenAISyncSession(c, nil))
				require.Empty(t, originalOpenAISyncThread(c, nil))
			}
		})
	}
}
