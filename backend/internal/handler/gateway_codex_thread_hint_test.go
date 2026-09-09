package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const codexThreadHintTestPath = "/alpha/notes/v2/thread_hint"

func beginCodexThreadHintTestObservation(t *testing.T, c *gin.Context, path string) {
	t.Helper()
	service.SetFingerprintObservationEnabled(false)
	service.SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { service.SetFingerprintObservationEnabled(false) })
	service.BeginCodexContextManagementObservation(c, "notes", path)
}

func TestCodexContextObservationNewThreadHintClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		newSession bool
		status     int
		body       string
		encoding   string
		normalized bool
	}{
		{name: "empty", path: codexThreadHintTestPath, newSession: true, status: 404, normalized: true},
		{name: "whitespace", path: codexThreadHintTestPath, newSession: true, status: 404, body: " \n\t", normalized: true},
		{name: "not_found", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Not found"}`, normalized: true},
		{name: "not_found_whitespace_and_case", path: codexThreadHintTestPath, newSession: true, status: 404, body: ` {"detail":" NOT FOUND "} `, normalized: true},
		{name: "existing_session", path: codexThreadHintTestPath, status: 404, body: `{"detail":"Not found"}`},
		{name: "existing_session_empty", path: codexThreadHintTestPath, status: 404},
		{name: "other_notes_operation", path: "/alpha/notes/v2/read_file", newSession: true, status: 404, body: `{"detail":"Not found"}`},
		{name: "history_operation", path: "/alpha/history/v2/thread_hint", newSession: true, status: 404, body: `{"detail":"Not found"}`},
		{name: "path_suffix", path: codexThreadHintTestPath + "/other", newSession: true, status: 404, body: `{"detail":"Not found"}`},
		{name: "forbidden", path: codexThreadHintTestPath, newSession: true, status: 403, body: `{"detail":"Not found"}`},
		{name: "server_error", path: codexThreadHintTestPath, newSession: true, status: 500, body: `{"detail":"Not found"}`},
		{name: "success", path: codexThreadHintTestPath, newSession: true, status: 200, body: `{"text":"existing hint"}`},
		{name: "different_error", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Permission denied"}`},
		{name: "extra_error_field", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Not found","error":"missing account"}`},
		{name: "wrong_detail_type", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":null}`},
		{name: "empty_object", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{}`},
		{name: "invalid_json", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Not found"`},
		{name: "trailing_json", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Not found"}{"error":"unexpected"}`},
		{name: "oversized", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Not found"}` + strings.Repeat(" ", 2048)},
		{name: "encoded_body", path: codexThreadHintTestPath, newSession: true, status: 404, body: `{"detail":"Not found"}`, encoding: "gzip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/backend-api/codex"+tc.path, nil)
			c.Set("codex_new_session_thread_hint", tc.newSession)
			beginCodexThreadHintTestObservation(t, c, tc.path)
			header := http.Header{
				"Content-Type":   {"application/problem+json"},
				"Content-Length": {strconv.Itoa(len(tc.body))},
				"X-Request-Id":   {"thread-hint-test"},
			}
			if tc.encoding != "" {
				header.Set("Content-Encoding", tc.encoding)
			}
			writeCodexHistoryNotesResponse(c, &http.Response{
				StatusCode: tc.status, Header: header, ContentLength: int64(len(tc.body)),
				Body: io.NopCloser(strings.NewReader(tc.body)),
			}, "notes", tc.path)
			c.Writer.WriteHeaderNow()

			wantStatus, wantObservation, wantErrorKind := tc.status, "rejected", "upstream_rejected"
			if tc.status >= 200 && tc.status < 300 {
				wantObservation, wantErrorKind = "delivered", ""
			}
			if tc.normalized {
				wantStatus, wantObservation, wantErrorKind = http.StatusOK, "delivered", ""
				require.JSONEq(t, `{"text":""}`, recorder.Body.String())
				require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
				if length := recorder.Header().Get("Content-Length"); length != "" {
					require.Equal(t, strconv.Itoa(recorder.Body.Len()), length)
				}
			} else {
				require.Equal(t, tc.body, recorder.Body.String(), "nonempty or non-bootstrap errors must pass through unchanged")
				require.Equal(t, header.Get("Content-Type"), recorder.Header().Get("Content-Type"))
				require.Equal(t, header.Get("Content-Length"), recorder.Header().Get("Content-Length"))
				require.Equal(t, tc.encoding, recorder.Header().Get("Content-Encoding"))
			}
			require.Equal(t, wantStatus, recorder.Code)
			require.Equal(t, "thread-hint-test", recorder.Header().Get("X-Request-Id"))
			page := service.PageCodexContextManagementObservations(1, 20)
			require.Len(t, page.Items, 1)
			require.Equal(t, wantObservation, page.Items[0].Status)
			require.Equal(t, wantErrorKind, page.Items[0].ErrorKind)
			require.Equal(t, wantStatus, page.Items[0].HTTPStatus)
			require.Equal(t, int64(recorder.Body.Len()), page.Items[0].DeliveredBytes)
			if wantObservation == "delivered" {
				require.Equal(t, 1, page.Summary.Successes)
				require.Zero(t, page.Summary.Failures)
			}
		})
	}
}

func TestCodexContextObservationNewThreadHintDeliveryFailure(t *testing.T) {
	const notFoundBody = `{"detail":"Not found"}`
	for _, tc := range []struct {
		name          string
		body          io.ReadCloser
		contentLength int64
		failWriter    bool
		wantStatus    int
		wantBody      string
	}{
		{name: "empty_read_error", body: codexObservationFailReader{}, wantStatus: 404},
		{name: "truncated_valid_json", body: io.NopCloser(io.MultiReader(strings.NewReader(notFoundBody), codexObservationFailReader{})), wantStatus: 404, wantBody: notFoundBody},
		{name: "content_length_mismatch", body: io.NopCloser(strings.NewReader(notFoundBody)), contentLength: int64(len(notFoundBody) + 1), wantStatus: 404, wantBody: notFoundBody},
		{name: "normalized_downstream_error", body: io.NopCloser(strings.NewReader(notFoundBody)), failWriter: true, wantStatus: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			var writer http.ResponseWriter = recorder
			if tc.failWriter {
				writer = codexObservationFailWriter{recorder}
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/backend-api/codex"+codexThreadHintTestPath, nil)
			c.Set("codex_new_session_thread_hint", true)
			beginCodexThreadHintTestObservation(t, c, codexThreadHintTestPath)
			writeCodexHistoryNotesResponse(c, &http.Response{
				StatusCode: 404, Header: make(http.Header), Body: tc.body, ContentLength: tc.contentLength,
			}, "notes", codexThreadHintTestPath)
			c.Writer.WriteHeaderNow()
			require.Equal(t, tc.wantStatus, recorder.Code)
			require.Equal(t, tc.wantBody, recorder.Body.String())
			page := service.PageCodexContextManagementObservations(1, 20)
			require.Len(t, page.Items, 1)
			require.Equal(t, "failed", page.Items[0].Status)
			require.Equal(t, "delivery_error", page.Items[0].ErrorKind)
			require.Equal(t, 1, page.Summary.Failures)
			require.Zero(t, page.Summary.Successes)
		})
	}
}

func TestOpsErrorLoggerMiddleware_CodexNewThreadHintMissingNotLogged(t *testing.T) {
	for _, prefix := range []string{"/v1", "/backend-api/codex"} {
		for _, newSession := range []bool{true, false} {
			t.Run(prefix+"/new="+strconv.FormatBool(newSession), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				gin.SetMode(gin.TestMode)
				ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				router := gin.New()
				router.Use(OpsErrorLoggerMiddleware(ops))
				requestPath := prefix + codexThreadHintTestPath
				router.POST(requestPath, func(c *gin.Context) {
					c.Set("codex_new_session_thread_hint", newSession)
					writeCodexHistoryNotesResponse(c, &http.Response{
						StatusCode: 404, Header: http.Header{"Content-Type": {"application/json"}},
						Body: io.NopCloser(strings.NewReader(`{"detail":"Not found"}`)),
					}, "notes", codexThreadHintTestPath)
				})

				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, requestPath, nil))
				if newSession {
					require.Equal(t, http.StatusOK, recorder.Code)
					require.JSONEq(t, `{"text":""}`, recorder.Body.String())
					require.Zero(t, OpsErrorLogQueueLength())
					return
				}
				require.Equal(t, http.StatusNotFound, recorder.Code)
				require.Equal(t, int64(1), OpsErrorLogQueueLength())
				job := <-opsErrorLogQueue
				require.Equal(t, http.StatusNotFound, job.entry.StatusCode)
			})
		}
	}
}
