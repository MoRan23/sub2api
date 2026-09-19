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
	for _, id := range ids {
		if owner := owners[id]; owner != nil {
			result.Items[strconv.FormatInt(id, 10)] = projectCodexTurnStateStatus(id, owner, recordsByOwner[owner.ID], models, nil, now)
		}
	}
	return result, nil
}

func projectCodexTurnStateStatus(accountID int64, owner *Account, records []CodexTurnStateRecord, allowedModels []string, policyErr error, now time.Time) *CodexTurnStateStatus {
	cfg := CodexTurnStateConfigForAccount(owner)
	result := &CodexTurnStateStatus{AccountID: accountID, OwnerAccountID: owner.ID, Inherited: owner.ID != accountID, Enabled: cfg.Enabled && codexTurnStateEligible(owner), AccountType: cfg.AccountType, ResolvedAccountType: CodexTurnStateAccountTypeForAccount(owner), CollectorProxyID: cfg.CollectorProxyID, Models: []CodexTurnStateModelStatus{}}
	if result.ResolvedAccountType == "personal" {
		result.ExpectedLength = 292
	} else if result.ResolvedAccountType == "team_business" {
		result.ExpectedLength = 332
	}
	if !result.Enabled {
		result.Reason = "disabled"
	} else if result.ExpectedLength == 0 {
		result.Reason = "account_type_unknown"
	} else if cfg.CollectorProxyID == nil {
		result.Reason = "business_learning_only"
	}
	allowed := make(map[string]bool, len(allowedModels))
	for _, model := range allowedModels {
		allowed[model] = true
	}
	for _, record := range records {
		if record.Generation != CodexTurnStateGenerationForAccount(owner) {
			continue
		}
		item := CodexTurnStateModelStatus{Model: record.Model, State: "missing", Shape: record.Shape, Source: record.Source, TokenLength: record.TokenLength, CipherBlocks: record.CipherBlocks, CollectorPaused: record.CollectorPaused, LastError: record.LastError, RefreshReason: record.RefreshReason}
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
		result.Models = append(result.Models, item)
	}
	return result
}
