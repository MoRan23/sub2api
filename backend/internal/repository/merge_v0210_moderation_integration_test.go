//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMergeV0210HistoricalAuditEventsNeverBecomePenalties(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := createEntUser(t, ctx, client, uniqueTestValue(t, "v0210-moderation")+"@example.test")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM content_moderation_logs WHERE user_id = $1", user.ID)
		_ = client.User.DeleteOneID(user.ID).Exec(context.Background())
	})
	repo := NewContentModerationRepository(integrationDB)
	for _, entry := range []struct{ mode, action string }{
		{service.ContentModerationModePreBlock, service.ContentModerationActionBlock},
		{service.ContentModerationModeObserve, service.ContentModerationActionCyberPolicy},
		{service.ContentModerationModePreBlock, service.ContentModerationActionWhitelistAllow},
		{service.ContentModerationModeCyberLogOnly, service.ContentModerationActionCyberPolicy},
		{service.ContentModerationModeRiskControlLogOnly, service.ContentModerationActionAllow},
		{service.ContentModerationModePreBlock, service.ContentModerationActionHashBlock},
	} {
		err := repo.CreateLog(ctx, &service.ContentModerationLog{
			RequestID: uniqueTestValue(t, "audit"), UserID: &user.ID, UserEmail: user.Email,
			Mode: entry.mode, Action: entry.action, Flagged: true,
			Endpoint: "/v1/responses", Model: "synthetic", CreatedAt: time.Now(),
		})
		require.NoError(t, err)
	}
	// These queries intentionally have no current allowlist membership: historical
	// evidence must remain exempt even after both lists are removed.
	for _, excludeCyber := range []bool{false, true} {
		count, err := repo.CountFlaggedByUserSince(ctx, user.ID, time.Now().Add(-time.Hour), excludeCyber)
		require.NoError(t, err)
		want := 2
		if excludeCyber {
			want = 1
		}
		require.Equal(t, want, count)
	}
}
