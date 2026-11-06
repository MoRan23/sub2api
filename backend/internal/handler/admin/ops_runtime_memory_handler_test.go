package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRuntimeMemoryHandlersAndHeapDownload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := service.ProvideOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(svc.StopRuntimeSettingsRefresh)
	h := NewOpsHandler(svc)
	r := gin.New()
	r.GET("/memory", h.GetRuntimeMemory)
	r.POST("/enable", h.EnableHeapProfile)
	r.POST("/disable", h.DisableHeapProfile)
	r.GET("/download", h.DownloadHeapProfile)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	w := request(http.MethodGet, "/memory", "")
	require.Equal(t, http.StatusOK, w.Code)
	var envelope struct {
		Data runtimeobs.View `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Equal(t, runtimeobs.InstanceID(), envelope.Data.CurrentInstanceID)
	require.Equal(t, http.StatusBadRequest, request(http.MethodGet, "/memory?instance_id=bad", "").Code)
	current := `{"instance_id":"` + runtimeobs.InstanceID() + `"}`
	other := `{"instance_id":"` + uuid.NewString() + `"}`
	require.Equal(t, http.StatusForbidden, request(http.MethodGet, "/download?instance_id="+runtimeobs.InstanceID(), "").Code)
	require.Equal(t, http.StatusConflict, request(http.MethodPost, "/enable", other).Code)
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/enable", current).Code)
	require.Equal(t, http.StatusConflict, request(http.MethodGet, "/download?instance_id="+uuid.NewString(), "").Code)
	release, err := svc.RuntimeMemory().Profile().Acquire(runtimeobs.InstanceID())
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, request(http.MethodGet, "/download?instance_id="+runtimeobs.InstanceID(), "").Code)
	release()
	w = request(http.MethodGet, "/download?instance_id="+runtimeobs.InstanceID(), "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/octet-stream", w.Header().Get("Content-Type"))
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.Greater(t, w.Body.Len(), 2)
	require.Equal(t, []byte{0x1f, 0x8b}, w.Body.Bytes()[:2])
	require.Equal(t, http.StatusOK, request(http.MethodPost, "/disable", current).Code)
	require.Equal(t, http.StatusForbidden, request(http.MethodGet, "/download?instance_id="+runtimeobs.InstanceID(), "").Code)
	svc.SetMonitoringEnabled(false)
	require.Equal(t, http.StatusNotFound, request(http.MethodGet, "/memory", "").Code)
	require.Equal(t, http.StatusNotFound, request(http.MethodPost, "/enable", current).Code)
}
