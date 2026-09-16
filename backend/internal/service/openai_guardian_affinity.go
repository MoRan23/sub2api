package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	codexAutoReviewModel      = "codex-auto-review"
	openAISubagentHeader      = "x-openai-subagent"
	codexParentThreadIDHeader = "x-codex-parent-thread-id"
	codexTurnMetadataHeader   = "x-codex-turn-metadata"
)

type openAIGuardianParentAffinityContextKey struct{}

type openAIGuardianParentAffinity struct {
	currentSessionHash string
	legacySessionHash  string
}

// WithOpenAIGuardianParentAffinity records a Codex review request's parent or
// classifier source thread as a routing hint. The hint is resolved against the
// current group's sticky-session namespace later; client headers never carry an account ID.
func WithOpenAIGuardianParentAffinity(ctx context.Context, c *gin.Context, body []byte, requestedModel string) context.Context {
	if ctx == nil || c == nil {
		return ctx
	}

	headerMetadata := c.GetHeader(codexTurnMetadataHeader)
	payload := openAIRequestPayloadView(body)
	bodyMetadata := payload.Get("client_metadata.x-codex-turn-metadata").String()
	cacheKey := strings.TrimSpace(payload.Get("prompt_cache_key").String())
	if codexHasGuardianClassifierLineage(headerMetadata, bodyMetadata, cacheKey) {
		sourceID := codexGuardianClassifierSourceThreadID(headerMetadata, bodyMetadata, cacheKey)
		if sourceID == "" || unambiguousOpenAICodexSubagent(
			c.GetHeader(openAISubagentHeader),
			payload.Get("client_metadata.x-openai-subagent").String(),
			codexSubagentKindFromMetadata(headerMetadata),
			codexSubagentKindFromMetadata(bodyMetadata),
		) != "guardian" {
			return ctx
		}
		return withOpenAIGuardianSourceAffinity(ctx, sourceID)
	}
	if !strings.EqualFold(strings.TrimSpace(requestedModel), codexAutoReviewModel) {
		return ctx
	}
	if !hasUnambiguousOpenAICodexReviewSubagent(
		c.GetHeader(openAISubagentHeader),
		codexSubagentKindFromMetadata(headerMetadata),
		codexSubagentKindFromMetadata(bodyMetadata),
	) {
		return ctx
	}

	parentID := ""
	for _, candidate := range []string{
		strings.TrimSpace(c.GetHeader(codexParentThreadIDHeader)),
		codexParentThreadIDFromMetadata(headerMetadata),
		codexParentThreadIDFromMetadata(bodyMetadata),
	} {
		if candidate == "" {
			continue
		}
		if parentID != "" && parentID != candidate {
			return ctx
		}
		parentID = candidate
	}
	if parentID == "" {
		return ctx
	}

	return withOpenAIGuardianSourceAffinity(ctx, parentID)
}

func withOpenAIGuardianSourceAffinity(ctx context.Context, sourceID string) context.Context {
	currentHash, legacyHash := deriveOpenAISessionHashes(sourceID)
	if currentHash == "" {
		return ctx
	}
	return context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{
		currentSessionHash: currentHash,
		legacySessionHash:  legacyHash,
	})
}

func codexHasGuardianClassifierLineage(headerMetadata, bodyMetadata, cacheKey string) bool {
	if strings.HasPrefix(cacheKey, "guardian-v2:") {
		return true
	}
	for _, raw := range []string{headerMetadata, bodyMetadata} {
		if gjson.Get(raw, "guardian_classifier_source_thread_id").Exists() ||
			gjson.Get(raw, "thread_source").String() == "guardian_classifier" {
			return true
		}
	}
	return false
}

// Classifier models are configurable. Their explicit source lineage, rather
// than a model name or cache key alone, identifies the account routing hint.
func codexGuardianClassifierSourceThreadID(headerMetadata, bodyMetadata, cacheKey string) string {
	sourceID := ""
	classifierSource := false
	for _, raw := range []string{headerMetadata, bodyMetadata} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if !gjson.Valid(raw) || !gjson.Parse(raw).IsObject() {
			return ""
		}
		if source := gjson.Get(raw, "thread_source"); source.Exists() {
			if source.Type != gjson.String || source.String() != "guardian_classifier" {
				return ""
			}
			classifierSource = true
		}
		if source := gjson.Get(raw, "guardian_classifier_source_thread_id"); source.Exists() {
			candidate := codexGuardianClassifierUUID(source.String())
			if source.Type != gjson.String || candidate == "" || (sourceID != "" && sourceID != candidate) {
				return ""
			}
			sourceID = candidate
		}
	}
	if !classifierSource || sourceID == "" {
		return ""
	}
	if cacheKey != "" {
		seed, ok := strings.CutPrefix(cacheKey, "guardian-v2:")
		if !ok || codexGuardianClassifierUUID(seed) != sourceID {
			return ""
		}
	}
	return sourceID
}

func codexGuardianClassifierUUID(raw string) string {
	raw = strings.TrimSpace(raw)
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || !strings.EqualFold(raw, id.String()) {
		return ""
	}
	return id.String()
}

func codexParentThreadIDFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "parent_thread_id").String())
}

func codexSubagentKindFromMetadata(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !gjson.Valid(raw) {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "subagent_kind").String())
}

func hasUnambiguousOpenAICodexReviewSubagent(candidates ...string) bool {
	subagent := unambiguousOpenAICodexSubagent(candidates...)
	return subagent == "guardian" || subagent == "review"
}

func unambiguousOpenAICodexSubagent(candidates ...string) string {
	subagent := ""
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if subagent != "" && subagent != candidate {
			return ""
		}
		subagent = candidate
	}
	return subagent
}

func openAIGuardianParentAffinityFromContext(ctx context.Context) (openAIGuardianParentAffinity, bool) {
	if ctx == nil {
		return openAIGuardianParentAffinity{}, false
	}
	affinity, ok := ctx.Value(openAIGuardianParentAffinityContextKey{}).(openAIGuardianParentAffinity)
	return affinity, ok && affinity.currentSessionHash != ""
}

func preserveOpenAIGuardianParentBinding(ctx context.Context, sessionHash string) bool {
	affinity, ok := openAIGuardianParentAffinityFromContext(ctx)
	if !ok {
		return false
	}
	sessionHash = strings.TrimSpace(sessionHash)
	return sessionHash != "" && (sessionHash == affinity.currentSessionHash || sessionHash == affinity.legacySessionHash)
}

func (s *OpenAIGatewayService) resolveOpenAIGuardianParentAccountID(ctx context.Context, groupID *int64) int64 {
	if s == nil || s.cache == nil {
		return 0
	}
	affinity, ok := openAIGuardianParentAffinityFromContext(ctx)
	if !ok {
		return 0
	}
	lookupCtx := withOpenAILegacySessionHash(ctx, affinity.legacySessionHash)
	accountID, err := s.getStickySessionAccountID(lookupCtx, groupID, affinity.currentSessionHash)
	if err != nil || accountID <= 0 {
		return 0
	}
	return accountID
}
