package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestListFingerprintObservationsReturnsSessionPaginationContract(t *testing.T) {
	service.SetFingerprintObservationEnabled(false)
	defer service.SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/fingerprint-observations?page=3&page_size=500", nil)

	(&OpenAIOAuthHandler{}).ListFingerprintObservations(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Enabled     bool                                        `json:"enabled"`
			Items       []service.FingerprintObservationSessionNode `json:"items"`
			Total       int                                         `json:"total"`
			Page        int                                         `json:"page"`
			PageSize    int                                         `json:"page_size"`
			Pages       int                                         `json:"pages"`
			SnapshotSeq uint64                                      `json:"snapshot_seq"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != 0 || envelope.Data.Enabled || envelope.Data.Total != 0 || envelope.Data.Page != 3 ||
		envelope.Data.PageSize != 100 || envelope.Data.Pages != 1 || envelope.Data.Items == nil {
		t.Fatalf("unexpected response: %+v", envelope)
	}

	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	data, _ := raw["data"].(map[string]any)
	if _, exists := data["entries"]; exists {
		t.Fatalf("legacy flat entries contract is still exposed: %s", recorder.Body.String())
	}
}

func TestListFingerprintObservationsUsesDefaultPaginationForInvalidQuery(t *testing.T) {
	service.SetFingerprintObservationEnabled(false)
	defer service.SetFingerprintObservationEnabled(false)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/fingerprint-observations?page=-1&page_size=bad&snapshot_seq=bad", nil)

	(&OpenAIOAuthHandler{}).ListFingerprintObservations(c)

	var envelope struct {
		Data fingerprintObservationsResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Page != 1 || envelope.Data.PageSize != fingerprintObservationDefaultPageSize || envelope.Data.Pages != 1 {
		t.Fatalf("invalid query did not fall back to defaults: %+v", envelope.Data)
	}
}
