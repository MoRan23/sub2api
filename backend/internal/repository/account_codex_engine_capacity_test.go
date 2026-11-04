package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type codexEngineCapacityCacheSpy struct {
	service.SchedulerCache
	setAccounts int
}

func (s *codexEngineCapacityCacheSpy) SetAccount(context.Context, *service.Account) error {
	s.setAccounts++
	return nil
}

type codexEngineCapacityMetadataArgument struct {
	expected      map[string]any
	observationID string
}

func (a *codexEngineCapacityMetadataArgument) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	var actual map[string]any
	if err := json.Unmarshal([]byte(text), &actual); err != nil {
		return false
	}
	id, ok := actual["observation_id"].(string)
	if !ok {
		return false
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil {
		return false
	}
	delete(actual, "observation_id")
	if !reflect.DeepEqual(a.expected, actual) {
		return false
	}
	a.observationID = id
	return true
}

func newCodexEngineCapacityMock(t *testing.T) (*accountRepository, sqlmock.Sqlmock, *[]string) {
	t.Helper()
	queries := []string{}
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		queries = append(queries, strings.Join(strings.Fields(actual), " "))
		return sqlmock.QueryMatcherRegexp.Match(expected, actual)
	})))
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, nil), mock, &queries
}

func codexEngineCapacityAccount() *service.Account {
	return &service.Account{ID: 27, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://engine.example/v1"},
		Extra:       map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}, Concurrency: 30, Status: service.StatusActive, Schedulable: true, SchedulingRevision: 5}
}

func expectCapacityLock(t *testing.T, m sqlmock.Sqlmock, a *service.Account) {
	t.Helper()
	creds, err := json.Marshal(a.Credentials)
	require.NoError(t, err)
	extra, err := json.Marshal(a.Extra)
	require.NoError(t, err)
	var proxy, parent any
	if a.ProxyID != nil {
		proxy = *a.ProxyID
	}
	if a.ParentAccountID != nil {
		parent = *a.ParentAccountID
	}
	m.ExpectQuery(`SELECT platform, type, parent_account_id, credentials, extra, schedulable, scheduling_revision, status .* FOR NO KEY UPDATE`).
		WithArgs(a.ID).WillReturnRows(sqlmock.NewRows([]string{"platform", "type", "parent", "credentials", "extra", "schedulable", "revision", "status"}).
		AddRow(a.Platform, a.Type, parent, string(creds), string(extra), a.Schedulable, a.SchedulingRevision, a.Status))
	m.ExpectQuery(`SELECT proxy_id, concurrency FROM accounts`).WithArgs(a.ID).
		WillReturnRows(sqlmock.NewRows([]string{"proxy_id", "concurrency"}).AddRow(proxy, a.Concurrency))
}

func capacityMeta(a *service.Account, limit int, owned bool) map[string]any {
	state := map[string]any{"identity": service.CodexEngineCapacityIdentity(a), "limit": limit}
	if owned {
		state["pause_revision"] = a.SchedulingRevision
	}
	return state
}

func expectCapacityWrite(t *testing.T, m sqlmock.Sqlmock, a *service.Account, limit int, schedule bool, owned bool) *codexEngineCapacityMetadataArgument {
	t.Helper()
	state := capacityMeta(a, limit, false)
	if schedule != a.Schedulable {
		m.ExpectQuery(`UPDATE accounts SET schedulable=\$2,updated_at=NOW\(\) WHERE id=\$1 RETURNING scheduling_revision`).
			WithArgs(a.ID, schedule).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(a.SchedulingRevision + 1))
		if !schedule {
			state["pause_revision"] = a.SchedulingRevision + 1
		}
	} else if owned {
		state["pause_revision"] = a.SchedulingRevision
	}
	value, err := json.Marshal(state)
	require.NoError(t, err)
	argument := &codexEngineCapacityMetadataArgument{}
	require.NoError(t, json.Unmarshal(value, &argument.expected))
	concurrency := a.Concurrency
	if limit > 0 {
		concurrency = limit
	}
	m.ExpectExec(`UPDATE accounts SET extra=jsonb_set`).WithArgs(a.ID, argument, concurrency).WillReturnResult(sqlmock.NewResult(0, 1))
	return argument
}

func TestApplyCodexEngineCapacityTransitions(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		currentLimit, limit   int
		paused, owned, manual bool
	}{
		{"first30", -1, 30, false, false, false}, {"decrease20", 30, 20, false, false, false},
		{"zero pauses", 20, 0, false, false, false}, {"resume30", 0, 30, true, true, false},
		{"manual paused", 0, 30, true, true, true}, {"preexisting paused", -1, 30, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, m, _ := newCodexEngineCapacityMock(t)
			a := codexEngineCapacityAccount()
			a.Schedulable = !tc.paused
			if tc.currentLimit >= 0 {
				a.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(a, tc.currentLimit, tc.owned)
			}
			if tc.manual {
				a.SchedulingRevision++
			}
			m.ExpectBegin()
			expectCapacityLock(t, m, a)
			schedule := a.Schedulable
			if tc.limit == 0 {
				schedule = false
			} else if tc.owned && !tc.manual {
				schedule = true
			}
			expectCapacityWrite(t, m, a, tc.limit, schedule, false)
			m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectCommit()
			changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, tc.limit)
			require.NoError(t, err)
			require.True(t, changed)
			require.NoError(t, m.ExpectationsWereMet())
		})
	}
}

func TestApplyCodexEngineCapacityNoChangeDoesNotWrite(t *testing.T) {
	for _, zero := range []bool{false, true} {
		for _, withID := range []bool{false, true} {
			name := map[bool]string{true: "owned zero", false: "positive"}[zero] + "/" + map[bool]string{true: "with observation ID", false: "legacy without ID"}[withID]
			t.Run(name, func(t *testing.T) {
				repo, m, queries := newCodexEngineCapacityMock(t)
				a := codexEngineCapacityAccount()
				limit := 30
				if zero {
					limit = 0
					a.Schedulable = false
				}
				metadata := capacityMeta(a, limit, zero)
				if withID {
					metadata["observation_id"] = uuid.NewString()
				}
				a.Extra[service.CodexEngineCapacitySyncExtraKey] = metadata
				m.ExpectBegin()
				expectCapacityLock(t, m, a)
				m.ExpectRollback()
				changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, limit)
				require.NoError(t, err)
				require.False(t, changed)
				require.Len(t, *queries, 2, "unchanged state must not rotate an existing ID or backfill a legacy ID")
				require.NoError(t, m.ExpectationsWereMet())
			})
		}
	}
}

func TestApplyCodexEngineCapacityRejectsStaleConfigurationAndScheduling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*service.Account)
	}{
		{"key", func(a *service.Account) { a.Credentials["api_key"] = "new" }},
		{"url", func(a *service.Account) { a.Credentials["base_url"] = "https://new.invalid" }},
		{"mode", func(a *service.Account) { a.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic" }},
		{"type", func(a *service.Account) { a.Type = service.AccountTypeOAuth }},
		{"proxy", func(a *service.Account) { id := int64(11); a.ProxyID = &id }},
		{"manual repeat pause", func(a *service.Account) { a.SchedulingRevision++ }},
		{"disabled status", func(a *service.Account) { a.Status = "disabled" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, m, _ := newCodexEngineCapacityMock(t)
			expected := codexEngineCapacityAccount()
			current := *expected
			current.Credentials = maps.Clone(expected.Credentials)
			current.Extra = maps.Clone(expected.Extra)
			tc.mutate(&current)
			m.ExpectBegin()
			expectCapacityLock(t, m, &current)
			m.ExpectRollback()
			changed, err := repo.ApplyCodexEngineCapacity(context.Background(), expected, 0)
			require.NoError(t, err)
			require.False(t, changed)
			require.NoError(t, m.ExpectationsWereMet())
		})
	}
}

func TestApplyCodexEngineCapacityRejectsOlderConcurrentObservation(t *testing.T) {
	repo, m, queries := newCodexEngineCapacityMock(t)
	expected := codexEngineCapacityAccount()
	expected.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(expected, 30, false)
	// Both requests started from the same 30-capacity snapshot. The request
	// reporting 20 has already committed when the older response arrives.
	current := *expected
	current.Extra = maps.Clone(expected.Extra)
	current.Concurrency = 20
	current.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(&current, 20, false)
	require.True(t, service.CodexEngineCapacityIdentityMatches(&current, expected),
		"positive capacity updates do not change identity or scheduling revision")
	m.ExpectBegin()
	expectCapacityLock(t, m, &current)
	m.ExpectRollback()

	changed, err := repo.ApplyCodexEngineCapacity(context.Background(), expected, 30)

	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, *queries, 2, "an older observation cannot overwrite capacity or enqueue another notification")
	require.NoError(t, m.ExpectationsWereMet())
}

func TestApplyCodexEngineCapacityRejectsABAObservation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "versioned snapshot", true: "legacy snapshot"}[legacy], func(t *testing.T) {
			repo, m, queries := newCodexEngineCapacityMock(t)
			expected := codexEngineCapacityAccount()
			expected.Concurrency = 20
			initial := capacityMeta(expected, 20, false)
			previousID := ""
			if !legacy {
				previousID = uuid.NewString()
				initial["observation_id"] = previousID
			}
			expected.Extra[service.CodexEngineCapacitySyncExtraKey] = initial
			current := *expected
			current.Extra = maps.Clone(expected.Extra)
			for _, limit := range []int{30, 20} {
				m.ExpectBegin()
				expectCapacityLock(t, m, &current)
				written := expectCapacityWrite(t, m, &current, limit, true, false)
				m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()
				changed, err := repo.ApplyCodexEngineCapacity(context.Background(), &current, limit)
				require.NoError(t, err)
				require.True(t, changed)
				require.NotEmpty(t, written.observationID)
				require.NotEqual(t, previousID, written.observationID, "each committed state change advances the observation generation")
				previousID = written.observationID
				current.Concurrency = limit
				metadata := capacityMeta(&current, limit, false)
				metadata["observation_id"] = written.observationID
				current.Extra[service.CodexEngineCapacitySyncExtraKey] = metadata
			}
			require.Equal(t, expected.Concurrency, current.Concurrency)
			require.True(t, service.CodexEngineCapacityIdentityMatches(&current, expected))
			before := len(*queries)
			m.ExpectBegin()
			expectCapacityLock(t, m, &current)
			m.ExpectRollback()

			changed, err := repo.ApplyCodexEngineCapacity(context.Background(), expected, 0)

			require.NoError(t, err)
			require.False(t, changed, "the old 20-capacity request cannot pause the account after 20-to-30-to-20")
			require.Len(t, *queries, before+2, "ABA rejection must not write scheduling, metadata or outbox")
			require.NoError(t, m.ExpectationsWereMet())
		})
	}
}

func TestApplyCodexEngineCapacityUnrelatedCredentialChangeDoesNotDiscardObservation(t *testing.T) {
	repo, m, _ := newCodexEngineCapacityMock(t)
	expected := codexEngineCapacityAccount()
	current := *expected
	current.Credentials = maps.Clone(expected.Credentials)
	current.Credentials["model_mapping"] = map[string]any{"public-model": "upstream-model"}
	m.ExpectBegin()
	expectCapacityLock(t, m, &current)
	expectCapacityWrite(t, m, &current, 20, true, false)
	m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectCommit()

	changed, err := repo.ApplyCodexEngineCapacity(context.Background(), expected, 20)

	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestApplyCodexEngineCapacityRechecksProxyBeforeAccountLock(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*service.Proxy)
	}{
		{name: "same proxy"},
		{name: "changed host", mutate: func(p *service.Proxy) { p.Host = "replacement.example" }},
		{name: "changed credentials", mutate: func(p *service.Proxy) { p.Password = "replacement-password" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, m, queries := newCodexEngineCapacityMock(t)
			a := codexEngineCapacityAccount()
			proxyID := int64(11)
			a.ProxyID = &proxyID
			a.Proxy = &service.Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example", Port: 3128,
				Username: "user", Password: "password", Status: service.StatusActive}
			currentProxy := *a.Proxy
			if test.mutate != nil {
				test.mutate(&currentProxy)
			}
			m.ExpectBegin()
			// Ordered expectations enforce the proxy-before-account lock order.
			m.ExpectQuery(`SELECT protocol, host, port, .* FROM proxies .* FOR SHARE`).WithArgs(proxyID).
				WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
					AddRow(currentProxy.Protocol, currentProxy.Host, currentProxy.Port, currentProxy.Username, currentProxy.Password, currentProxy.Status))
			if test.mutate == nil {
				expectCapacityLock(t, m, a)
				expectCapacityWrite(t, m, a, 20, true, false)
				m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()
			} else {
				m.ExpectRollback()
			}

			changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, 20)

			require.NoError(t, err)
			require.Equal(t, test.mutate == nil, changed)
			if test.mutate != nil {
				require.Len(t, *queries, 1, "a proxy identity mismatch must stop before locking or writing the account")
			}
			require.NoError(t, m.ExpectationsWereMet())
		})
	}
}

func TestApplyCodexEngineCapacityManualRepeatPauseRevokesRecovery(t *testing.T) {
	repo, m, queries := newCodexEngineCapacityMock(t)
	ctx := context.Background()
	paused := codexEngineCapacityAccount()
	paused.Schedulable = false
	paused.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(paused, 0, true)
	current := *paused
	current.Extra = maps.Clone(paused.Extra)
	// PostgreSQL increments this revision even for an explicit false-to-false
	// scheduling write, so the previous automatic pause no longer owns recovery.
	current.SchedulingRevision++
	m.ExpectBegin()
	expectCapacityLock(t, m, &current)
	m.ExpectRollback()
	changed, err := repo.ApplyCodexEngineCapacity(ctx, paused, 30)
	require.NoError(t, err)
	require.False(t, changed, "a positive response started before the manual pause is discarded")

	// A fresh zero observation removes the revoked claim without acquiring a
	// new one, and must not issue another schedulable=false write.
	m.ExpectBegin()
	expectCapacityLock(t, m, &current)
	expectCapacityWrite(t, m, &current, 0, false, false)
	m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectCommit()
	changed, err = repo.ApplyCodexEngineCapacity(ctx, &current, 0)
	require.NoError(t, err)
	require.True(t, changed)
	current.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(&current, 0, false)

	// Capacity later recovers, while the administrator's pause remains in force.
	m.ExpectBegin()
	expectCapacityLock(t, m, &current)
	expectCapacityWrite(t, m, &current, 30, false, false)
	m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectCommit()
	changed, err = repo.ApplyCodexEngineCapacity(ctx, &current, 30)
	require.NoError(t, err)
	require.True(t, changed)
	for _, query := range *queries {
		require.NotContains(t, query, "UPDATE accounts SET schedulable=", "neither zero nor positive observations reclaim a manual pause")
	}
	require.NoError(t, m.ExpectationsWereMet())
}

func TestApplyCodexEngineCapacityFreshIdentityRetainsPauseOwnership(t *testing.T) {
	for _, identityChange := range []struct {
		name   string
		mutate func(*service.Account)
	}{
		{name: "url", mutate: func(a *service.Account) { a.Credentials["base_url"] = "https://replacement.example/v1" }},
		{name: "key", mutate: func(a *service.Account) { a.Credentials["api_key"] = "replacement-key" }},
		{name: "proxy", mutate: func(a *service.Account) {
			id := int64(11)
			a.ProxyID = &id
			a.Proxy = &service.Proxy{ID: id, Protocol: "http", Host: "new-proxy.example", Port: 3128, Status: service.StatusActive}
		}},
	} {
		for _, observation := range []struct {
			name       string
			limit      int
			manualStop bool
		}{
			{name: "positive resumes", limit: 20},
			{name: "zero preserves ownership", limit: 0},
			{name: "manual repeat pause blocks recovery", limit: 20, manualStop: true},
		} {
			t.Run(identityChange.name+"/"+observation.name, func(t *testing.T) {
				repo, m, _ := newCodexEngineCapacityMock(t)
				a := codexEngineCapacityAccount()
				a.Schedulable = false
				oldIdentity := service.CodexEngineCapacityIdentity(a)
				a.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(a, 0, true)
				identityChange.mutate(a)
				require.NotEqual(t, oldIdentity, service.CodexEngineCapacityIdentity(a))
				if observation.manualStop {
					a.SchedulingRevision++
				}
				m.ExpectBegin()
				if proxy := a.Proxy; proxy != nil {
					m.ExpectQuery(`SELECT protocol, host, port, .* FROM proxies .* FOR SHARE`).WithArgs(proxy.ID).
						WillReturnRows(sqlmock.NewRows([]string{"protocol", "host", "port", "username", "password", "status"}).
							AddRow(proxy.Protocol, proxy.Host, proxy.Port, proxy.Username, proxy.Password, proxy.Status))
				}
				expectCapacityLock(t, m, a)
				schedule := observation.limit > 0 && !observation.manualStop
				retainOwnership := observation.limit == 0 && !observation.manualStop
				expectCapacityWrite(t, m, a, observation.limit, schedule, retainOwnership)
				m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
				m.ExpectCommit()

				// This request was fetched using the new identity. Only its saved
				// pause metadata still belongs to the preceding Engine identity.
				changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, observation.limit)

				require.NoError(t, err)
				require.True(t, changed)
				require.NoError(t, m.ExpectationsWereMet())
			})
		}
	}
}

func TestApplyCodexEngineCapacitySkipsAccountThatExitedEngineMode(t *testing.T) {
	repo, m, queries := newCodexEngineCapacityMock(t)
	a := codexEngineCapacityAccount()
	a.Schedulable = false
	a.Extra[service.CodexEngineCapacitySyncExtraKey] = capacityMeta(a, 0, true)
	a.Extra[service.OpenAIAPIKeyModeExtraKey] = "generic"

	changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, 20)

	require.NoError(t, err)
	require.False(t, changed)
	require.Empty(t, *queries, "generic API keys no longer participate in capacity synchronization")
	require.NoError(t, m.ExpectationsWereMet())
}

func TestApplyCodexEngineCapacityInvalidMetadataCannotOwnRecovery(t *testing.T) {
	for _, test := range []struct {
		name            string
		previousLimit   int
		revision        int64
		pauseRevision   any
		missingIdentity bool
	}{
		{name: "malformed revision at zero", previousLimit: 0, revision: 0, pauseRevision: "invalid"},
		{name: "fractional revision at zero", previousLimit: 0, revision: 0, pauseRevision: 0.5},
		{name: "positive capacity with stale ownership", previousLimit: 30, revision: 5, pauseRevision: int64(5)},
		{name: "missing identity", previousLimit: 0, revision: 5, pauseRevision: int64(5), missingIdentity: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, m, queries := newCodexEngineCapacityMock(t)
			a := codexEngineCapacityAccount()
			a.Schedulable = false
			a.SchedulingRevision = test.revision
			metadata := map[string]any{
				"identity": service.CodexEngineCapacityIdentity(a), "limit": test.previousLimit, "pause_revision": test.pauseRevision,
			}
			if test.missingIdentity {
				delete(metadata, "identity")
			}
			a.Extra[service.CodexEngineCapacitySyncExtraKey] = metadata
			m.ExpectBegin()
			expectCapacityLock(t, m, a)
			expectCapacityWrite(t, m, a, 40, false, false)
			m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
			m.ExpectCommit()

			changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, 40)

			require.NoError(t, err)
			require.True(t, changed)
			for _, query := range *queries {
				require.NotContains(t, query, "UPDATE accounts SET schedulable=", "invalid or positive-capacity metadata cannot authorize recovery")
			}
			require.NoError(t, m.ExpectationsWereMet())
		})
	}
}

func TestApplyCodexEngineCapacityOutboxFailureRollsBackPause(t *testing.T) {
	repo, m, _ := newCodexEngineCapacityMock(t)
	a := codexEngineCapacityAccount()
	failure := errors.New("outbox unavailable")
	m.ExpectBegin()
	expectCapacityLock(t, m, a)
	expectCapacityWrite(t, m, a, 0, false, false)
	m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnError(failure)
	m.ExpectRollback()
	changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, 0)
	require.ErrorIs(t, err, failure)
	require.False(t, changed)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestApplyCodexEngineCapacityMetadataFailureRollsBackSchedulingWrite(t *testing.T) {
	repo, m, _ := newCodexEngineCapacityMock(t)
	a := codexEngineCapacityAccount()
	failure := errors.New("capacity metadata unavailable")
	m.ExpectBegin()
	expectCapacityLock(t, m, a)
	m.ExpectQuery(`UPDATE accounts SET schedulable=\$2,updated_at=NOW\(\) WHERE id=\$1 RETURNING scheduling_revision`).
		WithArgs(a.ID, false).WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(a.SchedulingRevision + 1))
	m.ExpectExec(`UPDATE accounts SET extra=jsonb_set`).WillReturnError(failure)
	m.ExpectRollback()

	changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, 0)

	require.ErrorIs(t, err, failure)
	require.False(t, changed)
	require.NoError(t, m.ExpectationsWereMet(), "metadata failure rolls back the earlier pause and revision write")
}

func TestApplyCodexEngineCapacityCommitFailureDoesNotPublish(t *testing.T) {
	repo, m, queries := newCodexEngineCapacityMock(t)
	cache := &codexEngineCapacityCacheSpy{}
	repo.schedulerCache = cache
	a := codexEngineCapacityAccount()
	failure := errors.New("commit failed")
	m.ExpectBegin()
	expectCapacityLock(t, m, a)
	expectCapacityWrite(t, m, a, 0, false, false)
	m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
	m.ExpectCommit().WillReturnError(failure)

	changed, err := repo.ApplyCodexEngineCapacity(context.Background(), a, 0)

	require.ErrorIs(t, err, failure)
	require.False(t, changed)
	require.Zero(t, cache.setAccounts)
	require.Len(t, *queries, 5, "a failed commit must not attempt a scheduler cache reload")
	require.NoError(t, m.ExpectationsWereMet())
}

func TestApplyCodexEngineCapacityUsesCallerTransaction(t *testing.T) {
	base, baseMock, queries := newCodexEngineCapacityMock(t)
	r, m, _ := newCodexEngineCapacityMock(t)
	a := codexEngineCapacityAccount()
	m.ExpectBegin()
	tx, err := r.client.Tx(context.Background())
	require.NoError(t, err)
	expectCapacityLock(t, m, a)
	expectCapacityWrite(t, m, a, 20, true, false)
	m.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnResult(sqlmock.NewResult(1, 1))
	changed, err := base.ApplyCodexEngineCapacity(dbent.NewTxContext(context.Background(), tx), a, 20)
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, *queries)
	require.NoError(t, baseMock.ExpectationsWereMet())
	m.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, m.ExpectationsWereMet())
}

func TestListCodexEngineCapacityCandidatesBoundedPaused(t *testing.T) {
	repo, m, queries := newCodexEngineCapacityMock(t)
	m.ExpectQuery(`SELECT .* FROM "accounts"`).WithArgs(int64(9), service.StatusActive, service.PlatformOpenAI, service.AccountTypeAPIKey, "codex_engine").
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "credentials", "extra", "status", "schedulable", "concurrency"}).
			AddRow(27, "openai", "apikey", `{"base_url":"https://engine.test","api_key":"key"}`, `{"openai_api_key_mode":"codex_engine"}`, "active", false, 30))
	accounts, err := repo.ListCodexEngineCapacityCandidates(context.Background(), 9, 10)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.False(t, accounts[0].Schedulable)
	require.Contains(t, (*queries)[0], `ORDER BY "accounts"."id" ASC LIMIT 10`)
	_, predicate, _ := strings.Cut((*queries)[0], "WHERE")
	require.NotContains(t, predicate, "schedulable")
	require.NoError(t, m.ExpectationsWereMet())
}
