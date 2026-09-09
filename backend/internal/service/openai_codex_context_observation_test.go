package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCodexContextManagementObservationSharesFingerprintToggleAndScrubs(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("api_key", &APIKey{ID: 3, UserID: 7, Name: "pat"})
	BeginCodexContextManagementObservation(c, "history", "/alpha/history/v2/list_windows")
	entry := codexContextObservationFromContext(c)
	require.NotNil(t, entry)
	entry.Status = "delivered"
	entry.RewriteFields = []string{"body.context.session_id"}
	RecordCodexContextManagementResult(c, "history", "/alpha/history/v2/list_windows", "delivered", 200, 10, "")
	page := PageCodexContextManagementObservations(1, 20)
	require.Equal(t, 1, page.Total)
	require.Equal(t, 1, page.Summary.Successes)
	require.Equal(t, int64(10), page.Items[0].DeliveredBytes)
	require.Equal(t, int64(7), page.Items[0].UserID)

	SetFingerprintObservationEnabled(false)
	page = PageCodexContextManagementObservations(1, 20)
	require.False(t, IsFingerprintObservationEnabled())
	require.Empty(t, page.Items)
	for _, e := range globalCodexContextObservation.ring {
		require.True(t, reflect.DeepEqual(e, CodexContextManagementObservationEntry{}))
	}
}

func TestCodexContextManagementObservationDoesNotCrossCapturePeriods(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	BeginCodexContextManagementObservation(c, "notes", "/alpha/notes/v2/write_file")
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	RecordCodexContextManagementResult(c, "notes", "", "delivered", 200, 1, "")
	// A subsequent fallback attempt is part of the same old logical request.
	BeginCodexContextManagementObservation(c, "notes", "/alpha/notes/v2/write_file")
	RecordCodexContextManagementResult(c, "notes", "", "delivered", 200, 1, "")
	require.Empty(t, PageCodexContextManagementObservations(1, 20).Items)

	SetFingerprintObservationEnabled(false)
	late, _ := gin.CreateTestContext(httptest.NewRecorder())
	BeginCodexContextManagementObservation(late, "whoami", "/v1/user-auth-credential/whoami")
	SetFingerprintObservationEnabled(true)
	RecordCodexContextManagementResult(late, "whoami", "", "delivered", 200, 0, "")
	require.Empty(t, PageCodexContextManagementObservations(1, 20).Items)
}

func TestCodexContextObservationCapturesOnlySafeChangedIdentity(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	BeginCodexContextManagementObservation(c, "history", "/alpha/history/v2/list_windows")
	before := []byte(`{"session_id":"private-client-text","thread_id":"same-invalid","content":"private-note","context":{"window_number":2},"client_metadata":{"context_window_id":"old-secret"}}`)
	after := []byte(`{"session_id":"018f4f65-8f4e-7a8e-8d7f-5b0f7d4c8e91","thread_id":"changed-invalid","content":"private-note","context":{"window_number":3}}`)
	observeCodexContextIdentityRewrite(c, before, after, http.Header{"Authorization": {"private-token"}}, nil)
	RecordCodexContextManagementResult(c, "history", "", "delivered", 200, 10, "")
	page := PageCodexContextManagementObservations(1, 20)
	require.Len(t, page.Items, 1)
	require.Equal(t, []string{"body.client_metadata.context_window_id", "body.context.window_number", "body.session_id", "body.thread_id"}, page.Items[0].RewriteFields)
	require.Equal(t, "018f4f65-8f4e-7a8e-8d7f-5b0f7d4c8e91", page.Items[0].SessionID)
	raw, err := json.Marshal(page)
	require.NoError(t, err)
	for _, secret := range []string{"private-client-text", "private-note", "private-token", "same-invalid", "changed-invalid", "old-secret"} {
		require.NotContains(t, string(raw), secret)
	}
	page.Items[0].RewriteFields[0] = "mutated"
	page.Items[0].Rewrites[0].Before = "mutated"
	copy := PageCodexContextManagementObservations(1, 20)
	require.NotEqual(t, "mutated", copy.Items[0].RewriteFields[0])
	require.NotEqual(t, "mutated", copy.Items[0].Rewrites[0].Before)
}

func TestCodexContextObservationRingIsBoundedAndSummaryTracksAttempts(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	for i := 0; i < fingerprintObservationCapacity+2; i++ {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		BeginCodexContextManagementObservation(c, "history", "/alpha/history/v2/list_windows")
		entry := codexContextObservationFromContext(c)
		entry.Fallback = true
		RecordCodexContextManagementResult(c, "history", "", "failed", 503, 0, "upstream_5xx")
	}
	page := PageCodexContextManagementObservations(999, 1000)
	require.Equal(t, fingerprintObservationCapacity, page.Total)
	require.Equal(t, 5, page.Page)
	require.Equal(t, 100, page.PageSize)
	require.Equal(t, fingerprintObservationCapacity, page.Summary.Failures)
	require.Equal(t, fingerprintObservationCapacity, page.Summary.Fallbacks)
	require.Equal(t, uint64(3), page.Items[len(page.Items)-1].SequenceID)
}

func TestCodexContextObservationConcurrentCaptureToggle(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				BeginCodexContextManagementObservation(c, "whoami", "/v1/user-auth-credential/whoami")
				RecordCodexContextManagementResult(c, "whoami", "", "delivered", 200, 10, "")
				_ = PageCodexContextManagementObservations(1, 20)
			}
		}()
	}
	for i := 0; i < 20; i++ {
		SetFingerprintObservationEnabled(false)
		SetFingerprintObservationEnabled(true)
	}
	wg.Wait()
	SetFingerprintObservationEnabled(false)
	require.Zero(t, PageCodexContextManagementObservations(1, 20).Summary.Total)
}

func TestCodexAuxiliaryObservationErrorKindDoesNotReturnErrorMessage(t *testing.T) {
	require.Equal(t, "request_error", codexAuxiliaryObservationErrorKind(fmt.Errorf("secret token and URL")))
}

func TestCodexContextObservationHistoryFallbackWaitsForDelivery(t *testing.T) {
	for _, firstStatus := range []int{http.StatusServiceUnavailable, http.StatusForbidden} {
		t.Run(fmt.Sprint(firstStatus), func(t *testing.T) {
			SetFingerprintObservationEnabled(false)
			SetFingerprintObservationEnabled(true)
			t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
			accounts := []Account{
				{ID: 901, Name: "first", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "not-recorded"}},
				{ID: 902, Name: "second", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "not-recorded"}},
			}
			calls := 0
			upstream := codexModelsHTTPUpstreamStub{do: func(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
				calls++
				status := http.StatusOK
				if accountID == 901 {
					status = firstStatus
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"items":[]}`))}, nil
			}}
			svc := &OpenAIGatewayService{
				accountRepo: &countingCodexModelsAccountRepo{accounts: accounts}, httpUpstream: &upstream,
				cfg: &config.Config{JWT: config.JWTConfig{Secret: "context-observation-test-secret"}},
			}
			key := &APIKey{ID: 71, UserID: 70}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/backend-api/codex/alpha/history/v2/list_windows", nil)
			c.Set("api_key", key)
			body := []byte(`{"context":{"session_id":"018f4f65-8f4e-7a8e-8d7f-5b0f7d4c8e91"}}`)
			BeginCodexContextManagementObservation(c, "history", "/alpha/history/v2/list_windows")
			resp, err := svc.ForwardCodexHistoryNotes(context.Background(), c, key, "/alpha/history/v2/list_windows", body)
			require.NoError(t, err)
			require.NotNil(t, resp)
			defer resp.Body.Close()
			before := PageCodexContextManagementObservations(1, 20)
			if firstStatus == http.StatusForbidden {
				require.Equal(t, 1, calls)
				require.Empty(t, before.Items)
				RecordCodexContextManagementResult(c, "history", "", "rejected", resp.StatusCode, 0, "upstream_rejected")
				after := PageCodexContextManagementObservations(1, 20)
				require.Len(t, after.Items, 1)
				require.Equal(t, 403, after.Items[0].UpstreamHTTPStatus)
				return
			}
			require.Equal(t, 2, calls)
			require.Len(t, before.Items, 1, "only failed first attempt is recorded before body delivery")
			require.Equal(t, 503, before.Items[0].UpstreamHTTPStatus)
			require.True(t, before.Items[0].UpstreamSent)
			RecordCodexContextManagementResult(c, "history", "", "delivered", 200, 12, "")
			after := PageCodexContextManagementObservations(1, 20)
			require.Len(t, after.Items, 2)
			require.Equal(t, "delivered", after.Items[0].Status)
			require.Equal(t, int64(902), after.Items[0].AccountID)
			require.True(t, after.Items[0].Fallback)
			require.Equal(t, 2, after.Items[0].Attempt)
		})
	}
}

func TestCodexContextObservationOnlyAllowsKnownPath(t *testing.T) {
	require.Equal(t, "/alpha/notes/v2/[operation]", codexContextObservationPath("https://example/alpha/notes/v2/read_file?x=1"))
	require.Equal(t, "/alpha/history/v2/[operation]", codexContextObservationPath("/alpha/history/v2/private"))
	require.Equal(t, "[unknown]", codexContextObservationPath("/internal/token"))
}

func TestCodexContextObservationThreadHintPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"direct", "/alpha/notes/v2/thread_hint", "/alpha/notes/v2/thread_hint"},
		{"v1_alias", "/v1/alpha/notes/v2/thread_hint", "/alpha/notes/v2/thread_hint"},
		{"codex_alias", "/backend-api/codex/alpha/notes/v2/thread_hint", "/alpha/notes/v2/thread_hint"},
		{"query", "/alpha/notes/v2/thread_hint?x=1", "/alpha/notes/v2/[operation]"},
		{"query_on_alias", "/backend-api/codex/alpha/notes/v2/thread_hint?x=1", "/alpha/notes/v2/[operation]"},
		{"unknown_operation", "/alpha/notes/v2/thread_hint_extra", "/alpha/notes/v2/[operation]"},
		{"unknown_suffix", "/v1/alpha/notes/v2/thread_hint/private", "/alpha/notes/v2/[operation]"},
		{"history", "/alpha/history/v2/thread_hint", "/alpha/history/v2/[operation]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, codexContextObservationPath(tc.path))
		})
	}
}
