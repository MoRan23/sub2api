package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type daybreakRuntimeGroupRepo struct {
	GroupRepository
	fresh *Group
	err   error
	ids   []int64
}

func (r *daybreakRuntimeGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	r.ids = append(r.ids, id)
	return r.fresh, r.err
}

func daybreakRuntimeRequest(ctx context.Context, transport OpenAIClientTransport) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	SetOpenAIClientTransport(c, transport)
	return c
}

func TestOpenAIDaybreakWSRefreshesTrustedGroupWithoutMutatingHandshake(t *testing.T) {
	handshakeGroup := daybreakTestGroup(true, true)
	handshakeCopy := *handshakeGroup
	ctx := daybreakTestContext(handshakeGroup)
	c := daybreakRuntimeRequest(ctx, OpenAIClientTransportWS)
	account := daybreakPolicyTestAccount(true, true)
	account.GroupIDs = []int64{987, handshakeGroup.ID}
	svc := daybreakPolicyTestService(true)
	repo := &daybreakRuntimeGroupRepo{fresh: daybreakTestGroup(false, false)}
	svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: repo}
	svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(daybreakTransportManifest), time.Now())
	body := []byte(`{"type":"response.create","model":"gpt-6-astra","input":"history"}`)
	original := string(body)

	wire, reason, err := svc.applyOpenAIDaybreakWithContext(ctx, c, account, body)
	require.NoError(t, err)
	require.Equal(t, body, wire)
	require.Equal(t, "group_blue_disabled", reason)

	repo.fresh = daybreakTestGroup(true, true)
	wire, reason, err = svc.applyOpenAIDaybreakWithContext(ctx, c, account, body)
	require.NoError(t, err)
	require.Equal(t, "automatic", reason)
	require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String())

	repo.fresh = daybreakTestGroup(true, false)
	wire, reason, err = svc.applyOpenAIDaybreakWithContext(ctx, c, account, body)
	require.NoError(t, err)
	require.Equal(t, body, wire)
	require.Equal(t, "group_red_disabled", reason)
	require.Equal(t, []int64{handshakeGroup.ID, handshakeGroup.ID, handshakeGroup.ID}, repo.ids)
	require.Equal(t, handshakeCopy, *handshakeGroup)
	require.Same(t, handshakeGroup, ctx.Value(ctxkey.Group))
	require.Same(t, handshakeGroup, c.Request.Context().Value(ctxkey.Group))
	require.Equal(t, original, string(body), "later frames must not inherit injected retry state")
}

func TestOpenAIDaybreakHTTPUsesAuthorizedCompositeSnapshotWithoutRefresh(t *testing.T) {
	group := daybreakTestGroup(true, true)
	group.Platform = PlatformComposite
	ctx := daybreakTestContext(group)
	c := daybreakRuntimeRequest(ctx, OpenAIClientTransportHTTP)
	account := daybreakPolicyTestAccount(true, true)
	account.GroupIDs = []int64{group.ID + 1}
	svc := daybreakPolicyTestService(true)
	repo := &daybreakRuntimeGroupRepo{err: errors.New("must not refresh HTTP snapshot")}
	svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: repo}
	svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(daybreakTransportManifest), time.Now())
	wire, reason, err := svc.applyOpenAIDaybreakWithContext(context.Background(), c, account, []byte(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	require.Equal(t, "automatic", reason)
	require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String())
	require.Empty(t, repo.ids, "HTTP must use the authorization snapshot without another repository read")

	// A routed child may be enabled, but the authorized Composite parent remains
	// the policy authority. It is never substituted with an account's bindings.
	parent := *group
	parent.OpenAIDaybreakBlueEnabled = false
	parent.OpenAIDaybreakRedEnabled = false
	wire, reason, err = svc.applyOpenAIDaybreakWithContext(daybreakTestContext(&parent), c, account, []byte(`{"model":"gpt-6-astra"}`))
	require.NoError(t, err)
	require.Equal(t, "group_blue_disabled", reason)
	require.False(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
	require.Empty(t, repo.ids)
}

func TestOpenAIDaybreakWSGroupFailureSkipsCatalogAndAccountGroupFallback(t *testing.T) {
	_, catalogCalls := newCodexModelsOAuthCacheServer(t, `{"models":[]}`)
	validGroup := daybreakTestGroup(true, true)
	for _, tc := range []struct {
		name  string
		group *Group
		err   error
	}{
		{name: "lookup failure", err: errors.New("database unavailable")},
		{name: "deleted group"},
		{name: "different ID", group: &Group{ID: validGroup.ID + 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, OpenAIDaybreakBlueEnabled: true}},
		{name: "partial group", group: &Group{ID: validGroup.ID, Platform: PlatformOpenAI, Status: StatusActive}},
		{name: "disabled group", group: &Group{ID: validGroup.ID, Platform: PlatformOpenAI, Status: "disabled", Hydrated: true}},
		{name: "other platform", group: &Group{ID: validGroup.ID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := daybreakPolicyTestService(true)
			repo := &daybreakRuntimeGroupRepo{fresh: tc.group, err: tc.err}
			svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: repo}
			account := newCodexModelsTestAccount()
			account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true}
			account.GroupIDs = []int64{validGroup.ID, validGroup.ID + 1}
			account = scopedAuxiliaryOSFixture(t, svc, account)
			ctx := daybreakTestContext(validGroup)
			c := daybreakRuntimeRequest(ctx, OpenAIClientTransportWS)
			body := []byte(`{"type":"response.create","model":"gpt-6-sol"}`)
			wire, reason, err := svc.applyOpenAIDaybreakWithContext(ctx, c, account, body)
			require.NoError(t, err)
			require.Equal(t, body, wire)
			require.Equal(t, "group_unavailable", reason)
			require.Equal(t, []int64{validGroup.ID}, repo.ids)
			require.Zero(t, catalogCalls.Load(), "failed group authorization must not trigger auxiliary catalog requests")
		})
	}

	svc := daybreakPolicyTestService(true)
	repo := &daybreakRuntimeGroupRepo{fresh: validGroup}
	svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: repo}
	account := daybreakPolicyTestAccount(true, true)
	account.GroupIDs = []int64{validGroup.ID}
	ctx := context.Background()
	c := daybreakRuntimeRequest(ctx, OpenAIClientTransportWS)
	body := []byte(`{"type":"response.create","model":"gpt-6-astra"}`)
	wire, reason, err := svc.applyOpenAIDaybreakWithContext(ctx, c, account, body)
	require.NoError(t, err)
	require.Equal(t, body, wire)
	require.Equal(t, "group_unavailable", reason)
	require.Empty(t, repo.ids, "account bindings are not authority to select a request group")
	require.Zero(t, catalogCalls.Load())
}

func TestOpenAIDaybreakPhysicalRequestSnapshotDoesNotFreezeSharedContext(t *testing.T) {
	svc := daybreakPolicyTestService(true)
	shared := daybreakEnabledTestContext()
	first := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(shared)
	freezeOpenAIDaybreakPolicyOnRequest(first, svc.settingService)
	require.True(t, openAIDaybreakPolicyEnabled(first.Context(), svc.settingService))
	require.Nil(t, shared.Value(openAIDaybreakPolicyContextKey{}))

	svc.settingService.publishOpenAIDaybreakEnabled("false")
	freezeOpenAIDaybreakPolicyOnRequest(first, svc.settingService)
	require.True(t, openAIDaybreakPolicyEnabled(first.Context(), svc.settingService), "the same physical attempt uses one coherent setting")
	second := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(shared)
	freezeOpenAIDaybreakPolicyOnRequest(second, svc.settingService)
	require.False(t, openAIDaybreakPolicyEnabled(second.Context(), svc.settingService), "a new attempt reads the current preference")
	require.Nil(t, shared.Value(openAIDaybreakPolicyContextKey{}))

	svc.settingService.publishOpenAIDaybreakEnabled("true")
	freezeOpenAIDaybreakPolicyOnRequest(second, svc.settingService)
	require.False(t, openAIDaybreakPolicyEnabled(second.Context(), svc.settingService))
	require.True(t, openAIDaybreakPolicyEnabled(shared, svc.settingService), "WS frames using shared context see the live setting")
}
