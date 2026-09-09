package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexAuxiliaryEligibilityAccountRepo struct {
	AccountRepository
	accounts []Account
	parents  map[int64]*Account
	byID     map[int64]*Account
	getByID  func(int64) (*Account, error)
	getCalls int
	listErr  error
	getErr   error
}

func (r *codexAuxiliaryEligibilityAccountRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	return r.accounts, r.listErr
}

func (r *codexAuxiliaryEligibilityAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.getCalls++
	if r.getByID != nil {
		return r.getByID(id)
	}
	if r.getErr != nil {
		return nil, r.getErr
	}
	if account := r.byID[id]; account != nil {
		return account, nil
	}
	if parent := r.parents[id]; parent != nil {
		return parent, nil
	}
	return nil, ErrAccountNotFound
}

func newCodexAuxiliaryEligibilityAccount(now time.Time) Account {
	return Account{
		ID:          91,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"plan_type":               "plus",
			"subscription_expires_at": now.Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func TestListCodexAuxiliaryAccountsRequiresUnexpiredPaidOAuthSubscription(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tests := []struct {
		name   string
		mutate func(*Account)
		want   bool
	}{
		{name: "plus", want: true},
		{name: "pro", mutate: func(a *Account) { a.Credentials["plan_type"] = "pro" }, want: true},
		{name: "pro_lite", mutate: func(a *Account) { a.Credentials["plan_type"] = "pro_lite" }, want: true},
		{name: "prolite", mutate: func(a *Account) { a.Credentials["plan_type"] = "prolite" }, want: true},
		{name: "pro_lite_hyphen", mutate: func(a *Account) { a.Credentials["plan_type"] = "pro-lite" }, want: true},
		{name: "normalized_plan", mutate: func(a *Account) { a.Credentials["plan_type"] = "  PLUS  " }, want: true},
		{name: "legacy_plan_fallback", mutate: func(a *Account) {
			delete(a.Credentials, "plan_type")
			a.Credentials["chatgpt_plan_type"] = "pro"
		}, want: true},
		{name: "whitespace_plan_fallback", mutate: func(a *Account) {
			a.Credentials["plan_type"] = " "
			a.Credentials["chatgpt_plan_type"] = "pro_lite"
		}, want: true},
		{name: "canonical_free_does_not_use_legacy_paid", mutate: func(a *Account) {
			a.Credentials["plan_type"] = "free"
			a.Credentials["chatgpt_plan_type"] = "pro"
		}},
		{name: "team", mutate: func(a *Account) { a.Credentials["plan_type"] = "team" }},
		{name: "business", mutate: func(a *Account) { a.Credentials["plan_type"] = "business" }},
		{name: "unknown_plan", mutate: func(a *Account) { a.Credentials["plan_type"] = "custom_paid" }},
		{name: "missing_plan", mutate: func(a *Account) { delete(a.Credentials, "plan_type") }},
		{name: "missing_subscription_expiry", mutate: func(a *Account) { delete(a.Credentials, "subscription_expires_at") }},
		{name: "invalid_subscription_expiry", mutate: func(a *Account) { a.Credentials["subscription_expires_at"] = "unknown" }},
		{name: "expired_subscription", mutate: func(a *Account) { a.Credentials["subscription_expires_at"] = past.UTC().Format(time.RFC3339) }},
		{name: "unix_subscription_expiry", mutate: func(a *Account) { a.Credentials["subscription_expires_at"] = future.Unix() }, want: true},
		{name: "token_expiry_cannot_prove_subscription", mutate: func(a *Account) {
			delete(a.Credentials, "subscription_expires_at")
			a.Credentials["expires_at"] = future.UTC().Format(time.RFC3339)
		}},
		{name: "expired_refreshable_token_is_not_expired_subscription", mutate: func(a *Account) {
			a.Credentials["expires_at"] = past.UTC().Format(time.RFC3339)
			a.Credentials["refresh_token"] = "test-refresh-token"
		}, want: true},
		{name: "api_key", mutate: func(a *Account) { a.Type = AccountTypeAPIKey }},
		{name: "setup_token", mutate: func(a *Account) { a.Type = AccountTypeSetupToken }},
		{name: "foreign_oauth", mutate: func(a *Account) { a.Platform = PlatformAnthropic }},
		{name: "implicit_platform", mutate: func(a *Account) { a.Platform = "" }},
		{name: "personal_access_token", mutate: func(a *Account) { a.Credentials["auth_mode"] = OpenAIAuthModePersonalAccessToken }},
		{name: "legacy_personal_access_token", mutate: func(a *Account) { a.Credentials["openai_auth_mode"] = "personal_access_token" }},
		{name: "agent_identity", mutate: func(a *Account) { a.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity }},
		{name: "disabled", mutate: func(a *Account) { a.Status = "disabled" }},
		{name: "manual_unschedulable", mutate: func(a *Account) { a.Schedulable = false }},
		{name: "local_auto_pause_expired", mutate: func(a *Account) {
			a.ExpiresAt, a.AutoPauseOnExpired = &past, true
		}},
		{name: "local_expiry_without_auto_pause", mutate: func(a *Account) { a.ExpiresAt = &past }, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := newCodexAuxiliaryEligibilityAccount(now)
			if tt.mutate != nil {
				tt.mutate(&account)
			}
			repo := &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{account}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			accounts, err := svc.listCodexAuxiliaryAccounts(context.Background(), &APIKey{ID: 7})
			require.NoError(t, err)
			if tt.want {
				require.Len(t, accounts, 1)
				require.Equal(t, account.ID, accounts[0].ID)
			} else {
				require.Empty(t, accounts)
			}
		})
	}
}

func TestListCodexAuxiliaryAccountsIgnoresModelLimitsButNotCredentialFailures(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	tests := []struct {
		name   string
		mutate func(*Account)
		want   bool
	}{
		{name: "rate_limited_and_overloaded", mutate: func(a *Account) {
			a.RateLimitedAt, a.RateLimitResetAt, a.OverloadUntil = &now, &future, &future
		}, want: true},
		{name: "no_concurrency_capacity", mutate: func(a *Account) { a.Concurrency = 0 }, want: true},
		{name: "quota_threshold_cooldown", mutate: func(a *Account) {
			a.TempUnschedulableUntil = &future
			a.TempUnschedulableReason = BuildTempUnschedReasonPayload(AccountSchedulingThresholdReasonSource, "model usage threshold reached")
		}, want: true},
		{name: "custom_429_cooldown", mutate: func(a *Account) {
			a.TempUnschedulableUntil = &future
			a.TempUnschedulableReason = `{"status_code":429,"error_message":"model rate limit"}`
		}, want: true},
		{name: "oauth_401_cooldown", mutate: func(a *Account) {
			a.TempUnschedulableUntil = &future
			a.TempUnschedulableReason = "OAuth 401: invalid or expired credentials"
		}},
		{name: "custom_401_cooldown", mutate: func(a *Account) {
			a.TempUnschedulableUntil = &future
			a.TempUnschedulableReason = `{"status_code":401,"error_message":"invalid credentials"}`
		}},
		{name: "transport_cooldown", mutate: func(a *Account) {
			a.TempUnschedulableUntil = &future
			a.TempUnschedulableReason = BuildTempUnschedReasonPayload("upstream_transport", "proxy unavailable")
		}},
		{name: "unknown_cooldown", mutate: func(a *Account) { a.TempUnschedulableUntil = &future }},
		{name: "expired_auth_cooldown", mutate: func(a *Account) {
			a.TempUnschedulableUntil = &past
			a.TempUnschedulableReason = "OAuth 401: invalid or expired credentials"
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := newCodexAuxiliaryEligibilityAccount(now)
			tt.mutate(&account)
			svc := &OpenAIGatewayService{accountRepo: &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{account}}}
			accounts, err := svc.listCodexAuxiliaryAccounts(context.Background(), &APIKey{ID: 7})
			require.NoError(t, err)
			require.Equal(t, tt.want, len(accounts) == 1)
		})
	}
}

func TestListCodexAuxiliaryAccountsUsesShadowCredentialOwnerEligibility(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	parentID := int64(101)
	tests := []struct {
		name   string
		mutate func(parent, child *Account)
		want   bool
	}{
		{name: "inherits_paid_subscription", want: true},
		{name: "parent_manual_schedule_switch_is_independent", mutate: func(parent, _ *Account) { parent.Schedulable = false }, want: true},
		{name: "parent_model_rate_limits_are_independent", mutate: func(parent, _ *Account) {
			parent.RateLimitResetAt, parent.OverloadUntil = &future, &future
		}, want: true},
		{name: "parent_expired_subscription", mutate: func(parent, _ *Account) {
			parent.Credentials["subscription_expires_at"] = past.UTC().Format(time.RFC3339)
		}},
		{name: "child_paid_metadata_cannot_override_free_parent", mutate: func(parent, child *Account) {
			parent.Credentials["plan_type"] = "free"
			child.Credentials = newCodexAuxiliaryEligibilityAccount(now).Credentials
		}},
		{name: "parent_disabled", mutate: func(parent, _ *Account) { parent.Status = "disabled" }},
		{name: "parent_auth_cooldown", mutate: func(parent, _ *Account) {
			parent.TempUnschedulableUntil = &future
			parent.TempUnschedulableReason = "OAuth 401: invalid credentials"
		}},
		{name: "child_disabled", mutate: func(_, child *Account) { child.Status = "disabled" }},
		{name: "child_manual_unschedulable", mutate: func(_, child *Account) { child.Schedulable = false }},
		{name: "child_transport_cooldown", mutate: func(_, child *Account) {
			child.TempUnschedulableUntil = &future
			child.TempUnschedulableReason = "upstream transport unavailable"
		}},
		{name: "parent_is_shadow", mutate: func(parent, _ *Account) {
			grandparentID := int64(102)
			parent.ParentAccountID = &grandparentID
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := newCodexAuxiliaryEligibilityAccount(now)
			parent.ID = parentID
			child := newCodexAuxiliaryEligibilityAccount(now)
			child.ParentAccountID, child.Credentials = &parentID, nil
			if tt.mutate != nil {
				tt.mutate(&parent, &child)
			}
			repo := &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{child}, parents: map[int64]*Account{parentID: &parent}}
			svc := &OpenAIGatewayService{accountRepo: repo}
			accounts, err := svc.listCodexAuxiliaryAccounts(context.Background(), &APIKey{ID: 7})
			require.NoError(t, err)
			if tt.want {
				require.Len(t, accounts, 1)
				require.Equal(t, child.ID, accounts[0].ID, "retain the eligible group candidate, not its potentially ungrouped parent")
			} else {
				require.Empty(t, accounts)
			}
		})
	}
}

func TestListCodexAuxiliaryAccountsDistinguishesMissingOwnerFromRepositoryFailure(t *testing.T) {
	now := time.Now()
	parentID := int64(101)
	child := newCodexAuxiliaryEligibilityAccount(now)
	child.ParentAccountID, child.Credentials = &parentID, nil
	eligible := newCodexAuxiliaryEligibilityAccount(now)
	eligible.ID++
	t.Run("missing_parent_allows_another_eligible_account", func(t *testing.T) {
		repo := &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{child, eligible}}
		accounts, err := (&OpenAIGatewayService{accountRepo: repo}).listCodexAuxiliaryAccounts(context.Background(), &APIKey{ID: 7})
		require.NoError(t, err)
		require.Len(t, accounts, 1)
		require.Equal(t, eligible.ID, accounts[0].ID)
	})
	t.Run("parent_database_error_cannot_trigger_account_switch", func(t *testing.T) {
		failure := errors.New("parent database unavailable")
		repo := &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{child, eligible}, getErr: failure}
		accounts, err := (&OpenAIGatewayService{accountRepo: repo}).listCodexAuxiliaryAccounts(context.Background(), &APIKey{ID: 7})
		require.ErrorIs(t, err, failure)
		require.Empty(t, accounts)
	})
	t.Run("candidate_query_error_is_returned", func(t *testing.T) {
		failure := errors.New("candidate database unavailable")
		repo := &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{eligible}, listErr: failure}
		accounts, err := (&OpenAIGatewayService{accountRepo: repo}).listCodexAuxiliaryAccounts(context.Background(), &APIKey{ID: 7})
		require.ErrorIs(t, err, failure)
		require.Empty(t, accounts)
	})
}

func TestForwardCodexHistoryNotesRevalidatesEligibilityAfterTokenResolution(t *testing.T) {
	now := time.Now()
	databaseFailure := errors.New("account reload unavailable")
	tests := []struct {
		name            string
		mutate          func(*Account)
		reloadErr       error
		withoutProvider bool
		wantError       bool
	}{
		{name: "still_paid_and_active"},
		{name: "subscription_became_free", mutate: func(a *Account) { a.Credentials["plan_type"] = "free" }, wantError: true},
		{name: "subscription_expired", mutate: func(a *Account) {
			a.Credentials["subscription_expires_at"] = now.Add(-time.Hour).UTC().Format(time.RFC3339)
		}, wantError: true},
		{name: "subscription_expiry_disappeared", mutate: func(a *Account) { delete(a.Credentials, "subscription_expires_at") }, wantError: true},
		{name: "account_disabled", mutate: func(a *Account) { a.Status = "disabled" }, wantError: true},
		{name: "account_no_longer_schedulable", mutate: func(a *Account) { a.Schedulable = false }, wantError: true},
		{name: "reload_database_failure", reloadErr: databaseFailure, wantError: true},
		{name: "account_deleted", reloadErr: ErrAccountNotFound, wantError: true},
		{name: "without_refresh_provider_uses_captured_account", reloadErr: databaseFailure, withoutProvider: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := newCodexAuxiliaryEligibilityAccount(now)
			candidate.Credentials["access_token"] = "test-access-token"
			candidate.Credentials["expires_at"] = now.Add(2 * time.Hour).UTC().Format(time.RFC3339)
			candidate.Credentials["chatgpt_account_id"] = "test-chatgpt-account"
			latest := newCodexAuxiliaryEligibilityAccount(now)
			latest.Credentials["access_token"] = "test-access-token"
			latest.Credentials["expires_at"] = candidate.Credentials["expires_at"]
			latest.Credentials["chatgpt_account_id"] = "test-chatgpt-account"
			if tt.mutate != nil {
				tt.mutate(&latest)
			}
			alternative := newCodexAuxiliaryEligibilityAccount(now)
			alternative.ID++
			repo := &codexAuxiliaryEligibilityAccountRepo{
				accounts: []Account{candidate, alternative},
				byID:     map[int64]*Account{candidate.ID: &latest},
				getErr:   tt.reloadErr,
			}
			upstreamCalls := 0
			svc := &OpenAIGatewayService{
				accountRepo: repo,
				cfg:         &config.Config{JWT: config.JWTConfig{Secret: "auxiliary-eligibility-recheck-secret"}},
				httpUpstream: &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
					upstreamCalls++
					require.Equal(t, candidate.ID, accountID, "token resolution must not silently move ownership")
					return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
				}},
			}
			if !tt.withoutProvider {
				svc.openAITokenProvider = NewOpenAITokenProvider(repo, nil, nil)
			}
			apiKey := &APIKey{ID: 7}
			bindingKey := codexAuxiliaryAccountBindingKey(apiKey, codexAuxiliaryStickyTestSession)
			svc.codexAuxiliarySticky.Store(bindingKey, candidate.ID)
			c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
			resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
			if tt.wantError {
				require.Error(t, err)
				require.Nil(t, resp)
				require.Zero(t, upstreamCalls, "freshly ineligible accounts must be stopped before network dispatch")
				if tt.reloadErr == databaseFailure {
					require.ErrorIs(t, err, databaseFailure)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				require.NoError(t, resp.Body.Close())
				require.Equal(t, 1, upstreamCalls)
			}
			if tt.withoutProvider {
				require.Zero(t, repo.getCalls, "a path without token refresh must not add a redundant account reload")
			} else {
				require.Equal(t, 1, repo.getCalls, "the current account must be checked after the provider returns its token")
			}
			owner, ok := svc.codexAuxiliarySticky.Load(bindingKey)
			require.True(t, ok)
			require.Equal(t, candidate.ID, owner, "eligibility changes must not rebind this in-flight request to another account")
		})
	}
}

func TestForwardCodexHistoryNotesRevalidatesShadowOwnerAfterTokenResolution(t *testing.T) {
	now := time.Now()
	parent := newCodexAuxiliaryEligibilityAccount(now)
	parent.ID = 101
	parent.Credentials["access_token"] = "test-shadow-parent-token"
	parent.Credentials["expires_at"] = now.Add(2 * time.Hour).UTC().Format(time.RFC3339)
	parent.Credentials["chatgpt_account_id"] = "test-shadow-parent-account"
	child := newCodexAuxiliaryEligibilityAccount(now)
	child.ParentAccountID, child.Credentials = &parent.ID, nil
	refreshedParent := newCodexAuxiliaryEligibilityAccount(now)
	refreshedParent.ID = parent.ID
	refreshedParent.Credentials["plan_type"] = "free"
	childReloaded, parentRechecked := false, false
	repo := &codexAuxiliaryEligibilityAccountRepo{accounts: []Account{child}}
	repo.getByID = func(id int64) (*Account, error) {
		switch id {
		case child.ID:
			childReloaded = true
			return &child, nil
		case parent.ID:
			if childReloaded {
				parentRechecked = true
				return &refreshedParent, nil
			}
			return &parent, nil
		default:
			return nil, ErrAccountNotFound
		}
	}
	upstreamCalls := 0
	svc := &OpenAIGatewayService{
		accountRepo:         repo,
		openAITokenProvider: NewOpenAITokenProvider(repo, nil, nil),
		cfg:                 &config.Config{JWT: config.JWTConfig{Secret: "auxiliary-shadow-recheck-secret"}},
		httpUpstream: &codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
			upstreamCalls++
			return codexAuxiliaryStickyTestResponse(http.StatusOK), nil
		}},
	}
	apiKey := &APIKey{ID: 7}
	bindingKey := codexAuxiliaryAccountBindingKey(apiKey, codexAuxiliaryStickyTestSession)
	svc.codexAuxiliarySticky.Store(bindingKey, child.ID)
	c := newCodexAuxiliaryStickyTestContext(context.Background(), apiKey, "/alpha/history/v2/list_windows")
	resp, err := svc.ForwardCodexHistoryNotes(c.Request.Context(), c, apiKey, "/alpha/history/v2/list_windows", codexAuxiliaryStickyTestBody)
	require.Error(t, err)
	require.Nil(t, resp)
	require.True(t, childReloaded)
	require.True(t, parentRechecked, "a shadow's latest credential owner must be rechecked after token resolution")
	require.Zero(t, upstreamCalls)
	owner, ok := svc.codexAuxiliarySticky.Load(bindingKey)
	require.True(t, ok)
	require.Equal(t, child.ID, owner)
}
