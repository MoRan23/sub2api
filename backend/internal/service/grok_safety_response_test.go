package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokSafetyResponsePreservesNativeError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
		body := []byte(`{ "error": {"code":"safety_review_rejected","message":"rejected","param":"input","details":{"kind":"policy"}}, "trace":"upstream" }`)
		WriteGrokSafetyRejectionResponse(c, status, body)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Equal(t, body, recorder.Body.Bytes())
		WriteGrokSafetyRejectionResponse(c, status, nil)
		require.Equal(t, body, recorder.Body.Bytes(), "must not write a second response")
	}
}
func TestGrokSafetyResponseConvertsNestedErrorWithoutLosingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/responses", "/v1/messages"} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, path, nil)
		body := []byte(`{"type":"response.failed","response":{"error":{"code":"safety_review_rejected","type":"permission_error","message":"original rejection","param":"input","details":{"reason":"policy"}}}}`)
		WriteGrokSafetyRejectionResponse(c, http.StatusOK, body)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		for _, key := range []string{"code", "type", "message", "param", "details.reason"} {
			require.Equal(t, gjson.GetBytes(body, "response.error."+key).String(), gjson.GetBytes(recorder.Body.Bytes(), "error."+key).String())
		}
		if path == "/v1/messages" {
			require.Equal(t, "error", gjson.GetBytes(recorder.Body.Bytes(), "type").String())
		}
		require.False(t, gjson.GetBytes(recorder.Body.Bytes(), "fee").Exists())
	}
}
