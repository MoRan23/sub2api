package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const CodexTurnStateBatchStatusLimit = 200

func (s *CodexTurnStateService) statusNow() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *CodexTurnStateService) statusModelPolicy(ctx context.Context) ([]string, error) {
	if s == nil || s.modelPolicy == nil {
		return nil, errors.New("turn_state_model_policy_unavailable")
	}
	models, revision, err := s.modelPolicy.CodexTurnStateModelPolicy(ctx)
	if err != nil {
		return nil, err
	}
	if revision == "" {
		return nil, errors.New("turn_state_model_policy_unavailable")
	}
	return append([]string{}, models...), nil
}

func codexTurnStateStatusUnavailable(cause error) error {
	return infraerrors.New(http.StatusServiceUnavailable, "CODEX_TURN_STATE_UNAVAILABLE", "turn-state status is unavailable").WithCause(cause)
}

// GetStatuses performs bounded read-only lookups for an account-list page. It
// neither starts business leases nor decrypts state nor schedules collection.
func (s *CodexTurnStateService) GetStatuses(ctx context.Context, accountIDs []int64) (*CodexTurnStateBatchStatus, error) {
	ids := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]bool, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			return nil, infraerrors.BadRequest("CODEX_TURN_STATE_INVALID_ACCOUNT_IDS", "account IDs must be positive integers")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
			if len(ids) > CodexTurnStateBatchStatusLimit {
				return nil, infraerrors.BadRequest("CODEX_TURN_STATE_INVALID_ACCOUNT_IDS", "at most 200 account IDs may be requested")
			}
		}
	}
	if s == nil || s.accounts == nil || s.repo == nil {
		return nil, codexTurnStateStatusUnavailable(nil)
	}
	models, err := s.statusModelPolicy(ctx)
	if err != nil {
		return nil, codexTurnStateStatusUnavailable(err)
	}
	result := &CodexTurnStateBatchStatus{Items: make(map[string]*CodexTurnStateStatus), Models: models}
	if len(ids) == 0 {
		return result, nil
	}
	accounts, err := s.accounts.GetByIDs(ctx, ids)
	if err != nil {
		return nil, codexTurnStateStatusUnavailable(err)
	}
	loaded := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			loaded[account.ID] = account
		}
	}
	parentIDs := make([]int64, 0)
	parentsSeen := make(map[int64]bool)
	for _, id := range ids {
		account := loaded[id]
		if account == nil || !account.IsShadow() {
			continue
		}
		parentID := *account.ParentAccountID
		if parentID > 0 && loaded[parentID] == nil && !parentsSeen[parentID] {
			parentsSeen[parentID] = true
			parentIDs = append(parentIDs, parentID)
		}
	}
	if len(parentIDs) > 0 {
		parents, readErr := s.accounts.GetByIDs(ctx, parentIDs)
		if readErr != nil {
			return nil, codexTurnStateStatusUnavailable(readErr)
		}
		for _, parent := range parents {
			if parent != nil {
				loaded[parent.ID] = parent
			}
		}
	}
	owners := make(map[int64]*Account, len(ids))
	ownerIDs := make([]int64, 0, len(ids))
	ownerSeen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		account := loaded[id]
		if account == nil {
			continue
		}
		owner := account
		if account.IsShadow() {
			owner, err = credentialAccountFromParent(account, loaded[*account.ParentAccountID])
			if err != nil {
				// An unresolved owner is unavailable, never a disabled account.
				continue
			}
		}
		owners[id] = owner
		if !ownerSeen[owner.ID] {
			ownerSeen[owner.ID] = true
			ownerIDs = append(ownerIDs, owner.ID)
		}
	}
	recordsByOwner := make(map[int64][]CodexTurnStateRecord, len(ownerIDs))
	if len(ownerIDs) > 0 {
		records, readErr := s.repo.ListByAccounts(ctx, ownerIDs)
		if readErr != nil {
			return nil, codexTurnStateStatusUnavailable(readErr)
		}
		for _, record := range records {
			recordsByOwner[record.OwnerAccountID] = append(recordsByOwner[record.OwnerAccountID], record)
		}
	}
	now := s.statusNow()
	observationEnabled, observations := globalCodexTurnStateSummaryStore.snapshot(ownerIDs)
	for _, id := range ids {
		if owner := owners[id]; owner != nil {
			item := projectCodexTurnStateStatus(id, owner, recordsByOwner[owner.ID], models, nil, now)
			attachCodexTurnStateObservations(item, observationEnabled, observations[owner.ID])
			result.Items[strconv.FormatInt(id, 10)] = item
		}
	}
	return result, nil
}

func attachCodexTurnStateObservations(status *CodexTurnStateStatus, enabled bool, observations []CodexTurnStateModelObservation) {
	status.ObservationEnabled = enabled
	status.ObservationScope = "instance"
	status.Observations = append([]CodexTurnStateModelObservation{}, observations...)
}

func projectCodexTurnStateStatus(accountID int64, owner *Account, records []CodexTurnStateRecord, allowedModels []string, policyErr error, now time.Time) *CodexTurnStateStatus {
	cfg := CodexTurnStateConfigForAccount(owner)
	proxyIDs := CodexTurnStateCollectorProxyIDs(cfg)
	result := &CodexTurnStateStatus{AccountID: accountID, OwnerAccountID: owner.ID, Inherited: owner.ID != accountID, Enabled: cfg.Enabled && codexTurnStateEligible(owner), AccountType: cfg.AccountType, ResolvedAccountType: CodexTurnStateAccountTypeForAccount(owner), CollectorProxyID: codexStateProxyIDPtr(codexTurnStateSelectedProxy(proxyIDs, 0)), CollectorProxyIDs: append([]int64{}, proxyIDs...), Models: []CodexTurnStateModelStatus{}}
	if result.ResolvedAccountType == "personal" {
		result.ExpectedLength = 292
	} else if result.ResolvedAccountType == "team_business" {
		result.ExpectedLength = 332
	}
	if !result.Enabled {
		result.Reason = "disabled"
	} else if result.ExpectedLength == 0 {
		result.Reason = "account_type_unknown"
	} else if len(proxyIDs) == 0 {
		result.Reason = "business_learning_only"
	}
	allowed := make(map[string]bool, len(allowedModels))
	for _, model := range allowedModels {
		allowed[model] = true
	}
	ownerPaused := false
	var ownerRetry time.Time
	for _, record := range records {
		if record.Generation != CodexTurnStateGenerationForAccount(owner) {
			continue
		}
		ownerPaused = ownerPaused || record.CollectorPaused
		if !record.LastCollectedAt.IsZero() && record.NextCollectAt.After(ownerRetry) {
			ownerRetry = record.NextCollectAt
		}
	}
	for _, record := range records {
		if record.Generation != CodexTurnStateGenerationForAccount(owner) {
			continue
		}
		item := CodexTurnStateModelStatus{Model: record.Model, State: "missing", Shape: record.Shape, Source: record.Source, TokenLength: record.TokenLength, CipherBlocks: record.CipherBlocks, CollectorPaused: record.CollectorPaused, LastError: record.LastError, RefreshReason: record.RefreshReason}
		item.CollectorProxyID = codexStateProxyIDPtr(codexTurnStateSelectedProxy(proxyIDs, record.CollectorProxyID))
		item.LastCollectorProxyID = codexStateProxyIDPtr(record.LastCollectorProxyID)
		item.CollectorExtendedCount = record.CollectorExtendedCount
		if record.EncryptedToken != "" {
			item.State = "expired"
			if record.ExpiresAt.After(now) {
				item.State = "ready"
				item.RemainingSeconds = int64(record.ExpiresAt.Sub(now) / time.Second)
			}
		}
		if record.CollectorPaused {
			item.State = "paused"
		}
		item.ModelAllowed = allowed[record.Model] && policyErr == nil
		if !item.ModelAllowed {
			item.State = "model_excluded"
			if policyErr != nil {
				item.State = "model_policy_unavailable"
			}
		}
		item.ExpiresAt = codexStateTimePtr(record.ExpiresAt)
		item.LastBusinessAt = codexStateTimePtr(record.LastBusinessAt)
		item.LastCollectedAt = codexStateTimePtr(record.LastCollectedAt)
		item.NextCollectAt = codexStateTimePtr(record.NextCollectAt)
		item.CacheAvailable = result.Enabled && item.ModelAllowed && result.ExpectedLength > 0 &&
			record.EncryptedToken != "" && record.Shape == CodexTurnStateShapeTarget &&
			record.TokenLength == result.ExpectedLength && record.CipherBlocks == map[int]int{292: 10, 332: 12}[result.ExpectedLength] &&
			!record.IssuedAt.IsZero() && !record.IssuedAt.After(now.Add(30*time.Second)) && record.ExpiresAt.After(now)
		item.CollectionStatus, item.CollectionReason = projectCodexStateCollection(result, owner, record, now, ownerPaused, ownerRetry)
		if !item.ModelAllowed {
			item.CollectionStatus, item.CollectionReason = "blocked", item.State
		}
		if ownerRetry.After(record.NextCollectAt) && item.CollectionStatus == "backoff" {
			item.NextCollectAt = codexStateTimePtr(ownerRetry)
		}
		result.Models = append(result.Models, item)
	}
	return result
}

func projectCodexStateCollection(status *CodexTurnStateStatus, owner *Account, record CodexTurnStateRecord, now time.Time, ownerPaused bool, ownerRetry time.Time) (string, string) {
	if !status.Enabled {
		return "blocked", "disabled"
	}
	if status.ExpectedLength == 0 {
		return "blocked", "account_type_unknown"
	}
	if status.CollectorProxyID == nil || *status.CollectorProxyID <= 0 {
		return "blocked", "collector_proxy_not_configured"
	}
	if owner.Status != StatusActive {
		return "blocked", "account_inactive"
	}
	if !owner.Schedulable {
		return "blocked", "account_scheduling_disabled"
	}
	if owner.ExpiresAt != nil && !owner.ExpiresAt.After(now) {
		return "blocked", "account_expired"
	}
	if ownerPaused {
		return "paused", "collector_auth_rejected"
	}
	if record.LastBusinessAt.Before(now.Add(-CodexTurnStateActiveWindow)) {
		return "idle", "idle"
	}
	if record.CollectionStatus == "collecting" && record.LastCollectedAt.Add(CodexTurnStateCollectTimeout).After(now) {
		return "collecting", "collecting"
	}
	if codexTurnStateWaitsForProxyCacheExpiry(&record, now) {
		if record.NextCollectAt.After(now) || ownerRetry.After(now) {
			reason := record.LastError
			if reason == "" {
				reason = "account_cooldown"
			}
			return "backoff", reason
		}
		return "idle", "collector_proxy_changed"
	}
	if record.DemandReason == "" {
		if record.EncryptedToken == "" {
			return "idle", "waiting_business_response"
		}
		if record.ExpiresAt.After(now.Add(CodexTurnStateRefreshAhead)) {
			return "idle", ""
		}
	}
	if record.NextCollectAt.After(now) || ownerRetry.After(now) {
		reason := record.LastError
		if reason == "" {
			reason = "account_cooldown"
		}
		return "backoff", reason
	}
	if record.CollectionReason == "collector_proxy_unavailable" {
		return "blocked", record.CollectionReason
	}
	return "pending", "queued"
}
