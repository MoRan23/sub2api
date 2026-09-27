//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func candyAgentCASCredentials() map[string]any {
	return map[string]any{
		"auth_mode":          service.OpenAIAuthModeAgentIdentity,
		"agent_private_key":  "synthetic-signing-key",
		"agent_runtime_id":   "synthetic-runtime",
		"access_token":       "synthetic-access",
		"refresh_token":      "synthetic-refresh",
		"id_token":           "synthetic-id",
		"client_id":          "synthetic-client",
		"chatgpt_account_id": "synthetic-account",
		"chatgpt_user_id":    "synthetic-user",
		"organization_id":    "synthetic-organization",
		"_token_version":     float64(3),
		"base_url":           "https://fixture.invalid/v1",
		"model_mapping":      map[string]any{"fixture": "fixture-upstream"},
	}
}

// Match the auth snapshot plus task/signing fields used by the candy executor;
// every missing value must compare as JSON null rather than being ignored.
func candyAgentCASExpected(credentials map[string]any) map[string]any {
	expected := make(map[string]any)
	for _, key := range []string{"access_token", "refresh_token", "id_token", "client_id", "auth_mode", "openai_auth_mode", "_token_version", "chatgpt_account_id", "chatgpt_user_id", "organization_id", "agent_private_key", "agent_runtime_id", "task_id"} {
		expected[key] = credentials[key]
	}
	return expected
}

func TestCandyAgentTaskPostgresCASUpdatesOnlyTaskID(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "candy-agent-cas", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: candyAgentCASCredentials()})
	before, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.True(t, before.IsOpenAIAgentIdentity())
	expected := candyAgentCASExpected(before.Credentials)
	updated, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, expected, nil, map[string]any{"task_id": "synthetic-task"}, nil)
	require.NoError(t, err)
	require.True(t, updated)
	after, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	want := maps.Clone(before.Credentials)
	want["task_id"] = "synthetic-task"
	require.Equal(t, want, after.Credentials, "the task callback must preserve every other credential/configuration key")
	require.Equal(t, before.Status, after.Status)
	require.Equal(t, before.Schedulable, after.Schedulable)
	require.Equal(t, before.ErrorMessage, after.ErrorMessage)
	var events int
	require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, []any{account.ID}, &events))
	require.Equal(t, 1, events)
}

func TestCandyAgentTaskPostgresCASRejectsReplacedIdentity(t *testing.T) {
	for _, field := range []string{"agent_private_key", "agent_runtime_id", "chatgpt_account_id", "chatgpt_user_id", "organization_id", "access_token", "_token_version"} {
		t.Run(field, func(t *testing.T) {
			tx := testEntTx(t)
			ctx := dbent.NewTxContext(context.Background(), tx)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "candy-agent-replaced-" + field, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: candyAgentCASCredentials()})
			before, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			replaced := maps.Clone(before.Credentials)
			replaced[field] = "replacement"
			replaced["task_id"] = "new-authorization-task"
			data, err := json.Marshal(replaced)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, `UPDATE accounts SET credentials=$1::jsonb WHERE id=$2`, string(data), account.ID)
			require.NoError(t, err)
			updated, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, candyAgentCASExpected(before.Credentials), nil, map[string]any{"task_id": "stale-task"}, nil)
			require.NoError(t, err)
			require.False(t, updated)
			after, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, replaced, after.Credentials)
			var events int
			require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, []any{account.ID}, &events))
			require.Zero(t, events)
		})
	}
}

func TestCandyAgentTaskPostgresCASDoesNotOverwriteExistingTask(t *testing.T) {
	for _, initialTask := range []any{nil, ""} {
		name := "missing"
		if initialTask != nil {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			tx := testEntTx(t)
			ctx := dbent.NewTxContext(context.Background(), tx)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			credentials := candyAgentCASCredentials()
			if initialTask != nil {
				credentials["task_id"] = initialTask
			}
			account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "candy-agent-existing", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: credentials})
			before, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			expected := candyAgentCASExpected(before.Credentials)
			updated, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, expected, nil, map[string]any{"task_id": "winner-task"}, nil)
			require.NoError(t, err)
			require.True(t, updated)
			updated, err = repo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, expected, nil, map[string]any{"task_id": "late-task"}, nil)
			require.NoError(t, err)
			require.False(t, updated)
			after, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, "winner-task", after.GetCredential("task_id"))
		})
	}
}

func TestCandyAgentTaskPostgresCASRejectsChangedAccountBinding(t *testing.T) {
	for _, binding := range []string{"parent", "proxy"} {
		t.Run(binding, func(t *testing.T) {
			tx := testEntTx(t)
			ctx := dbent.NewTxContext(context.Background(), tx)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "candy-agent-binding", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: candyAgentCASCredentials()})
			before, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			if binding == "parent" {
				parent := mustCreateAccount(t, tx.Client(), &service.Account{Name: "candy-agent-parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: candyAgentCASCredentials()})
				_, err = tx.ExecContext(ctx, `UPDATE accounts SET parent_account_id=$1,quota_dimension=$2 WHERE id=$3`, parent.ID, service.QuotaDimensionSpark, account.ID)
			} else {
				proxy := mustCreateProxy(t, tx.Client(), &service.Proxy{Name: "candy-agent-new-proxy", Host: "127.0.0.1", Port: 19876})
				_, err = tx.ExecContext(ctx, `UPDATE accounts SET proxy_id=$1 WHERE id=$2`, proxy.ID, account.ID)
			}
			require.NoError(t, err)
			updated, err := repo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, candyAgentCASExpected(before.Credentials), nil, map[string]any{"task_id": "stale-task"}, nil)
			require.NoError(t, err)
			require.False(t, updated)
			after, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, before.Credentials, after.Credentials)
		})
	}
}

func TestCandyAgentTaskPostgresCASConcurrentCallbackHasOneWinner(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	account := mustCreateAccount(t, client, &service.Account{Name: "candy-agent-race-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: candyAgentCASCredentials()})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM scheduler_outbox WHERE account_id=$1`, account.ID)
		_ = client.Account.DeleteOneID(account.ID).Exec(context.Background())
	})
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	before, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	expected := candyAgentCASExpected(before.Credentials)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	var callErrors []error
	for _, task := range []string{"callback-a", "callback-b"} {
		wg.Add(1)
		go func(task string) {
			defer wg.Done()
			<-start
			separateRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
			updated, err := separateRepo.PatchOpenAIOAuthCredentialsIfUnchanged(ctx, account.ID, expected, nil, map[string]any{"task_id": task}, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				callErrors = append(callErrors, err)
			}
			if updated {
				winners++
			}
		}(task)
	}
	close(start)
	wg.Wait()
	require.Empty(t, callErrors)
	require.Equal(t, 1, winners)
	after, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Contains(t, []string{"callback-a", "callback-b"}, after.GetCredential("task_id"))
	want := maps.Clone(before.Credentials)
	want["task_id"] = after.GetCredential("task_id")
	require.Equal(t, want, after.Credentials)
}
