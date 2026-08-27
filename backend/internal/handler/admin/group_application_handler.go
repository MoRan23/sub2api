package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	groupApplicationListDefaultPageSize = 50
	groupApplicationListMaxPageSize     = 200
	groupApplicationPolicyMultipartMax  = service.GroupApplicationMaxAttachmentBytes + 1<<20
)

type GroupApplicationHandler struct {
	service *service.GroupApplicationService
	worker  *service.GroupApplicationWorker
}

func NewGroupApplicationHandler(service *service.GroupApplicationService, worker *service.GroupApplicationWorker) *GroupApplicationHandler {
	return &GroupApplicationHandler{service: service, worker: worker}
}

func groupApplicationAdminID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Admin not authenticated")
		return 0, false
	}
	return subject.UserID, true
}

func groupApplicationID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid application ID")
		return 0, false
	}
	return id, true
}

func (h *GroupApplicationHandler) List(c *gin.Context) {
	filter, err := parseGroupApplicationListFilter(c)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.service.ListApplications(c.Request.Context(), filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func parseGroupApplicationListFilter(c *gin.Context) (service.GroupApplicationListFilter, error) {
	filter := service.GroupApplicationListFilter{
		Status: strings.TrimSpace(c.Query("status")),
		Search: strings.TrimSpace(c.Query("search")),
		Limit:  groupApplicationListDefaultPageSize,
	}
	page := 1
	if raw, exists := c.GetQuery("page"); exists {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return service.GroupApplicationListFilter{}, errors.New("Invalid page")
		}
		page = value
	}
	if raw, exists := c.GetQuery("page_size"); exists {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > groupApplicationListMaxPageSize {
			return service.GroupApplicationListFilter{}, errors.New("Invalid page_size")
		}
		filter.Limit = value
	}
	var err error
	if filter.UserID, err = parseOptionalPositiveGroupApplicationID(c, "user_id"); err != nil {
		return service.GroupApplicationListFilter{}, err
	}
	if filter.GroupID, err = parseOptionalPositiveGroupApplicationID(c, "group_id"); err != nil {
		return service.GroupApplicationListFilter{}, err
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/filter.Limit {
		return service.GroupApplicationListFilter{}, errors.New("Invalid page")
	}
	filter.Offset = (page - 1) * filter.Limit
	return filter, nil
}

func parseOptionalPositiveGroupApplicationID(c *gin.Context, name string) (int64, error) {
	raw, exists := c.GetQuery(name)
	if !exists {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("Invalid %s", name)
	}
	return value, nil
}

func (h *GroupApplicationHandler) Get(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	item, err := h.service.GetApplication(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) ListCommunications(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	items, err := h.service.ListCommunications(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *GroupApplicationHandler) ExportCommunications(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	application, err := h.service.GetApplication(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items, err := h.service.ListCommunications(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	payload := struct {
		SchemaVersion  int                                     `json:"schema_version"`
		ExportedAt     time.Time                               `json:"exported_at"`
		ApplicationID  int64                                   `json:"application_id"`
		UserID         int64                                   `json:"user_id"`
		UserEmail      string                                  `json:"user_email"`
		GroupID        int64                                   `json:"group_id"`
		GroupName      string                                  `json:"group_name"`
		ContactEmail   string                                  `json:"contact_email"`
		Status         string                                  `json:"status"`
		Communications []service.GroupApplicationCommunication `json:"communications"`
	}{
		SchemaVersion: 1, ExportedAt: time.Now().UTC(), ApplicationID: application.ID,
		UserID: application.UserID, UserEmail: application.UserEmail, GroupID: application.GroupID,
		GroupName: application.GroupName, ContactEmail: application.ContactEmail, Status: application.Status,
		Communications: items,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	data = append(data, '\n')
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=group-application-%d-communications.json", id))
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func (h *GroupApplicationHandler) Approve(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	adminID, ok := groupApplicationAdminID(c)
	if !ok {
		return
	}
	item, err := h.service.Approve(c.Request.Context(), id, adminID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) Reject(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	adminID, ok := groupApplicationAdminID(c)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	item, err := h.service.Reject(c.Request.Context(), id, adminID, input.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) Revoke(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	adminID, ok := groupApplicationAdminID(c)
	if !ok {
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	item, err := h.service.Revoke(c.Request.Context(), id, adminID, input.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) ResendApproval(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	if err := h.service.ResendApproval(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"queued": true})
}

func (h *GroupApplicationHandler) RetryMail(c *gin.Context) {
	id, ok := groupApplicationID(c)
	if !ok {
		return
	}
	outboxID, err := strconv.ParseInt(c.Param("outbox_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid outbox ID")
		return
	}
	if err = h.service.RetryMail(c.Request.Context(), id, outboxID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"queued": true})
}

func (h *GroupApplicationHandler) ListPolicies(c *gin.Context) {
	items, err := h.service.ListPolicies(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

func (h *GroupApplicationHandler) SavePolicy(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	adminID, ok := groupApplicationAdminID(c)
	if !ok {
		return
	}
	var policy service.GroupApplicationPolicy
	policy.GroupID = groupID
	var attachment *service.GroupApplicationAttachment
	if strings.HasPrefix(strings.ToLower(c.ContentType()), "multipart/form-data") {
		if c.Request.ContentLength > groupApplicationPolicyMultipartMax {
			response.Error(c, http.StatusRequestEntityTooLarge, "Multipart request exceeds 11 MiB")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, groupApplicationPolicyMultipartMax)
		if err = c.Request.ParseMultipartForm(1 << 20); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				response.Error(c, http.StatusRequestEntityTooLarge, "Multipart request exceeds 11 MiB")
			} else {
				response.BadRequest(c, "Invalid multipart request")
			}
			return
		}
		defer c.Request.MultipartForm.RemoveAll()
		if err = json.Unmarshal([]byte(c.PostForm("policy")), &policy); err != nil {
			response.BadRequest(c, "Invalid policy JSON")
			return
		}
		policy.GroupID = groupID
		fileHeader, fileErr := c.FormFile("attachment")
		if fileErr != nil && !errors.Is(fileErr, http.ErrMissingFile) {
			response.BadRequest(c, "Cannot read attachment")
			return
		}
		if fileErr == nil {
			if fileHeader.Size > service.GroupApplicationMaxAttachmentBytes {
				response.BadRequest(c, "PDF attachment exceeds 10 MiB")
				return
			}
			file, openErr := fileHeader.Open()
			if openErr != nil {
				response.BadRequest(c, "Cannot read attachment")
				return
			}
			defer file.Close()
			data, readErr := io.ReadAll(io.LimitReader(file, service.GroupApplicationMaxAttachmentBytes+1))
			if readErr != nil {
				response.BadRequest(c, "Cannot read attachment")
				return
			}
			attachment = &service.GroupApplicationAttachment{Filename: fileHeader.Filename, Data: data}
		}
	} else if err = c.ShouldBindJSON(&policy); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	} else {
		policy.GroupID = groupID
	}
	item, err := h.service.SavePolicy(c.Request.Context(), &policy, attachment, adminID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) DownloadAttachment(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("attachment_id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid attachment ID")
		return
	}
	item, err := h.service.GetAttachment(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", item.Filename))
	c.Data(http.StatusOK, item.ContentType, item.Data)
}

func (h *GroupApplicationHandler) GetEmailConfig(c *gin.Context) {
	item, err := h.service.GetEmailConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *GroupApplicationHandler) SaveEmailConfig(c *gin.Context) {
	var input service.GroupApplicationEmailConfig
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	item, err := h.service.SaveEmailConfig(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.worker.RefreshConfiguration(c.Request.Context())
	response.Success(c, item)
}

func (h *GroupApplicationHandler) TestSMTP(c *gin.Context) {
	var input service.GroupApplicationEmailConfig
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if err := h.worker.TestSMTP(c.Request.Context(), input); err != nil {
		response.ErrorFrom(c, groupApplicationSMTPTestError(err))
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *GroupApplicationHandler) SendTestEmail(c *gin.Context) {
	var input struct {
		Config    service.GroupApplicationEmailConfig `json:"config"`
		Recipient string                              `json:"recipient"`
	}
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if err := h.worker.SendTestEmail(c.Request.Context(), input.Config, input.Recipient); err != nil {
		response.ErrorFrom(c, groupApplicationSMTPSendTestError(err))
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func groupApplicationSMTPTestError(err error) error {
	return stableGroupApplicationSMTPError(
		err,
		"GROUP_APPLICATION_SMTP_TEST_FAILED",
		"SMTP test failed. Verify the host, port, TLS mode, account credentials, and network route.",
	)
}

func groupApplicationSMTPSendTestError(err error) error {
	return stableGroupApplicationSMTPError(
		err,
		"GROUP_APPLICATION_SMTP_SEND_TEST_FAILED",
		"Test email could not be sent. Verify the SMTP settings, sender address, recipient, and account permissions.",
	)
}

func stableGroupApplicationSMTPError(err error, reason, message string) error {
	if err == nil {
		return nil
	}
	var applicationErr *infraerrors.ApplicationError
	if errors.As(err, &applicationErr) {
		return err
	}
	return infraerrors.BadRequest(reason, message).WithCause(err)
}

func (h *GroupApplicationHandler) TestIMAP(c *gin.Context) {
	var input service.GroupApplicationEmailConfig
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	mailboxes, err := h.worker.TestIMAP(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true, "mailboxes": mailboxes})
}

func (h *GroupApplicationHandler) WorkerStatus(c *gin.Context) {
	response.Success(c, h.worker.Health())
}
