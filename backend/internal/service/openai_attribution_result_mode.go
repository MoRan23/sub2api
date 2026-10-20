package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const OpenAIAttributionReplaceWhitelistEnabledExtraKey = "openai_attribution_replace_whitelist_enabled"

// OpenAIAttributionReplacesWhitelist preserves the legacy behavior unless the
// administrator explicitly selected the scheduling-only mode.
func (a *Account) OpenAIAttributionReplacesWhitelist() bool {
	if a == nil {
		return true
	}
	enabled, ok := a.Extra[OpenAIAttributionReplaceWhitelistEnabledExtraKey].(bool)
	return !ok || enabled
}

func HasOpenAIAttributionResultMode(extra map[string]any) bool {
	_, ok := extra[OpenAIAttributionReplaceWhitelistEnabledExtraKey]
	return ok
}

func ValidateOpenAIAttributionResultMode(platform, accountType string, extra map[string]any) error {
	value, exists := extra[OpenAIAttributionReplaceWhitelistEnabledExtraKey]
	if !exists {
		return nil
	}
	if _, ok := value.(bool); !ok {
		return infraerrors.BadRequest("OPENAI_ATTRIBUTION_RESULT_MODE_INVALID", "openai_attribution_replace_whitelist_enabled must be a boolean")
	}
	if platform != PlatformOpenAI || (accountType != AccountTypeOAuth && accountType != AccountTypeAPIKey) {
		return infraerrors.BadRequest("OPENAI_ATTRIBUTION_RESULT_MODE_UNSUPPORTED", "attribution result modes are supported only by OpenAI OAuth and API key accounts")
	}
	return nil
}

type accountSchedulingIntentKey struct{}
type accountSchedulingIntent struct {
	id    int64
	value bool
}

// WithAccountSchedulingIntent identifies an intentional scheduling write rather
// than an older full-account snapshot. Dedicated SetSchedulable/BulkUpdate calls
// already express this intent through their typed arguments.
func WithAccountSchedulingIntent(ctx context.Context, id int64, value bool) context.Context {
	return context.WithValue(ctx, accountSchedulingIntentKey{}, accountSchedulingIntent{id: id, value: value})
}

func AccountSchedulingIntentFromContext(ctx context.Context, id int64) (bool, bool) {
	intent, ok := ctx.Value(accountSchedulingIntentKey{}).(accountSchedulingIntent)
	return intent.value, ok && intent.id == id
}

// AccountSchedulingSnapshotRefresher propagates committed scheduling changes
// immediately in addition to the durable scheduler outbox.
type AccountSchedulingSnapshotRefresher interface {
	RefreshAccountSchedulingSnapshot(context.Context, int64)
}
