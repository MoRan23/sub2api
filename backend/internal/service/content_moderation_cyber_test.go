package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

// cyberOrderingTestRepo records the sequence of repo calls to verify F7 ordering.
type cyberOrderingTestRepo struct {
	mu         sync.Mutex
	calls      []string
	emailSents []bool // EmailSent value captured at each CreateLog call
}

func (r *cyberOrderingTestRepo) CreateLog(ctx context.Context, log *ContentModerationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "create")
	if log != nil {
		r.emailSents = append(r.emailSents, log.EmailSent)
		log.ID = 1 // simulate DB-assigned ID so UpdateLogEmailSent guard passes
	}
	return nil
}

func (r *cyberOrderingTestRepo) UpdateLogEmailSent(ctx context.Context, id int64, sent bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "update_email_sent")
	return nil
}

func (r *cyberOrderingTestRepo) ListLogs(ctx context.Context, filter ContentModerationLogFilter) ([]ContentModerationLog, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *cyberOrderingTestRepo) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time, excludeCyberPolicy bool) (int, error) {
	return 0, nil
}

func (r *cyberOrderingTestRepo) CleanupExpiredLogs(ctx context.Context, hitBefore time.Time, nonHitBefore time.Time) (*ContentModerationCleanupResult, error) {
	return &ContentModerationCleanupResult{}, nil
}

func (r *cyberOrderingTestRepo) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *cyberOrderingTestRepo) snapshotEmailSents() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]bool, len(r.emailSents))
	copy(out, r.emailSents)
	return out
}

func TestRecordCyberPolicyEvent_DisabledWhenRiskControlOff(t *testing.T) {
	repo := &contentModerationTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled: "false",
		}},
		repo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID:          1,
		UserEmail:       "u@x.com",
		Model:           "gpt-5",
		Endpoint:        "/v1/responses",
		UpstreamMessage: "flagged",
		UpstreamBody:    `{"error":{"code":"cyber_policy"}}`,
		UpstreamStatus:  400,
	})

	require.Empty(t, repo.snapshotLogs(), "CreateLog must NOT be called when risk_control_enabled is off")
}

func TestRecordCyberPolicyEvent_WritesLogWhenEnabled(t *testing.T) {
	repo := &contentModerationTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled: "true",
		}},
		repo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // emailService=nil: email path safely skipped
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID:          1,
		UserEmail:       "u@x.com",
		Model:           "gpt-5",
		Endpoint:        "/v1/responses",
		UpstreamMessage: "flagged",
		UpstreamBody:    `{"error":{"code":"cyber_policy"}}`,
		UpstreamStatus:  400,
	})

	logs := repo.snapshotLogs()
	require.Len(t, logs, 1)
	log := logs[0]

	require.Equal(t, "cyber_policy", log.Action)
	require.True(t, log.Flagged)
	require.Equal(t, "cyber_policy", log.HighestCategory)
	require.Contains(t, log.Error, "flagged")
	require.False(t, log.AutoBanned)
	// emailService is nil, so EmailSent must be false
	require.False(t, log.EmailSent)

	// UserID pointer must be set
	require.NotNil(t, log.UserID)
	require.Equal(t, int64(1), *log.UserID)

	// score for cyber_policy is always 1.0
	require.Equal(t, 1.0, log.HighestScore)

	// mode must be post_upstream
	require.Equal(t, "post_upstream", log.Mode)

	// provider
	require.Equal(t, "openai", log.Provider)

	// model
	require.Equal(t, "gpt-5", log.Model)

	// endpoint
	require.Equal(t, "/v1/responses", log.Endpoint)

	// violation count >= 1 (side-effects ran)
	require.GreaterOrEqual(t, log.ViolationCount, 1)

	// Error field should also contain the upstream body JSON
	require.True(t, strings.Contains(log.Error, "cyber_policy") || strings.Contains(log.Error, "flagged"),
		"Error should mention flagged or cyber_policy")
}

func TestRecordCyberPolicyEvent_RespectsContentModerationScope(t *testing.T) {
	groupID := int64(7)
	tests := []struct {
		name       string
		config     string
		groupID    *int64
		model      string
		wantCalls  []bool
		wantLogs   int
		wantBanned bool
	}{
		{
			name:     "excluded group",
			config:   `{"all_groups":false,"group_ids":[8],"ban_threshold":1}`,
			groupID:  &groupID,
			model:    "gpt-5",
			wantLogs: 0,
		},
		{
			name:     "ungrouped excluded by selected groups",
			config:   `{"all_groups":false,"group_ids":[7],"ban_threshold":1}`,
			groupID:  nil,
			model:    "gpt-5",
			wantLogs: 0,
		},
		{
			name:     "excluded model",
			config:   `{"all_groups":true,"model_filter":{"type":"include","models":["gpt-4o"]},"ban_threshold":1}`,
			groupID:  &groupID,
			model:    "gpt-5",
			wantLogs: 0,
		},
		{
			name:       "included group and model",
			config:     `{"enabled":false,"mode":"off","sample_rate":0,"all_groups":false,"group_ids":[7],"model_filter":{"type":"include","models":["gpt-5"]},"ban_threshold":1}`,
			groupID:    &groupID,
			model:      "gpt-5",
			wantCalls:  []bool{false},
			wantLogs:   1,
			wantBanned: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &banCountArgsTestRepo{}
			userRepo := &contentModerationTestUserRepo{user: &User{ID: 1, Role: RoleUser, Status: StatusActive}}
			svc := NewContentModerationService(
				&contentModerationTestSettingRepo{values: map[string]string{
					SettingKeyRiskControlEnabled:      "true",
					SettingKeyContentModerationConfig: tt.config,
				}},
				repo, nil, nil, userRepo, nil, nil, nil,
			)

			svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
				UserID:  1,
				GroupID: tt.groupID,
				Model:   tt.model,
			})

			if tt.wantCalls == nil {
				require.Empty(t, repo.snapshotCountCalls())
			} else {
				require.Equal(t, tt.wantCalls, repo.snapshotCountCalls())
			}
			require.Len(t, repo.snapshotLogs(), tt.wantLogs)
			require.Equal(t, tt.wantBanned, userRepo.user.Status == StatusDisabled)
			if tt.wantBanned {
				require.Len(t, userRepo.updated, 1)
			} else {
				require.Empty(t, userRepo.updated)
			}
		})
	}
}

func TestRecordCyberPolicyEvent_InitialRuntimeSnapshotLoadFailureSkipsEvent(t *testing.T) {
	repo := &banCountArgsTestRepo{}
	settingRepo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: `{invalid`,
	}}
	svc := NewContentModerationService(settingRepo, repo, nil, nil, nil, nil, nil, nil)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID: 1,
		Model:  "gpt-5",
	})

	require.Empty(t, repo.snapshotCountCalls())
	require.Empty(t, repo.snapshotLogs())
	getValue, getMultiple := settingRepo.calls()
	require.Zero(t, getValue)
	require.GreaterOrEqual(t, getMultiple, 1)
}

func TestRecordCyberPolicyEvent_RuntimeSnapshotRefreshFailureKeepsStaleScope(t *testing.T) {
	repo := &banCountArgsTestRepo{}
	settingRepo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: `{"all_groups":true,"model_filter":{"type":"include","models":["gpt-5"]}}`,
	}}
	svc := NewContentModerationService(settingRepo, repo, nil, nil, nil, nil, nil, nil)
	svc.runtimeCacheTTL = time.Minute

	_, err := svc.loadRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	current := svc.runtimeSnapshot.Load()
	require.NotNil(t, current)
	expired := *current
	expired.loadedAt = time.Now().Add(-2 * time.Minute)
	svc.runtimeSnapshot.Store(&expired)
	settingRepo.failMultiple(errors.New("database unavailable"))

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID: 1,
		Model:  "gpt-5",
	})

	require.Len(t, repo.snapshotLogs(), 1)
	require.Eventually(t, func() bool {
		_, calls := settingRepo.calls()
		return calls == 2
	}, time.Second, time.Millisecond)
	getValue, getMultiple := settingRepo.calls()
	require.Zero(t, getValue)
	require.Equal(t, 2, getMultiple)
}

// TestRecordCyberPolicyEvent_CreateLogBeforeEmail verifies F7: the moderation
// log is persisted BEFORE email delivery, and EmailSent is patched afterwards —
// SMTP hangs can no longer swallow the audit record.
//
// Note on email ordering: EmailService is a concrete type with no injectable
// send interface, so SMTP-success cannot be simulated in unit tests.
// With emailService=nil the email block is skipped and UpdateLogEmailSent is not
// called (correct: logPersisted && emailSent guard). The test therefore asserts
// the two invariants that ARE observable without real SMTP:
//  1. CreateLog runs first (calls[0]=="create").
//  2. The log is stored with EmailSent=false (not pre-set to true).
//
// The update_email_sent path is covered by integration/e2e tests where a real
// (or test-double) SMTP endpoint is available.
func TestRecordCyberPolicyEvent_CreateLogBeforeEmail(t *testing.T) {
	repo := &cyberOrderingTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled: "true",
		}},
		repo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // emailService=nil: email path safely skipped; see doc comment above
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		RequestID:       "req-1",
		UserID:          7,
		UserEmail:       "u@example.com",
		Model:           "gpt-5",
		UpstreamMessage: "blocked",
	})

	calls := repo.snapshot()
	require.GreaterOrEqual(t, len(calls), 1, "CreateLog must be called")
	require.Equal(t, "create", calls[0], "CreateLog must run first (F7: log-before-email)")

	// EmailSent must be false when the log is first persisted (new code sets it
	// false before CreateLog; email result is patched via UpdateLogEmailSent).
	emailSents := repo.snapshotEmailSents()
	require.NotEmpty(t, emailSents, "CreateLog must have captured EmailSent value")
	require.False(t, emailSents[0], "log must be stored with EmailSent=false initially (F7)")

	// With emailService=nil, no email is sent, so UpdateLogEmailSent must NOT
	// be called (logPersisted && emailSent guard correctly suppresses the patch).
	require.NotContains(t, calls, "update_email_sent",
		"UpdateLogEmailSent must not be called when no email was sent")
}

// banCountArgsTestRepo 在 contentModerationTestRepo 基础上记录
// CountFlaggedByUserSince 收到的 excludeCyberPolicy 参数，供透传断言。
type banCountArgsTestRepo struct {
	contentModerationTestRepo
	argsMu     sync.Mutex
	countCalls []bool
}

func (r *banCountArgsTestRepo) CountFlaggedByUserSince(ctx context.Context, userID int64, since time.Time, excludeCyberPolicy bool) (int, error) {
	r.argsMu.Lock()
	r.countCalls = append(r.countCalls, excludeCyberPolicy)
	r.argsMu.Unlock()
	return r.contentModerationTestRepo.CountFlaggedByUserSince(ctx, userID, since, excludeCyberPolicy)
}

func (r *banCountArgsTestRepo) snapshotCountCalls() []bool {
	r.argsMu.Lock()
	defer r.argsMu.Unlock()
	out := make([]bool, len(r.countCalls))
	copy(out, r.countCalls)
	return out
}

func TestApplyFlaggedAccountSideEffects_PassesExcludeCyberFlag(t *testing.T) {
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{}},
		repo, nil, nil, nil, nil, nil, nil,
	)
	userID := int64(42)

	cfgExclude := defaultContentModerationConfig()
	cfgExclude.CyberPolicyExcludeFromBanCount = true
	svc.applyFlaggedAccountSideEffects(context.Background(), cfgExclude, &ContentModerationLog{Flagged: true, UserID: &userID})

	cfgDefault := defaultContentModerationConfig() // 默认 false
	svc.applyFlaggedAccountSideEffects(context.Background(), cfgDefault, &ContentModerationLog{Flagged: true, UserID: &userID})

	cfgWhitelist := defaultContentModerationConfig()
	cfgWhitelist.CyberPolicyWhitelistUserIDs = []int64{userID}
	svc.applyFlaggedAccountSideEffects(context.Background(), cfgWhitelist, &ContentModerationLog{Flagged: true, UserID: &userID})

	require.Equal(t, []bool{true, false, true}, repo.snapshotCountCalls(),
		"全局开关或用户白名单都必须让历史 cyber_policy 记录退出 COUNT 查询")
}

func TestRecordCyberPolicyEvent_ExcludeFromBanCount_SkipsBanJudgment(t *testing.T) {
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: `{"cyber_policy_exclude_from_ban_count":true}`,
		}},
		repo, nil, nil, nil, nil, nil, nil,
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID:          1,
		UserEmail:       "u@x.com",
		Model:           "gpt-5",
		Endpoint:        "/v1/responses",
		UpstreamMessage: "flagged",
		UpstreamStatus:  400,
	})

	require.Empty(t, repo.snapshotCountCalls(), "开关开时不得执行封号计数查询")
	logs := repo.snapshotLogs()
	require.Len(t, logs, 1, "风控日志必须照记")
	require.True(t, logs[0].Flagged, "日志仍标记 Flagged=true（列表可见可筛）")
	require.Equal(t, "cyber_policy", logs[0].Action)
	require.Equal(t, 0, logs[0].ViolationCount, "不参与计数时 ViolationCount 保持 0")
	require.False(t, logs[0].AutoBanned)
}

func TestRecordCyberPolicyEvent_DefaultCountsTowardBan(t *testing.T) {
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled: "true",
		}},
		repo, nil, nil, nil, nil, nil, nil,
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID:          1,
		UserEmail:       "u@x.com",
		Model:           "gpt-5",
		Endpoint:        "/v1/responses",
		UpstreamMessage: "flagged",
		UpstreamStatus:  400,
	})

	require.Equal(t, []bool{false}, repo.snapshotCountCalls(),
		"默认配置必须执行计数查询且不排除 cyber 行")
	logs := repo.snapshotLogs()
	require.Len(t, logs, 1)
	require.GreaterOrEqual(t, logs[0].ViolationCount, 1, "默认路径行为不变（现状回归）")
}

func TestRecordCyberPolicyEvent_WhitelistUserSkipsBanCountAndKeepsLog(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.CyberPolicyWhitelistUserIDs = []int64{1}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled:      "true",
			SettingKeyContentModerationConfig: string(raw),
		}},
		repo, nil, nil, nil, nil, nil, nil,
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID: 1, UserEmail: "u@example.com", Model: "gpt-5", UpstreamMessage: "blocked",
	})

	require.Empty(t, repo.snapshotCountCalls(), "白名单用户不得执行 cyber_policy 封号计数")
	logs := repo.snapshotLogs()
	require.Len(t, logs, 1)
	require.Equal(t, ContentModerationActionCyberPolicy, logs[0].Action)
	require.Zero(t, logs[0].ViolationCount)
	require.False(t, logs[0].AutoBanned)
}

func TestRecordCyberPolicyEvent_GroupOutOfScopeSkipsAllModerationSideEffects(t *testing.T) {
	smtpServer := startNotificationEmailTestSMTPServer(t)
	cfg := defaultContentModerationConfig()
	cfg.AllGroups = false
	cfg.GroupIDs = []int64{2}
	cfg.CyberPolicyWhitelistUserIDs = []int64{1}
	cfg.CyberPolicyNotificationEmails = []string{"ops@example.com"}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings := smtpServer.settings()
	settings[SettingKeyRiskControlEnabled] = "true"
	settings[SettingKeyContentModerationConfig] = string(raw)
	settingRepo := &contentModerationTestSettingRepo{values: settings}
	repo := &banCountArgsTestRepo{}
	svc := NewContentModerationService(
		settingRepo,
		repo, nil, nil, nil, nil, nil, NewEmailService(settingRepo, nil),
	)
	groupID := int64(1)
	selectedGroupID := int64(2)

	require.False(t, svc.CyberPolicyGroupInScope(context.Background(), &groupID))
	require.False(t, svc.CyberPolicyGroupInScope(context.Background(), nil))
	require.True(t, svc.CyberPolicyGroupInScope(context.Background(), &selectedGroupID))
	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID: 1, GroupID: &groupID, UserEmail: "u@example.com", UpstreamMessage: "blocked",
	})

	require.Empty(t, repo.snapshotCountCalls())
	require.Empty(t, repo.snapshotLogs())
	require.Zero(t, smtpServer.messageCount())
}

func TestCyberPolicyGroupInScope_AllGroupsIncludesRequestsWithoutGroup(t *testing.T) {
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{}},
		nil, nil, nil, nil, nil, nil, nil,
	)
	groupID := int64(9)

	require.True(t, svc.CyberPolicyGroupInScope(context.Background(), &groupID))
	require.True(t, svc.CyberPolicyGroupInScope(context.Background(), nil))
}

func TestRecordCyberPolicyEvent_WhitelistSendsUserAndConfiguredRecipients(t *testing.T) {
	smtpServer := startNotificationEmailTestSMTPServer(t)
	cfg := defaultContentModerationConfig()
	cfg.CyberPolicyWhitelistUserIDs = []int64{7}
	cfg.CyberPolicyNotificationEmails = []string{
		"USER@example.com",
		"security@example.com",
		"ops@example.com",
	}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings := smtpServer.settings()
	settings[SettingKeyRiskControlEnabled] = "true"
	settings[SettingKeyContentModerationConfig] = string(raw)
	settingRepo := &contentModerationTestSettingRepo{values: settings}
	emailService := NewEmailService(settingRepo, nil)
	repo := &contentModerationTestRepo{}
	svc := NewContentModerationService(settingRepo, repo, nil, nil, nil, nil, nil, emailService)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID: 7, UserEmail: "User@example.com", Model: "gpt-5", UpstreamMessage: "blocked",
	})

	require.Eventually(t, func() bool { return smtpServer.messageCount() == 3 }, 3*time.Second, 20*time.Millisecond)
	require.Len(t, repo.snapshotLogs(), 1)
}

func TestRecordCyberPolicyEvent_ContinuesAfterRecipientFailure(t *testing.T) {
	smtpServer := startNotificationEmailTestSMTPServer(t)
	smtpServer.rejectRecipient("security@example.com")
	cfg := defaultContentModerationConfig()
	cfg.CyberPolicyWhitelistUserIDs = []int64{7}
	cfg.CyberPolicyNotificationEmails = []string{"security@example.com", "ops@example.com"}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	settings := smtpServer.settings()
	settings[SettingKeyRiskControlEnabled] = "true"
	settings[SettingKeyContentModerationConfig] = string(raw)
	settingRepo := &contentModerationTestSettingRepo{values: settings}
	repo := &cyberOrderingTestRepo{}
	svc := NewContentModerationService(
		settingRepo, repo, nil, nil, nil, nil, nil, NewEmailService(settingRepo, nil),
	)

	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID: 7, UserEmail: "user@example.com", Model: "gpt-5", UpstreamMessage: "blocked",
	})

	require.Eventually(t, func() bool { return smtpServer.messageCount() == 2 }, 3*time.Second, 20*time.Millisecond)
	require.Contains(t, repo.snapshot(), "update_email_sent", "任一收件人成功时必须回写 email_sent=true")
}
