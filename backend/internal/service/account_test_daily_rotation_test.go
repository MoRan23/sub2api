//go:build unit

package service

import (
	"context"
	"testing"
)

func TestApplyOAuthAccountTestRootSessionUsesDailySyncRoot(t *testing.T) {
	settingsRepo := &openAIUUIDv7RuntimeRepo{values: map[string]string{
		SettingKeyEnableOpenAIOAuthDailySessionRotation: "true",
	}}
	daily := &fakeOAuthDailySessionRepository{pool: OAuthDailySessionPool{
		AccountID: 1374, BusinessDate: "2026-09-11", Generation: "018f5c3c-6e3a-7abe-8def-1234567890ad",
		StreamSessionIDs: [OAuthDailyStreamSessionCount]string{
			"018f5c3c-6e3a-7abc-8def-1234567890ab", "018f5c3c-6e3a-7abd-8def-1234567890ac", "018f5c3c-6e3a-7abe-8def-1234567890ad",
		},
		SyncSessionID: "018f5c3c-6e3a-7abf-8def-1234567890ae",
	}}
	svc := &AccountTestService{
		settingService: NewSettingService(settingsRepo, nil), oauthDailySessionRepo: daily,
	}
	plan := &OpenAIOAuthIdentityPlan{}
	err := svc.applyOAuthAccountTestRootSession(context.Background(), &Account{ID: 1374, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TurnIdentity.SessionID != daily.pool.SyncSessionID || plan.TurnIdentity.ThreadID != daily.pool.SyncSessionID {
		t.Fatalf("account test identity = %#v, want dedicated sync root", plan.TurnIdentity)
	}
	if plan.TurnIdentity.ParentThreadID != "" || plan.TurnIdentity.ForkedFromThreadID != "" {
		t.Fatalf("account test must remain a root probe: %#v", plan.TurnIdentity)
	}
}
