package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type UsageExportHandler struct{ service *service.UsageExportService }

func NewUsageExportHandler(s *service.UsageExportService) *UsageExportHandler {
	return &UsageExportHandler{service: s}
}
func (h *UsageExportHandler) owner(c *gin.Context) (service.UsageExportOwner, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Authentication required")
		return service.UsageExportOwner{}, false
	}
	o, e := h.service.Owner(c.Request.Context(), subject.UserID, c.GetString("auth_method"), c.ClientIP())
	if e != nil {
		response.ErrorFrom(c, e)
		return o, false
	}
	return o, true
}

type createUsageExportRequest struct {
	UserID                int64  `json:"user_id"`
	APIKeyID              int64  `json:"api_key_id"`
	AccountID             int64  `json:"account_id"`
	GroupID               int64  `json:"group_id"`
	RequestID             string `json:"request_id"`
	Model                 string `json:"model"`
	RequestType           string `json:"request_type"`
	Stream                *bool  `json:"stream"`
	NativeCompactionV2    *bool  `json:"native_compaction_v2"`
	BillingType           *int8  `json:"billing_type"`
	BillingMode           string `json:"billing_mode"`
	UpstreamModelMismatch *bool  `json:"upstream_model_mismatch"`
	StartDate             string `json:"start_date"`
	EndDate               string `json:"end_date"`
	Timezone              string `json:"timezone"`
	SortBy                string `json:"sort_by"`
	SortOrder             string `json:"sort_order"`
	Language              string `json:"language"`
}

func (h *UsageExportHandler) Create(c *gin.Context) {
	o, ok := h.owner(c)
	if !ok {
		return
	}
	var in createUsageExportRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if e := c.ShouldBindJSON(&in); e != nil {
		response.BadRequest(c, "Invalid export request")
		return
	}
	spec := service.UsageExportSpec{SortBy: in.SortBy, SortOrder: in.SortOrder, Language: in.Language, Timezone: in.Timezone, Filters: usagestats.UsageLogFilters{UserID: in.UserID, APIKeyID: in.APIKeyID, AccountID: in.AccountID, GroupID: in.GroupID, RequestID: strings.TrimSpace(in.RequestID), Model: in.Model, ModelFilterSource: usagestats.ModelSourceRequested, Stream: in.Stream, NativeCompactionV2: in.NativeCompactionV2, BillingType: in.BillingType, BillingMode: in.BillingMode, UpstreamModelMismatch: in.UpstreamModelMismatch}}
	if in.RequestType != "" {
		v, e := service.ParseUsageRequestType(in.RequestType)
		if e != nil {
			response.BadRequest(c, e.Error())
			return
		}
		n := int16(v)
		spec.Filters.RequestType = &n
	}
	if in.StartDate != "" {
		t, e := timezone.ParseInUserLocation("2006-01-02", in.StartDate, in.Timezone)
		if e != nil {
			response.BadRequest(c, "Invalid start_date")
			return
		}
		spec.Filters.StartTime = &t
	}
	if in.EndDate != "" {
		t, e := timezone.ParseInUserLocation("2006-01-02", in.EndDate, in.Timezone)
		if e != nil {
			response.BadRequest(c, "Invalid end_date")
			return
		}
		t = t.AddDate(0, 0, 1)
		spec.Filters.EndTime = &t
	}
	if spec.Filters.StartTime != nil && spec.Filters.EndTime != nil && !spec.Filters.StartTime.Before(*spec.Filters.EndTime) {
		response.BadRequest(c, "Invalid date range")
		return
	}
	j, e := h.service.Create(c.Request.Context(), o, spec)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"code": 0, "message": "success", "data": j})
}
func (h *UsageExportHandler) List(c *gin.Context) {
	o, ok := h.owner(c)
	if !ok {
		return
	}
	v, e := h.service.List(c.Request.Context(), o)
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, v)
}
func (h *UsageExportHandler) Get(c *gin.Context) {
	o, ok := h.owner(c)
	if !ok {
		return
	}
	v, e := h.service.Get(c.Request.Context(), o, c.Param("id"))
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, v)
}
func (h *UsageExportHandler) Cancel(c *gin.Context) {
	o, ok := h.owner(c)
	if !ok {
		return
	}
	if e := h.service.Cancel(c.Request.Context(), o, c.Param("id")); e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}
func (h *UsageExportHandler) Delete(c *gin.Context) {
	o, ok := h.owner(c)
	if !ok {
		return
	}
	if e := h.service.Delete(c.Request.Context(), o, c.Param("id")); e != nil {
		response.ErrorFrom(c, e)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
func (h *UsageExportHandler) Ticket(c *gin.Context) {
	o, ok := h.owner(c)
	if !ok {
		return
	}
	v, e := h.service.Ticket(c.Request.Context(), o, c.Param("id"))
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"url": "/api/v1/usage-exports/download/" + v, "expires_in": 60})
}
func (h *UsageExportHandler) Download(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	f, j, e := h.service.Download(c.Request.Context(), c.Param("ticket"), c.ClientIP())
	if e != nil {
		response.ErrorFrom(c, e)
		return
	}
	defer f.Close()
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", `attachment; filename="usage_`+j.CreatedAt.Format("20060102_150405")+`.xlsx"`)
	http.ServeContent(c.Writer, c.Request, "usage.xlsx", time.Time{}, f)
}
