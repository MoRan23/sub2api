//go:build integration

package repository

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestAccountFastSettingsAdminIntentAndStaleWriters() {
	account := mustCreateAccount(s.T(), s.client, &service.Account{Name: "fast-config", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Extra: map[string]any{service.ForceOpenAIFastExtraKey: true, service.FreeOpenAIFastExtraKey: true}})
	admin := service.NewAdminService(nil, nil, nil, s.repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, s.client, nil, nil, nil, nil, nil, nil, nil, nil)
	stale, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().NoError(admin.UpdateAccountExtra(s.ctx, account.ID, map[string]any{service.ForceOpenAIFastExtraKey: false, service.ForceOpenAIUltrafastExtraKey: true}))
	stale.Name = "stale form"
	s.Require().NoError(s.repo.Update(s.ctx, stale))
	s.Require().NoError(s.repo.UpdateExtra(s.ctx, account.ID, map[string]any{service.ForceOpenAIUltrafastExtraKey: false, "unrelated": true}))
	_, err = s.repo.BulkUpdate(s.ctx, []int64{account.ID}, service.AccountBulkUpdate{Extra: map[string]any{service.FreeOpenAIFastExtraKey: false}})
	s.Require().NoError(err)
	stored, err := s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().False(stored.IsForceOpenAIFastEnabled())
	s.Require().True(stored.IsForceOpenAIUltrafastEnabled())
	s.Require().True(stored.IsFreeOpenAIFastEnabled())
	s.Require().Equal(true, stored.Extra["unrelated"])
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{service.ForceOpenAIFastExtraKey: true}})
	s.Require().Error(err)
	_, err = admin.BulkUpdateAccounts(s.ctx, &service.BulkUpdateAccountsInput{AccountIDs: []int64{account.ID}, Extra: map[string]any{service.ForceOpenAIUltrafastExtraKey: false, service.ForceOpenAIFastExtraKey: true, service.FreeOpenAIUltrafastExtraKey: true}})
	s.Require().NoError(err)
	stored, err = s.repo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(stored.IsForceOpenAIFastEnabled())
	s.Require().False(stored.IsForceOpenAIUltrafastEnabled())
	s.Require().True(stored.IsFreeOpenAIFastEnabled())
	s.Require().True(stored.IsFreeOpenAIUltrafastEnabled())
}

func (s *AccountRepoSuite) TestAccountFastSettingsPatchRechecksConcurrentForceChange() {
	client := testEntClient(s.T())
	baseRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	repo := &accountFastSettingsReadBarrierRepo{accountRepository: baseRepo, read: make(chan *service.Account, 1)}
	account := mustCreateAccount(s.T(), client, &service.Account{Name: "fast-config-race", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	s.T().Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id=$1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
	})
	admin := service.NewAdminService(nil, nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, client, nil, nil, nil, nil, nil, nil, nil, nil)
	tx, err := client.Tx(s.ctx)
	s.Require().NoError(err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Client().ExecContext(s.ctx, `UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || '{"force_openai_fast":true}'::jsonb WHERE id=$1`, account.ID)
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- admin.UpdateAccountExtra(ctx, account.ID, map[string]any{service.ForceOpenAIUltrafastExtraKey: true})
	}()
	select {
	case stale := <-repo.read:
		s.Require().False(stale.IsForceOpenAIFastEnabled(), "admin validation must see the pre-commit row")
	case <-ctx.Done():
		s.FailNow("admin did not read the pre-commit snapshot")
	}
	s.Require().NoError(tx.Commit())
	s.Require().Equal("OPENAI_ACCOUNT_FAST_CONFLICT", infraerrors.Reason(<-done))
	stored, err := baseRepo.GetByID(s.ctx, account.ID)
	s.Require().NoError(err)
	s.Require().True(stored.IsForceOpenAIFastEnabled())
	s.Require().False(stored.IsForceOpenAIUltrafastEnabled())
}

type accountFastSettingsReadBarrierRepo struct {
	*accountRepository
	read chan *service.Account
}

func (r *accountFastSettingsReadBarrierRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	account, err := r.accountRepository.GetByID(ctx, id)
	if err == nil {
		select {
		case r.read <- account:
		default:
		}
	}
	return account, err
}
