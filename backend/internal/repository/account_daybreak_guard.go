package repository

import (
	"context"
	"sort"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func lockDaybreakEvidenceOwners(ctx context.Context, client *dbent.Client, ids []int64) error {
	owners := map[int64]bool{}
	hasIntent := false
	for _, id := range ids {
		hasIntent = hasIntent || service.HasOpenAIDaybreakSettings(service.AccountConfigurationIntentFromContext(ctx, id).Extra)
		if evidence, ok := service.AccountDaybreakEvidenceFromContext(ctx, id); ok && evidence.CredentialOwnerID > 0 {
			owners[evidence.CredentialOwnerID] = true
		}
	}
	if !hasIntent && len(owners) == 0 {
		return nil
	}
	// Include every target, even targets already enabled (and therefore without
	// new evidence), so simultaneous enable/disable batches share one lock order.
	for _, id := range ids {
		owners[id] = true
	}
	ordered := make([]int64, 0, len(owners))
	for id := range owners {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		if _, err := lockAccountConfiguration(ctx, client, id); err != nil {
			return err
		}
	}
	return nil
}

// All capability I/O completed before this transaction. Only the exact checked
// grant may enable a tier; a concurrent reauthorization makes the save stale.
func validateDaybreakPatchLocked(ctx context.Context, client *dbent.Client, current *service.Account, target *service.Account, patch map[string]any) error {
	if !service.HasOpenAIDaybreakSettings(patch) {
		return nil
	}
	normalized, err := service.NormalizeOpenAIDaybreakSettings(target, patch)
	if err != nil {
		return err
	}
	enabling := false
	for _, key := range []string{service.OpenAIDaybreakBlueEnabledKey, service.OpenAIDaybreakRedEnabledKey} {
		requested, explicit := normalized[key].(bool)
		existing, _ := current.Extra[key].(bool)
		if explicit && requested && !existing {
			enabling = true
		}
	}
	if !enabling {
		return nil
	}
	stale := func() error {
		return infraerrors.Conflict("DAYBREAK_CAPABILITY_STALE", "upstream credentials, access mode or Daybreak settings changed; save credentials, reload capabilities and retry")
	}
	evidence, ok := service.AccountDaybreakEvidenceFromContext(ctx, current.ID)
	if !ok {
		return stale()
	}
	if current.IsOpenAIApiKey() || target.IsOpenAIApiKey() {
		// Both rows must match: a stale full snapshot must not reinstate the old
		// key after a concurrent edit. A combined credential edit and enable must
		// save its new credentials and obtain fresh evidence first.
		if evidence.CredentialOwnerID != current.ID || evidence.CredentialIdentity == "" ||
			service.OpenAIDaybreakAPIKeyCredentialIdentity(current) != evidence.CredentialIdentity ||
			service.OpenAIDaybreakAPIKeyCredentialIdentity(target) != evidence.CredentialIdentity {
			return stale()
		}
		return nil
	}
	if evidence.AuthorizationGeneration == "" || evidence.CredentialOS == "" {
		return stale()
	}
	owner := current.ID
	if current.ParentAccountID != nil {
		owner = *current.ParentAccountID
	}
	targetOwner := target.ID
	if target.ParentAccountID != nil {
		targetOwner = *target.ParentAccountID
	}
	if owner != evidence.CredentialOwnerID || targetOwner != owner || !current.IsOpenAIOAuth() || !target.IsOpenAIOAuth() {
		return stale()
	}
	slots, err := readOpenAIOAuthOSCredentials(ctx, client, owner, "")
	if err != nil {
		return err
	}
	if len(slots) != 1 || slots[0].AuthorizationGeneration != evidence.AuthorizationGeneration || slots[0].OSFamily != evidence.CredentialOS {
		return stale()
	}
	return nil
}
