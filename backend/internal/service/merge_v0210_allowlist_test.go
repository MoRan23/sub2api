package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeV0210GlobalAllowlistOverridesLegacyAuditWhitelist(t *testing.T) {
	for _, removeBeforeAudit := range []bool{false, true} {
		name := "both_lists"
		if removeBeforeAudit {
			name = "removed_while_audit_queued"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			smtpServer := startNotificationEmailTestSMTPServer(t)
			cfg := defaultContentModerationConfig()
			cfg.Enabled, cfg.AutoBanEnabled, cfg.EmailOnHit = true, true, true
			cfg.BanThreshold = 1
			cfg.Mode = ContentModerationModePreBlock
			cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
			cfg.BlockedKeywords = []string{"blocked prompt"}
			cfg.ContentModerationWhitelistUserIDs = []int64{12}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			settings := smtpServer.settings()
			settings[SettingKeyRiskControlEnabled] = "true"
			settings[SettingKeyContentModerationConfig] = string(raw)
			settings[SettingKeyCyberPolicyUserAllowlist] = "12"
			settingRepo := &contentModerationTestSettingRepo{values: settings}
			repo := &banCountArgsTestRepo{}
			hashes := &contentModerationTestHashCache{}
			userRepo := &contentModerationTestUserRepo{user: &User{ID: 12, Status: StatusActive}}
			svc := &ContentModerationService{
				settingRepo: settingRepo, repo: repo, hashCache: hashes, userRepo: userRepo,
				emailService: NewEmailService(settingRepo, nil), asyncQueue: make(chan contentModerationTask, 4),
			}
			decision, err := svc.Check(ctx, ContentModerationCheckInput{
				UserID: 12, UserEmail: "trusted@example.com", Protocol: ContentModerationProtocolOpenAIChat,
				Body: []byte(`{"messages":[{"role":"user","content":"blocked prompt"}]}`),
			})
			require.NoError(t, err)
			require.True(t, decision.Allowed)
			require.False(t, decision.Blocked)
			require.Equal(t, ContentModerationActionAllow, decision.Action)
			var task contentModerationTask
			select {
			case task = <-svc.asyncQueue:
			default:
				t.Fatal("expected the legacy whitelist audit task")
			}
			require.True(t, task.auditOnly)
			if removeBeforeAudit {
				settingRepo.values[SettingKeyCyberPolicyUserAllowlist] = ""
				cfg.ContentModerationWhitelistUserIDs = nil
				raw, err = json.Marshal(cfg)
				require.NoError(t, err)
				settingRepo.values[SettingKeyContentModerationConfig] = string(raw)
				_, err = svc.refreshRuntimeSnapshot(ctx)
				require.NoError(t, err)
				require.Empty(t, svc.runtimeSnapshot.Load().allowlistedUsers)
			}
			delay := 0
			svc.runAuditOnlyWithKeywordMatch(ctx, task.input, task.config, task.content, task.matchedKeyword, task.keywordExcerpt, task.inputHash, &delay)
			logs := repo.snapshotLogs()
			require.Len(t, logs, 1)
			require.True(t, logs[0].Flagged)
			require.Equal(t, ContentModerationModeRiskControlLogOnly, logs[0].Mode)
			require.Zero(t, logs[0].ViolationCount)
			require.False(t, logs[0].AutoBanned)
			require.False(t, logs[0].EmailSent)
			require.Empty(t, hashes.snapshotRecorded())
			require.Empty(t, repo.snapshotCountCalls())
			require.Empty(t, userRepo.updated)
			require.Zero(t, smtpServer.messageCount())
		})
	}
}

func TestMergeV0210CyberLogOnlyOverridesLegacyNotificationRecipients(t *testing.T) {
	smtpServer := startNotificationEmailTestSMTPServer(t)
	cfg := defaultContentModerationConfig()
	cfg.AutoBanEnabled, cfg.EmailOnHit = true, true
	cfg.BanThreshold = 1
	cfg.CyberPolicyWhitelistUserIDs = []int64{12}
	cfg.CyberPolicyNotificationEmails = []string{"security@example.com", "ops@example.com"}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings := smtpServer.settings()
	settings[SettingKeyRiskControlEnabled] = "true"
	settings[SettingKeyContentModerationConfig] = string(raw)
	settingRepo := &contentModerationTestSettingRepo{values: settings}
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(settingRepo, repo, nil, nil, nil, nil, nil, NewEmailService(settingRepo, nil))
	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		LogOnly: true, UserID: 12, UserEmail: "trusted@example.com", Model: "gpt-5",
		UpstreamMessage: "blocked", UpstreamBody: `{"error":{"code":"cyber_policy"}}`,
	})
	logs := repo.snapshotLogs()
	require.Len(t, logs, 1)
	require.Equal(t, ContentModerationModeCyberLogOnly, logs[0].Mode)
	require.True(t, logs[0].Flagged)
	require.Contains(t, logs[0].Error, "cyber_policy")
	require.False(t, logs[0].AutoBanned)
	require.False(t, logs[0].EmailSent)
	require.Empty(t, repo.snapshotCountCalls())
	require.Zero(t, smtpServer.messageCount())
}
