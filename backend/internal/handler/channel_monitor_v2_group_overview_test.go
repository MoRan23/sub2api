package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type overviewHandlerAuthorizer struct {
	channelMonitorV2GroupAuthorizerStub
	rates     map[int64]float64
	rateErr   error
	rateCalls []int64
}

func (a *overviewHandlerAuthorizer) GetUserGroupRates(_ context.Context, userID int64) (map[int64]float64, error) {
	a.rateCalls = append(a.rateCalls, userID)
	return a.rates, a.rateErr
}

type overviewHandlerRepo struct {
	service.ChannelMonitorV2Repository
	filter service.ChannelMonitorV2Filter
	reads  int
}

func (r *overviewHandlerRepo) GetConfig(context.Context) (*service.ChannelMonitorV2Config, error) {
	return &service.ChannelMonitorV2Config{Enabled: true, RefreshIntervalSeconds: 60, Platforms: []service.ChannelMonitorV2PlatformConfig{{Platform: "openai", Enabled: true}}}, nil
}
func (r *overviewHandlerRepo) GetMatrix(_ context.Context, f service.ChannelMonitorV2Filter, _ service.ChannelMonitorV2Config, _ service.ChannelMonitorV2GroupBy, _ bool) (*service.ChannelMonitorV2Matrix, error) {
	r.filter = f
	r.reads++
	return &service.ChannelMonitorV2Matrix{}, nil
}
func (r *overviewHandlerRepo) GetGroupAttributionHistory(context.Context, []int64) (map[int64][]service.ChannelMonitorV2AttributionEntry, error) {
	return nil, nil
}

func TestChannelMonitorV2GroupOverviewAlwaysAuthorizesCurrentUser(t *testing.T) {
	for _, role := range []string{service.RoleUser, service.RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/channel-monitor-v2/group-overview?range=90m&group_id=99", nil)
			c.Request.Header.Set("Accept-Language", "en-US")
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
			c.Set(string(middleware.ContextKeyUserRole), role)
			a := &overviewHandlerAuthorizer{channelMonitorV2GroupAuthorizerStub: channelMonitorV2GroupAuthorizerStub{groups: []service.Group{{ID: 7, Name: "permitted", I18n: service.GroupI18n{"en": {Name: "Allowed English"}}, Platform: "openai", Status: service.StatusActive, RateMultiplier: 1}}}, rates: map[int64]float64{7: .25}}
			r := &overviewHandlerRepo{}
			h := &ChannelMonitorV2Handler{service: service.NewChannelMonitorV2Service(r), apiKeyService: a}
			h.GroupOverview(c)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, []int64{42}, a.calls)
			require.Equal(t, []int64{42}, a.rateCalls)
			require.Equal(t, []int64{7}, r.filter.AllowedGroupIDs)
			require.True(t, r.filter.RestrictGroups)
			require.NotContains(t, recorder.Body.String(), `"group_id":99`)
			require.Contains(t, recorder.Body.String(), `"rate_multiplier":0.25`)
			require.Contains(t, recorder.Body.String(), `"group_name":"Allowed English"`)
			require.NotContains(t, recorder.Body.String(), `group_i18n`)
			require.Equal(t, "Accept-Language", recorder.Header().Get("Vary"))
		})
	}
}

func TestChannelMonitorV2GroupOverviewRejectsUnknownUser(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/channel-monitor-v2/group-overview", nil)
	(&ChannelMonitorV2Handler{}).GroupOverview(c)
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestChannelMonitorV2GroupOverviewRateErrorFailsClosed(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/channel-monitor-v2/group-overview", nil)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	a := &overviewHandlerAuthorizer{rateErr: errors.New("rate lookup failed")}
	r := &overviewHandlerRepo{}
	h := &ChannelMonitorV2Handler{service: service.NewChannelMonitorV2Service(r), apiKeyService: a}
	h.GroupOverview(c)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Zero(t, r.reads)
}
