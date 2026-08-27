package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupApplicationListRejectsInvalidFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name    string
		query   string
		message string
	}{
		{name: "empty page", query: "?page=", message: "Invalid page"},
		{name: "non numeric page", query: "?page=abc", message: "Invalid page"},
		{name: "zero page", query: "?page=0", message: "Invalid page"},
		{name: "negative page", query: "?page=-1", message: "Invalid page"},
		{name: "overflowing offset", query: "?page=" + strconv.Itoa(maxInt) + "&page_size=200", message: "Invalid page"},
		{name: "empty page size", query: "?page_size=", message: "Invalid page_size"},
		{name: "non numeric page size", query: "?page_size=abc", message: "Invalid page_size"},
		{name: "zero page size", query: "?page_size=0", message: "Invalid page_size"},
		{name: "negative page size", query: "?page_size=-1", message: "Invalid page_size"},
		{name: "oversized page size", query: "?page_size=201", message: "Invalid page_size"},
		{name: "empty user id", query: "?user_id=", message: "Invalid user_id"},
		{name: "non numeric user id", query: "?user_id=abc", message: "Invalid user_id"},
		{name: "zero user id", query: "?user_id=0", message: "Invalid user_id"},
		{name: "negative user id", query: "?user_id=-1", message: "Invalid user_id"},
		{name: "empty group id", query: "?group_id=", message: "Invalid group_id"},
		{name: "non numeric group id", query: "?group_id=abc", message: "Invalid group_id"},
		{name: "zero group id", query: "?group_id=0", message: "Invalid group_id"},
		{name: "negative group id", query: "?group_id=-1", message: "Invalid group_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/group-applications"+tt.query, nil)

			(&GroupApplicationHandler{}).List(ctx)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.JSONEq(t, `{"code":400,"message":"`+tt.message+`"}`, recorder.Body.String())
		})
	}
}

func TestParseGroupApplicationListFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("defaults", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/group-applications", nil)

		filter, err := parseGroupApplicationListFilter(ctx)

		require.NoError(t, err)
		require.Equal(t, groupApplicationListDefaultPageSize, filter.Limit)
		require.Zero(t, filter.Offset)
		require.Zero(t, filter.UserID)
		require.Zero(t, filter.GroupID)
	})

	t.Run("valid explicit filters", func(t *testing.T) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/group-applications?page=3&page_size=25&user_id=7&group_id=9&status=%20pending%20&search=%20alice%20", nil)

		filter, err := parseGroupApplicationListFilter(ctx)

		require.NoError(t, err)
		require.Equal(t, 25, filter.Limit)
		require.Equal(t, 50, filter.Offset)
		require.Equal(t, int64(7), filter.UserID)
		require.Equal(t, int64(9), filter.GroupID)
		require.Equal(t, "pending", filter.Status)
		require.Equal(t, "alice", filter.Search)
	})
}

type groupApplicationPanicReader struct{}

func (groupApplicationPanicReader) Read([]byte) (int, error) {
	panic("multipart body was read despite an oversized Content-Length")
}

type groupApplicationFillReader struct{}

func (groupApplicationFillReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestSaveGroupApplicationPolicyRejectsOversizedMultipartBeforeReading(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "group_id", Value: "4"}}
	ctx.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/group-application-policies/4", nil)
	ctx.Request.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")
	ctx.Request.Body = io.NopCloser(groupApplicationPanicReader{})
	ctx.Request.ContentLength = groupApplicationPolicyMultipartMax + 1

	require.NotPanics(t, func() {
		(&GroupApplicationHandler{}).SavePolicy(ctx)
	})
	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

func TestSaveGroupApplicationPolicyLimitsChunkedMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const boundary = "test-boundary"
	prefix := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"attachment\"; filename=\"large.pdf\"\r\n" +
		"Content-Type: application/pdf\r\n\r\n"
	body := io.MultiReader(
		strings.NewReader(prefix),
		io.LimitReader(groupApplicationFillReader{}, int64(groupApplicationPolicyMultipartMax)),
		strings.NewReader("\r\n--"+boundary+"--\r\n"),
	)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "group_id", Value: "4"}}
	ctx.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/group-application-policies/4", body)
	ctx.Request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	ctx.Request.ContentLength = -1

	(&GroupApplicationHandler{}).SavePolicy(ctx)

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

func TestGroupApplicationSMTPErrorMapping(t *testing.T) {
	raw := errors.New("smtp authentication failed: credential-canary")
	tests := []struct {
		name    string
		mapErr  func(error) error
		reason  string
		message string
	}{
		{
			name: "connection test", mapErr: groupApplicationSMTPTestError,
			reason:  "GROUP_APPLICATION_SMTP_TEST_FAILED",
			message: "SMTP test failed. Verify the host, port, TLS mode, account credentials, and network route.",
		},
		{
			name: "send test", mapErr: groupApplicationSMTPSendTestError,
			reason:  "GROUP_APPLICATION_SMTP_SEND_TEST_FAILED",
			message: "Test email could not be sent. Verify the SMTP settings, sender address, recipient, and account permissions.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped := tt.mapErr(raw)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(mapped))
			require.Equal(t, tt.reason, infraerrors.Reason(mapped))
			require.Equal(t, tt.message, infraerrors.Message(mapped))
			require.NotContains(t, infraerrors.Message(mapped), "credential-canary")
			require.ErrorIs(t, mapped, raw)
		})
	}
}

func TestGroupApplicationSMTPErrorMappingPreservesApplicationErrors(t *testing.T) {
	existing := infraerrors.Conflict("GROUP_APPLICATION_DISABLED", "workflow is disabled")
	wrapped := errors.Join(errors.New("resolve configuration"), existing)

	mapped := groupApplicationSMTPTestError(wrapped)

	require.Same(t, wrapped, mapped)
	require.Equal(t, http.StatusConflict, infraerrors.Code(mapped))
	require.Equal(t, "GROUP_APPLICATION_DISABLED", infraerrors.Reason(mapped))
	require.NoError(t, groupApplicationSMTPTestError(nil))
}

type groupApplicationHandlerSettingRepo struct {
	values map[string]string
}

func (r *groupApplicationHandlerSettingRepo) Get(ctx context.Context, key string) (*service.Setting, error) {
	value, err := r.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *groupApplicationHandlerSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *groupApplicationHandlerSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *groupApplicationHandlerSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *groupApplicationHandlerSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *groupApplicationHandlerSettingRepo) GetAll(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(r.values))
	for key, value := range r.values {
		values[key] = value
	}
	return values, nil
}

func (r *groupApplicationHandlerSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type groupApplicationHandlerEncryptor struct{}

func (groupApplicationHandlerEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (groupApplicationHandlerEncryptor) Decrypt(ciphertext string) (string, error) {
	return strings.TrimPrefix(ciphertext, "enc:"), nil
}

func newGroupApplicationSMTPHandlerTestSubject() *GroupApplicationHandler {
	settings := &groupApplicationHandlerSettingRepo{values: map[string]string{}}
	applicationService := service.NewGroupApplicationService(nil, settings, groupApplicationHandlerEncryptor{})
	worker := service.NewGroupApplicationWorker(nil, applicationService, nil)
	return NewGroupApplicationHandler(applicationService, worker)
}

func TestGroupApplicationSecureSMTPHandlersReturnStableErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config := `{
		"enabled":false,
		"smtp":{
			"host":"smtp.example.com",
			"port":465,
			"username":"sender@example.com",
			"password":"smtp-secret",
			"from_address":"sender@example.com",
			"from_name":"Applications",
			"tls_mode":"implicit"
		},
		"imap":{}
	}`
	tests := []struct {
		name       string
		path       string
		body       string
		handle     func(*GroupApplicationHandler, *gin.Context)
		wantReason string
	}{
		{
			name: "connection test", path: "/api/v1/admin/group-applications/email-config/test-smtp",
			body: config, handle: func(handler *GroupApplicationHandler, ctx *gin.Context) { handler.TestSMTPSecure(ctx) },
			wantReason: "GROUP_APPLICATION_SMTP_TEST_FAILED",
		},
		{
			name: "send test", path: "/api/v1/admin/group-applications/email-config/send-test",
			body:       `{"config":` + config + `,"recipient":"recipient@example.com"}`,
			handle:     func(handler *GroupApplicationHandler, ctx *gin.Context) { handler.SendTestEmailSecure(ctx) },
			wantReason: "GROUP_APPLICATION_SMTP_SEND_TEST_FAILED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")

			tt.handle(newGroupApplicationSMTPHandlerTestSubject(), ctx)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			var envelope struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.Equal(t, tt.wantReason, envelope.Reason)
			require.NotContains(t, envelope.Message, "unavailable")
		})
	}
}
