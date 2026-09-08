package handler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexObservationFailWriter struct{ *httptest.ResponseRecorder }

func (w codexObservationFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("private write error")
}
func (w codexObservationFailWriter) WriteString(string) (int, error) {
	return 0, errors.New("private write error")
}

type codexObservationFailReader struct{}

func (codexObservationFailReader) Read([]byte) (int, error) {
	return 0, errors.New("private body error")
}
func (codexObservationFailReader) Close() error { return nil }

func TestCodexContextObservationTracksWhoamiDeliveryFailure(t *testing.T) {
	service.SetFingerprintObservationEnabled(false)
	service.SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { service.SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(codexObservationFailWriter{httptest.NewRecorder()})
	c.Set("api_key", &service.APIKey{ID: 7, UserID: 9, User: &service.User{ID: 9}})
	(&GatewayHandler{}).CodexPATWhoami(c)
	page := service.PageCodexContextManagementObservations(1, 20)
	require.Len(t, page.Items, 1)
	require.Equal(t, "failed", page.Items[0].Status)
	require.Equal(t, "delivery_error", page.Items[0].ErrorKind)
}

func TestCodexContextObservationTracksHistoryBodyDelivery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       io.ReadCloser
		failWriter bool
		want       string
	}{
		{"success", io.NopCloser(strings.NewReader(`{"items":[]}`)), false, "delivered"},
		{"upstream_body_error", codexObservationFailReader{}, false, "failed"},
		{"downstream_error", io.NopCloser(strings.NewReader(`{"items":[]}`)), true, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.SetFingerprintObservationEnabled(false)
			service.SetFingerprintObservationEnabled(true)
			t.Cleanup(func() { service.SetFingerprintObservationEnabled(false) })
			var writer http.ResponseWriter = httptest.NewRecorder()
			if tc.failWriter {
				writer = codexObservationFailWriter{httptest.NewRecorder()}
			}
			c, _ := gin.CreateTestContext(writer)
			service.BeginCodexContextManagementObservation(c, "history", "/alpha/history/v2/list_windows")
			writeCodexHistoryNotesResponse(c, &http.Response{StatusCode: 200, Header: make(http.Header), Body: tc.body}, "history", "/alpha/history/v2/list_windows")
			page := service.PageCodexContextManagementObservations(1, 20)
			require.Len(t, page.Items, 1)
			require.Equal(t, tc.want, page.Items[0].Status)
			if tc.want == "failed" {
				require.Equal(t, "delivery_error", page.Items[0].ErrorKind)
			}
		})
	}
}
