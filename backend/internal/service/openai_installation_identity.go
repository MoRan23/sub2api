package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// codexInstallationIDKey is the header / client_metadata key that carries the
// Codex installation identifier on both the inbound (client) and outbound
// (upstream) sides.
const codexInstallationIDKey = "x-codex-installation-id"

const codexTurnMetadataInstallationIDKey = "installation_id"

// installationPinContextKey caches the per-request installation resolution in
// the gin context so body and header stages emit the exact same value.
const installationPinContextKey = "openai_resolved_installation_id"

// openAIInstallationIDCASRepository is implemented by the production account
// repository without widening AccountRepository for every gateway test double.
// It repairs a missing/invalid UUID with compare-and-swap semantics so concurrent
// requests converge on one persisted value.
type openAIInstallationIDCASRepository interface {
	EnsureOpenAIInstallationID(ctx context.Context, accountID int64, expectedID, generatedID string) (string, error)
}

// installationIDResolution is the resolved outbound installation identity for a
// single request.
type installationIDResolution struct {
	// Enabled reports whether pinning is active for this account. When false the
	// caller must fall back to the legacy passthrough behavior.
	Enabled bool
	// OutboundID is the value to emit upstream (empty when !Enabled).
	OutboundID string
	// ClientID is what the inbound client reported (header/body), kept for the
	// observation panel so operators can see the value being suppressed.
	ClientID string
	// BodyModified and HeadersModified report mutations made by the shared
	// outbound boundary. They let callers preserve their existing serialization
	// and observation behavior without duplicating rewrite decisions.
	BodyModified    bool
	HeadersModified bool
}

type installationIDRequestCache struct {
	SourceAccountID int64
	Resolution      installationIDResolution
}

// shouldRewriteOpenAIInstallationID freezes the transport boundary for pinned
// installation identity. OAuth passthrough uses the same account-owned value
// as transformed Responses traffic.
func shouldRewriteOpenAIInstallationID(account *Account, _ bool) bool {
	return account != nil && account.IsOpenAIOAuth()
}

// resolveOutboundInstallationID computes the outbound installation_id for an
// account given whatever the inbound client reported.
//
// The client-reported value is retained only for the observation panel. It never
// participates in selecting the fixed outbound UUID.
func resolveOutboundInstallationID(account *Account, clientReportedID string) installationIDResolution {
	res := installationIDResolution{ClientID: strings.TrimSpace(clientReportedID)}
	if account == nil || !account.IsOpenAIInstallationPinEnabled() {
		return res
	}
	res.Enabled = true
	res.OutboundID = normalizeCodexInstallationID(account.GetPinnedOpenAIInstallationID())
	return res
}

// normalizeCodexInstallationID accepts only canonicalizable UUID v4 values.
func normalizeCodexInstallationID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.Version() != 4 {
		return ""
	}
	return parsed.String()
}

// resolveOpenAIInstallationIdentityAccount resolves a request source account
// to the account whose credentials and pinned installation identity are used
// upstream. Shadow accounts never own an installation identity of their own.
func resolveOpenAIInstallationIdentityAccount(ctx context.Context, repo AccountRepository, account *Account) (*Account, error) {
	if account == nil || !account.IsShadow() {
		return account, nil
	}
	return resolveCredentialAccount(ctx, repo, account)
}

// resolveInstallationIDForRequestWithRepo is the shared resolver used by the
// gateway and auxiliary OAuth requests. It deliberately accepts the
// repository explicitly so background probes and admin tests use the same CAS
// persistence path as normal forwarding.
func resolveInstallationIDForRequestWithRepo(
	ctx context.Context,
	c *gin.Context,
	repo AccountRepository,
	account *Account,
	clientReportedID string,
) (installationIDResolution, error) {
	if c != nil && account != nil {
		if cached, ok := c.Get(installationPinContextKey); ok {
			if requestCache, ok := cached.(installationIDRequestCache); ok && requestCache.SourceAccountID == account.ID {
				return requestCache.Resolution, nil
			}
		}
	}

	identityAccount, err := resolveOpenAIInstallationIdentityAccount(ctx, repo, account)
	if err != nil {
		return installationIDResolution{}, err
	}
	res := resolveOutboundInstallationID(identityAccount, clientReportedID)
	if res.Enabled && res.OutboundID == "" {
		generated, ensureErr := ensureOpenAIInstallationID(ctx, repo, identityAccount)
		if ensureErr != nil {
			return installationIDResolution{}, ensureErr
		}
		res.OutboundID = generated
	}
	if c != nil {
		sourceAccountID := int64(0)
		if account != nil {
			sourceAccountID = account.ID
		}
		c.Set(installationPinContextKey, installationIDRequestCache{
			SourceAccountID: sourceAccountID,
			Resolution:      res,
		})
	}
	return res, nil
}

// resolveInstallationIDForRequest resolves the outbound installation identity
// once per request and caches it in the gin context, so every stage (body and
// header, HTTP and WS) emits the same value. Shadow accounts inherit their
// credential parent's identity.
func (s *OpenAIGatewayService) resolveInstallationIDForRequest(ctx context.Context, c *gin.Context, account *Account, clientReportedID string) (installationIDResolution, error) {
	var repo AccountRepository
	if s != nil {
		repo = s.accountRepo
	}
	return resolveInstallationIDForRequestWithRepo(ctx, c, repo, account, clientReportedID)
}

func (s *OpenAIGatewayService) ensureOpenAIInstallationID(ctx context.Context, account *Account) (string, error) {
	var repo AccountRepository
	if s != nil {
		repo = s.accountRepo
	}
	return ensureOpenAIInstallationID(ctx, repo, account)
}

func ensureOpenAIInstallationID(ctx context.Context, repo AccountRepository, account *Account) (string, error) {
	if account == nil || account.ID <= 0 || !account.IsOpenAIOAuth() || account.IsShadow() {
		return "", fmt.Errorf("openai installation_id repair requires an OAuth parent account")
	}
	expected := strings.TrimSpace(account.GetPinnedOpenAIInstallationID())
	generated := uuid.NewString()
	if repo == nil {
		return generated, nil
	}
	if casRepo, ok := repo.(openAIInstallationIDCASRepository); ok {
		resolved, err := callEnsureOpenAIInstallationID(casRepo, ctx, account.ID, expected, generated)
		if err != nil {
			return "", err
		}
		normalized := normalizeCodexInstallationID(resolved)
		if normalized == "" {
			return "", fmt.Errorf("account %d has invalid persisted openai installation_id", account.ID)
		}
		return normalized, nil
	}
	// Narrow test doubles may omit persistence. Production repositories implement
	// the CAS contract; unsupported doubles still receive a request-local UUID
	// without falling back to the client-reported value.
	return generated, nil
}

func callEnsureOpenAIInstallationID(repo openAIInstallationIDCASRepository, ctx context.Context, accountID int64, expectedID, generatedID string) (resolved string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			resolved = generatedID
			err = nil
		}
	}()
	return repo.EnsureOpenAIInstallationID(ctx, accountID, expectedID, generatedID)
}

// extractClientInstallationID reads the installation_id the inbound client
// reported. Direct header/body fields take precedence over values embedded in
// x-codex-turn-metadata. Opaque turn metadata is ignored without mutation.
func extractClientInstallationID(c *gin.Context, reqBody map[string]any) string {
	if c != nil && c.Request != nil {
		if v := strings.TrimSpace(c.Request.Header.Get(codexInstallationIDKey)); v != "" {
			return v
		}
	}
	if reqBody != nil {
		if direct, _ := clientInstallationMetadataValues(reqBody); direct != "" {
			return direct
		}
	}
	if c != nil && c.Request != nil {
		if v := extractInstallationIDFromTurnMetadata(c.Request.Header.Get(openAIWSTurnMetadataHeader)); v != "" {
			return v
		}
	}
	if reqBody != nil {
		if _, turnMetadata := clientInstallationMetadataValues(reqBody); turnMetadata != "" {
			return extractInstallationIDFromTurnMetadata(turnMetadata)
		}
	}
	return ""
}

func extractClientInstallationIDFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(codexInstallationIDKey)); value != "" {
		return value
	}
	return extractInstallationIDFromTurnMetadata(headers.Get(openAIWSTurnMetadataHeader))
}

func copyOpenAIInstallationIDHeadersFromContext(c *gin.Context, headers http.Header) {
	if c == nil || c.Request == nil || headers == nil {
		return
	}
	if headers.Get(codexInstallationIDKey) == "" {
		if value := strings.TrimSpace(c.Request.Header.Get(codexInstallationIDKey)); value != "" {
			headers.Set(codexInstallationIDKey, value)
		}
	}
	if len(headers.Values(openAIWSTurnMetadataHeader)) == 0 {
		for _, value := range c.Request.Header.Values(openAIWSTurnMetadataHeader) {
			headers.Add(openAIWSTurnMetadataHeader, value)
		}
	}
}

// applyOpenAIInstallationIDForOutbound is the common body/header boundary for
// OpenAI OAuth requests outside the main gateway forwarder. It resolves the
// source account (including shadows), repairs missing pinned UUIDs through CAS,
// and applies the compact or regular Responses wire rules consistently.
func applyOpenAIInstallationIDForOutbound(
	ctx context.Context,
	c *gin.Context,
	repo AccountRepository,
	account *Account,
	body map[string]any,
	headers http.Header,
	compact bool,
	passthrough bool,
) (installationIDResolution, error) {
	if !shouldRewriteOpenAIInstallationID(account, passthrough) {
		return installationIDResolution{}, nil
	}
	identityAccount, err := resolveOpenAIInstallationIdentityAccount(ctx, repo, account)
	if err != nil {
		return installationIDResolution{}, err
	}
	copyOpenAIInstallationIDHeadersFromContext(c, headers)
	clientReportedID := extractClientInstallationID(c, body)
	if clientReportedID == "" {
		clientReportedID = extractClientInstallationIDFromHeaders(headers)
	}
	resolution, err := resolveInstallationIDForRequestWithRepo(ctx, c, repo, account, clientReportedID)
	if err != nil {
		return installationIDResolution{}, err
	}

	bodyModified := false
	if compact {
		bodyModified = stripOpenAICompactClientMetadata(body)
	} else if resolution.Enabled && resolution.OutboundID != "" {
		bodyModified = rewriteOpenAIInstallationIDInBody(body, resolution.OutboundID)
	} else if body != nil {
		bodyModified = applyCodexClientMetadata(body, identityAccount)
	}
	headerModified := false
	if resolution.Enabled && resolution.OutboundID != "" {
		headerModified = rewriteOpenAIInstallationIDHeaders(headers, resolution.OutboundID)
	}
	resolution.BodyModified = bodyModified
	resolution.HeadersModified = headerModified
	return resolution, nil
}

func clientInstallationMetadataValues(reqBody map[string]any) (direct string, turnMetadata string) {
	if reqBody == nil {
		return "", ""
	}
	switch metadata := reqBody["client_metadata"].(type) {
	case map[string]any:
		if value, ok := metadata[codexInstallationIDKey].(string); ok {
			direct = strings.TrimSpace(value)
		}
		if value, ok := metadata[openAIWSTurnMetadataHeader].(string); ok {
			turnMetadata = value
		}
	case map[string]string:
		direct = strings.TrimSpace(metadata[codexInstallationIDKey])
		turnMetadata = metadata[openAIWSTurnMetadataHeader]
	}
	return direct, turnMetadata
}

func extractInstallationIDFromTurnMetadata(raw string) string {
	var metadata map[string]any
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &metadata) != nil || metadata == nil {
		return ""
	}
	value, _ := metadata[codexTurnMetadataInstallationIDKey].(string)
	return strings.TrimSpace(value)
}

// rewriteCodexTurnMetadataInstallationID rewrites only installation_id in an
// existing JSON object. Missing or opaque metadata is preserved byte-for-byte.
func rewriteCodexTurnMetadataInstallationID(raw, id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" || strings.TrimSpace(raw) == "" {
		return raw, false
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		return raw, false
	}
	if encoded, ok := metadata[codexTurnMetadataInstallationIDKey]; ok {
		var current string
		if json.Unmarshal(encoded, &current) == nil && current == id {
			return raw, false
		}
	}
	encodedID, err := json.Marshal(id)
	if err != nil {
		return raw, false
	}
	metadata[codexTurnMetadataInstallationIDKey] = encodedID
	rewritten, err := json.Marshal(metadata)
	if err != nil {
		return raw, false
	}
	return string(rewritten), true
}

// rewriteOpenAIInstallationIDInBody makes all parseable installation identity
// fields in a regular Responses body agree with the request-scoped pinned ID.
// It creates only the top-level client_metadata container; turn metadata is
// rewritten only when the client supplied it.
func rewriteOpenAIInstallationIDInBody(reqBody map[string]any, id string) bool {
	id = strings.TrimSpace(id)
	if reqBody == nil || id == "" {
		return false
	}

	var metadata map[string]any
	changed := false
	existingValue, present := reqBody["client_metadata"]
	switch existing := existingValue.(type) {
	case map[string]any:
		metadata = existing
	case map[string]string:
		metadata = make(map[string]any, len(existing)+1)
		for key, value := range existing {
			metadata[key] = value
		}
		changed = true
	default:
		if present {
			return false
		}
		metadata = make(map[string]any, 1)
		changed = true
	}

	if current, ok := metadata[codexInstallationIDKey].(string); !ok || current != id {
		metadata[codexInstallationIDKey] = id
		changed = true
	}
	if raw, ok := metadata[openAIWSTurnMetadataHeader].(string); ok {
		if rewritten, nestedChanged := rewriteCodexTurnMetadataInstallationID(raw, id); nestedChanged {
			metadata[openAIWSTurnMetadataHeader] = rewritten
			changed = true
		}
	}
	if changed {
		reqBody["client_metadata"] = metadata
	}
	return changed
}

// stripOpenAICompactClientMetadata enforces the narrower HTTP compact schema.
func stripOpenAICompactClientMetadata(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	if _, ok := reqBody["client_metadata"]; !ok {
		return false
	}
	delete(reqBody, "client_metadata")
	return true
}

// rewriteOpenAIInstallationIDHeaders applies the pinned ID to the direct
// header and to every parseable turn-metadata header value. It never creates a
// turn-metadata header when the client did not send one.
func rewriteOpenAIInstallationIDHeaders(headers http.Header, id string) bool {
	id = strings.TrimSpace(id)
	if headers == nil || id == "" {
		return false
	}
	directValues := headerValuesCaseInsensitive(headers, codexInstallationIDKey)
	changed := len(directValues) != 1 || strings.TrimSpace(directValues[0]) != id
	deleteOpenAIHeaderEqualFold(headers, codexInstallationIDKey)
	headers.Set(codexInstallationIDKey, id)

	values := headerValuesCaseInsensitive(headers, openAIWSTurnMetadataHeader)
	if len(values) == 0 {
		return changed
	}
	rewrittenValues := make([]string, len(values))
	nestedChanged := false
	for index, raw := range values {
		rewrittenValues[index] = raw
		if rewritten, valueChanged := rewriteCodexTurnMetadataInstallationID(raw, id); valueChanged {
			rewrittenValues[index] = rewritten
			nestedChanged = true
		}
	}
	if nestedChanged || len(values) > 0 {
		deleteOpenAIHeaderEqualFold(headers, openAIWSTurnMetadataHeader)
		for _, value := range rewrittenValues {
			headers.Add(openAIWSTurnMetadataHeader, value)
		}
	}
	return changed || nestedChanged
}

// enforceCodexInstallationIDInBody force-sets client_metadata[installation] to
// the account-owned value, overwriting whatever the client sent. Returns true
// when the body was mutated. Unlike applyCodexClientMetadata (which is additive
// and never overrides), this is authoritative and used only when pinning is on.
func enforceCodexInstallationIDInBody(reqBody map[string]any, id string) bool {
	return rewriteOpenAIInstallationIDInBody(reqBody, id)
}
