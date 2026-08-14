package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	openAIOAuthIdentityCaptureContextKey   = "openai_oauth_identity_capture"
	openAIOAuthIdentityPlanContextKey      = "openai_oauth_identity_plan"
	openAICodexFingerprintPolicyContextKey = "openai_codex_fingerprint_policy"
	openAICodexClientIdentityContextKey    = "openai_codex_client_identity"
)

// OpenAIOAuthIdentityProjectionMode selects the final wire projection. Capture
// and resolution are deliberately independent of this transport decision.
type OpenAIOAuthIdentityProjectionMode string

const (
	OpenAIOAuthIdentityProjectionRegular                  OpenAIOAuthIdentityProjectionMode = "regular"
	OpenAIOAuthIdentityProjectionCompact                  OpenAIOAuthIdentityProjectionMode = "compact"
	OpenAIOAuthIdentityProjectionHeadersOnly              OpenAIOAuthIdentityProjectionMode = "headers_only"
	OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly OpenAIOAuthIdentityProjectionMode = "existing_turn_metadata_only"
	OpenAIOAuthIdentityProjectionAlphaSearch              OpenAIOAuthIdentityProjectionMode = "alpha_search"
)

type OpenAIOAuthInstallationPolicy string

const (
	OpenAIOAuthInstallationAccountPin OpenAIOAuthInstallationPolicy = "account_pin"
	OpenAIOAuthInstallationPreserve   OpenAIOAuthInstallationPolicy = "preserve"
)

// OpenAICodexLogicalTurnAlias is an explicit endpoint spelling observed on the
// same inbound request. Fallback seeds never become aliases of an explicit
// tuple.
type OpenAICodexLogicalTurnAlias struct {
	SessionKey          string
	ThreadKey           string
	ParentThreadKey     string
	ForkedFromThreadKey string
	Source              string
	Explicit            bool
	Priority            int
}

// OpenAIOAuthIdentityCapture is immutable request input captured before any
// compatibility or compact body transformation.
type OpenAIOAuthIdentityCapture struct {
	Logical              OpenAICodexLogicalTurnIdentity
	Aliases              []OpenAICodexLogicalTurnAlias
	ClientInstallationID string
	ConflictCount        int
	InvalidMetadataCount int
}

// OpenAICodexIdentityInput is the public capture contract named by the unified
// OAuth identity pipeline. OpenAIOAuthIdentityCapture remains the compatible
// spelling used by the first set of call sites.
type OpenAICodexIdentityInput = OpenAIOAuthIdentityCapture

type OpenAIOAuthIdentityPlanOptions struct {
	TurnIdentityEnabled bool
	ProjectionMode      OpenAIOAuthIdentityProjectionMode
	InstallationPolicy  OpenAIOAuthInstallationPolicy
}

// ResolveOpenAIOAuthProfileIdentityPlan resolves only the account-scoped
// installation and client identity portions. It is used by OAuth endpoints
// that do not carry a logical Codex turn and therefore must never touch the V2
// UUIDv7 store.
func (s *OpenAIGatewayService) ResolveOpenAIOAuthProfileIdentityPlan(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	installationPolicy OpenAIOAuthInstallationPolicy,
) (OpenAIOAuthIdentityPlan, error) {
	return s.GetOrResolveOpenAIOAuthOutboundIdentity(ctx, c, account, OpenAIOAuthIdentityCapture{}, OpenAIOAuthIdentityPlanOptions{
		TurnIdentityEnabled: false,
		ProjectionMode:      OpenAIOAuthIdentityProjectionRegular,
		InstallationPolicy:  installationPolicy,
	}, nil)
}

// OpenAIOAuthIdentityPlan contains every generated identity needed by the
// final projector. Apply never consults a store and never resolves again.
type OpenAIOAuthIdentityPlan struct {
	Capture                  OpenAIOAuthIdentityCapture
	PolicySnapshot           CodexFingerprintPolicySnapshot
	ClientIdentity           CodexClientIdentityPlan
	ClientIdentityEnabled    bool
	TurnIdentity             OpenAICodexTurnIdentity
	TurnIdentityEnabled      bool
	TurnIdentityRequested    bool
	InstallationID           string
	InstallationEnabled      bool
	ProjectionMode           OpenAIOAuthIdentityProjectionMode
	InstallationPolicy       OpenAIOAuthInstallationPolicy
	CredentialOwnerNamespace string
	APIKeyID                 int64
	ResolveSource            string
	ResolveOutcome           OpenAIOAuthIdentityResolveOutcome
	SocketDigest             string
}

// OpenAIOAuthOutboundIdentityPlan is the immutable outbound-plan name used by
// the unified Codex fingerprint pipeline. Keep OpenAIOAuthIdentityPlan as the
// established implementation spelling for source compatibility.
type OpenAIOAuthOutboundIdentityPlan = OpenAIOAuthIdentityPlan

type OpenAIOAuthIdentityResolveOutcome string

const (
	OpenAIOAuthIdentityResolveNone          OpenAIOAuthIdentityResolveOutcome = "none"
	OpenAIOAuthIdentityResolvePrimary       OpenAIOAuthIdentityResolveOutcome = "primary"
	OpenAIOAuthIdentityResolveFallback      OpenAIOAuthIdentityResolveOutcome = "fallback"
	OpenAIOAuthIdentityResolveAliasConflict OpenAIOAuthIdentityResolveOutcome = "alias_conflict"
	OpenAIOAuthIdentityResolveAliasJump     OpenAIOAuthIdentityResolveOutcome = "alias_jump"
	OpenAIOAuthIdentityResolveStoreError    OpenAIOAuthIdentityResolveOutcome = "store_error"
)

func CaptureOpenAIOAuthIdentity(c *gin.Context, body []byte, callerSeed string) OpenAIOAuthIdentityCapture {
	return CaptureOpenAIOAuthIdentityWithTurnMetadata(c, body, callerSeed, "")
}

// CaptureOpenAICodexIdentityInput is the explicit facade entrypoint named by
// the Codex fingerprint pipeline. The older OAuth spelling remains available
// to existing callers while both return the same immutable capture value.
func CaptureOpenAICodexIdentityInput(c *gin.Context, body []byte, callerSeed string) OpenAICodexIdentityInput {
	return CaptureOpenAIOAuthIdentity(c, body, callerSeed)
}

func CaptureOpenAIOAuthIdentityWithTurnMetadata(c *gin.Context, body []byte, callerSeed, explicitTurnMetadata string) OpenAIOAuthIdentityCapture {
	return captureOpenAIOAuthIdentity(c, body, callerSeed, explicitTurnMetadata, false)
}

// CaptureOpenAIOAuthIdentityWithEndpointAlias captures an endpoint-native
// legacy identifier as the lowest-priority compatibility alias.
func CaptureOpenAIOAuthIdentityWithEndpointAlias(c *gin.Context, body []byte, endpointAlias string) OpenAIOAuthIdentityCapture {
	return captureOpenAIOAuthIdentity(c, body, endpointAlias, "", true)
}

func captureOpenAIOAuthIdentity(c *gin.Context, body []byte, callerSeed, explicitTurnMetadata string, appendEndpointAlias bool) OpenAIOAuthIdentityCapture {
	capture := captureOpenAICodexLogicalTurnIdentity(c, body, callerSeed, explicitTurnMetadata, appendEndpointAlias)
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if len(body) > 0 && decoder.Decode(&decoded) == nil {
		capture.ClientInstallationID = extractClientInstallationID(c, decoded)
	} else {
		capture.ClientInstallationID = extractClientInstallationID(c, nil)
	}
	return capture
}

// FillOpenAIOAuthIdentityCaptureFallback adds a deferred affinity seed without
// parsing the ingress body again. Explicit identity captured from the original
// request always keeps priority.
func FillOpenAIOAuthIdentityCaptureFallback(c *gin.Context, callerSeed string) bool {
	capture, ok := OpenAIOAuthIdentityCaptureFromContext(c)
	if !ok || strings.TrimSpace(capture.Logical.SessionKey) != "" {
		return false
	}
	seed := sanitizeSessionID(callerSeed)
	if seed == "" {
		return false
	}
	capture.Logical = normalizeLogicalTuple(
		openAICodexLogicalTuple{session: seed, thread: seed},
		OpenAIOutboundSessionLogicalKeySourceCallerSeed,
		false,
	)
	capture.Aliases = []OpenAICodexLogicalTurnAlias{{
		SessionKey: seed,
		ThreadKey:  seed,
		Source:     capture.Logical.Source,
	}}
	SetOpenAIOAuthIdentityCapture(c, capture)
	return true
}

func normalizeOpenAIOAuthIdentityPlanOptions(options OpenAIOAuthIdentityPlanOptions) OpenAIOAuthIdentityPlanOptions {
	switch options.ProjectionMode {
	case OpenAIOAuthIdentityProjectionRegular, OpenAIOAuthIdentityProjectionCompact,
		OpenAIOAuthIdentityProjectionHeadersOnly, OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly,
		OpenAIOAuthIdentityProjectionAlphaSearch:
	default:
		options.ProjectionMode = OpenAIOAuthIdentityProjectionRegular
	}
	if options.InstallationPolicy != OpenAIOAuthInstallationPreserve {
		options.InstallationPolicy = OpenAIOAuthInstallationAccountPin
	}
	return options
}

func (s *OpenAIGatewayService) ResolveOpenAIOAuthIdentityPlan(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	capture OpenAIOAuthIdentityCapture,
	options OpenAIOAuthIdentityPlanOptions,
) (OpenAIOAuthIdentityPlan, error) {
	options = normalizeOpenAIOAuthIdentityPlanOptions(options)
	policy := s.openAICodexFingerprintPolicyForRequest(ctx, c)
	plan := OpenAIOAuthIdentityPlan{
		Capture: capture, ProjectionMode: options.ProjectionMode,
		InstallationPolicy: options.InstallationPolicy,
		PolicySnapshot:     policy,
		APIKeyID:           getAPIKeyIDFromContext(c), ResolveSource: capture.Logical.Source,
		ResolveOutcome: OpenAIOAuthIdentityResolveNone,
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return plan, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plan.ClientIdentityEnabled = true
	plan.TurnIdentityRequested = options.TurnIdentityEnabled && policy.TurnIdentityNormalizationEnabled()
	canonicalClientIdentity := openAICodexClientIdentityForRequest(c)
	plan.ClientIdentity = resolveCodexClientIdentityPlanFromSnapshot(
		CodexClientIdentitySafePair, "", canonicalClientIdentity,
	)
	forceCodexCLI := s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI
	if policy.ClientIdentityNormalizationEnabled() || forceCodexCLI {
		overrideUA := ""
		var err error
		if !forceCodexCLI {
			var accountRepo AccountRepository
			if s != nil {
				accountRepo = s.accountRepo
			}
			overrideUA, err = resolveOpenAIAccountStoredUserAgent(ctx, accountRepo, account)
		}
		if err != nil {
			return plan, fmt.Errorf("resolve OpenAI account User-Agent: %w", err)
		}
		plan.ClientIdentity = resolveCodexClientIdentityPlanFromSnapshot(
			CodexClientIdentityNormalize, overrideUA, canonicalClientIdentity,
		)
	}
	if namespace, err := s.resolveOpenAIOutboundSessionIdentityNamespace(ctx, account); err == nil {
		plan.CredentialOwnerNamespace = namespace
	}
	if plan.TurnIdentityRequested && strings.TrimSpace(capture.Logical.SessionKey) != "" {
		observeOpenAIOAuthIdentityResolveSource(capture.Logical.Source)
		identity, ok, outcome, err := s.resolveOpenAICodexTurnIdentityWithAliasesDetailed(ctx, c, account, capture.Logical, capture.Aliases)
		plan.ResolveOutcome = outcome
		if err != nil {
			if errors.Is(err, ErrOpenAICodexAliasConflict) {
				plan.ResolveOutcome = OpenAIOAuthIdentityResolveAliasConflict
				return plan, err
			}
			if errors.Is(err, errOpenAIOutboundSessionIdentityNamespace) {
				return plan, err
			}
			plan.ResolveOutcome = OpenAIOAuthIdentityResolveStoreError
		} else if ok {
			plan.TurnIdentity = identity
			plan.TurnIdentityEnabled = true
			if plan.ResolveOutcome == OpenAIOAuthIdentityResolveNone {
				plan.ResolveOutcome = OpenAIOAuthIdentityResolvePrimary
			}
		}
		observeOpenAIOAuthIdentityResolveOutcome(plan.ResolveOutcome)
	}
	if options.InstallationPolicy == OpenAIOAuthInstallationAccountPin && policy.InstallationIDNormalizationEnabled() {
		resolution, err := s.resolveInstallationIDForRequest(ctx, c, account, capture.ClientInstallationID)
		if err != nil {
			return plan, err
		}
		plan.InstallationEnabled = resolution.Enabled && resolution.OutboundID != ""
		plan.InstallationID = resolution.OutboundID
	}
	return plan, nil
}

// ResolveOpenAIOAuthOutboundIdentity exposes the credential-aware materialize
// step under the unified pipeline name.
func (s *OpenAIGatewayService) ResolveOpenAIOAuthOutboundIdentity(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	capture OpenAICodexIdentityInput,
	options OpenAIOAuthIdentityPlanOptions,
) (OpenAIOAuthOutboundIdentityPlan, error) {
	return s.ResolveOpenAIOAuthIdentityPlan(ctx, c, account, capture, options)
}

// GetOrResolveOpenAIOAuthOutboundIdentity is the only production materialize
// entrypoint. A cached plan is reusable only for the exact immutable capture,
// credential owner, downstream API key, policy snapshot, and projection
// options. Account failover therefore rematerializes from the same capture.
func (s *OpenAIGatewayService) GetOrResolveOpenAIOAuthOutboundIdentity(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	capture OpenAICodexIdentityInput,
	options OpenAIOAuthIdentityPlanOptions,
	pinnedPlan *OpenAIOAuthOutboundIdentityPlan,
) (OpenAIOAuthOutboundIdentityPlan, error) {
	options = normalizeOpenAIOAuthIdentityPlanOptions(options)
	if pinnedPlan != nil &&
		openAIOAuthIdentityCapturesEqual(pinnedPlan.Capture, capture) &&
		s.OpenAIOAuthIdentityPlanMatches(ctx, c, account, *pinnedPlan, options) {
		plan := *pinnedPlan
		SetOpenAIOAuthIdentityPlan(c, plan)
		return plan, nil
	}
	if cached, ok := OpenAIOAuthIdentityPlanFromContext(c); ok &&
		openAIOAuthIdentityCapturesEqual(cached.Capture, capture) &&
		s.OpenAIOAuthIdentityPlanMatches(ctx, c, account, cached, options) {
		return cached, nil
	}

	plan, err := s.ResolveOpenAIOAuthOutboundIdentity(ctx, c, account, capture, options)
	if err != nil {
		return plan, err
	}
	SetOpenAIOAuthIdentityPlan(c, plan)
	return plan, nil
}

func openAIOAuthIdentityCapturesEqual(left, right OpenAIOAuthIdentityCapture) bool {
	if left.Logical != right.Logical ||
		left.ClientInstallationID != right.ClientInstallationID ||
		left.ConflictCount != right.ConflictCount ||
		left.InvalidMetadataCount != right.InvalidMetadataCount ||
		len(left.Aliases) != len(right.Aliases) {
		return false
	}
	for i := range left.Aliases {
		if left.Aliases[i] != right.Aliases[i] {
			return false
		}
	}
	return true
}

func openAICodexClientIdentityForRequest(c *gin.Context) codexOutboundIdentity {
	if c != nil {
		if value, ok := c.Get(openAICodexClientIdentityContextKey); ok {
			if identity, valid := value.(codexOutboundIdentity); valid {
				return identity
			}
		}
	}
	identity := resolveCodexOutboundIdentity("")
	if c != nil {
		c.Set(openAICodexClientIdentityContextKey, identity)
	}
	return identity
}

// OpenAIOAuthIdentityPlanMatches reports whether a cached plan may be reused
// for the selected credential owner and final projection. Account failover,
// API-key changes, or option changes require rematerialization from Capture.
func (s *OpenAIGatewayService) OpenAIOAuthIdentityPlanMatches(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	plan OpenAIOAuthIdentityPlan,
	options OpenAIOAuthIdentityPlanOptions,
) bool {
	if account == nil || !account.IsOpenAIOAuth() {
		return false
	}
	options = normalizeOpenAIOAuthIdentityPlanOptions(options)
	if plan.APIKeyID != getAPIKeyIDFromContext(c) ||
		plan.ProjectionMode != options.ProjectionMode ||
		plan.InstallationPolicy != options.InstallationPolicy ||
		plan.TurnIdentityRequested != (options.TurnIdentityEnabled && plan.PolicySnapshot.TurnIdentityNormalizationEnabled()) {
		return false
	}
	currentPolicy := s.openAICodexFingerprintPolicyForRequest(ctx, c)
	if currentPolicy != plan.PolicySnapshot {
		return false
	}
	namespace, err := s.resolveOpenAIOutboundSessionIdentityNamespace(ctx, account)
	return err == nil && namespace == plan.CredentialOwnerNamespace
}

// ApplyOpenAIOAuthIdentityPlan is a pure final projection: it performs no
// account lookup, setting read, or identity-store operation.
func ApplyOpenAIOAuthIdentityPlan(headers http.Header, body []byte, plan OpenAIOAuthIdentityPlan) ([]byte, error) {
	mode := normalizeOpenAIOAuthIdentityPlanOptions(OpenAIOAuthIdentityPlanOptions{
		ProjectionMode: plan.ProjectionMode, InstallationPolicy: plan.InstallationPolicy,
	}).ProjectionMode
	if mode == OpenAIOAuthIdentityProjectionAlphaSearch {
		applyOpenAICodexAlphaSearchIdentityHeader(headers, plan)
		if plan.ClientIdentityEnabled {
			applyCodexClientIdentityPlan(headers, plan.ClientIdentity)
		}
		return body, nil
	}
	out := body
	// Compact has a deliberately narrow Rust request schema. Strip the regular
	// Responses metadata carrier independently of the installation switch so a
	// partial fingerprint rollback cannot leak client_metadata onto this path.
	if mode == OpenAIOAuthIdentityProjectionCompact && len(out) > 0 {
		var root map[string]json.RawMessage
		if err := json.Unmarshal(out, &root); err != nil || root == nil {
			if err == nil {
				err = errors.New("expected object")
			}
			return body, fmt.Errorf("decode OpenAI OAuth compact body: %w", err)
		}
		if _, present := root["client_metadata"]; present {
			delete(root, "client_metadata")
			encoded, err := marshalJSONWithoutHTMLEscape(root)
			if err != nil {
				return body, fmt.Errorf("encode OpenAI OAuth compact body: %w", err)
			}
			out = encoded
		}
	}
	if plan.InstallationPolicy != OpenAIOAuthInstallationPreserve && plan.InstallationEnabled && plan.InstallationID != "" {
		if headers != nil {
			rewriteOpenAIInstallationIDHeaders(headers, plan.InstallationID)
		}
		if mode != OpenAIOAuthIdentityProjectionHeadersOnly && mode != OpenAIOAuthIdentityProjectionCompact && len(out) > 0 {
			var root map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(out)))
			decoder.UseNumber()
			if err := decoder.Decode(&root); err != nil {
				return body, fmt.Errorf("decode OpenAI OAuth installation body: %w", err)
			}
			modified := false
			if mode == OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly {
				modified = rewriteExistingOpenAITurnMetadataInstallationInBody(root, plan.InstallationID)
			} else {
				// A non-object client_metadata value is opaque client input. The
				// installation header is still pinned, but the body container is
				// not replaced merely to create an installation field.
				if clientMetadata, present := root["client_metadata"]; !present || isJSONObject(clientMetadata) {
					modified = rewriteOpenAIInstallationIDInBody(root, plan.InstallationID)
				}
			}
			if !modified {
				goto installationBodyDone
			}
			encoded, err := marshalJSONWithoutHTMLEscape(root)
			if err != nil {
				return body, fmt.Errorf("encode OpenAI OAuth installation body: %w", err)
			}
			out = encoded
		}
	}

installationBodyDone:
	if plan.TurnIdentityEnabled {
		switch mode {
		case OpenAIOAuthIdentityProjectionCompact:
			applyOpenAICodexTurnIdentityHeaders(headers, plan.TurnIdentity, true)
		case OpenAIOAuthIdentityProjectionHeadersOnly:
			applyOpenAICodexTurnIdentityHeaders(headers, plan.TurnIdentity, false)
		case OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly:
			applyOpenAICodexExistingTurnMetadataHeader(headers, plan.TurnIdentity)
			var err error
			out, err = mergeOpenAICodexExistingTurnMetadataBody(out, plan.TurnIdentity)
			if err != nil {
				return body, err
			}
		default:
			applyOpenAICodexTurnIdentityHeaders(headers, plan.TurnIdentity, false)
			var err error
			out, err = mergeOpenAICodexTurnIdentityBody(out, plan.TurnIdentity)
			if err != nil {
				return body, err
			}
		}
	}
	if plan.ClientIdentityEnabled {
		applyCodexClientIdentityPlan(headers, plan.ClientIdentity)
	}
	return out, nil
}

// applyOpenAICodexAlphaSearchIdentityHeader projects the account-owned
// identity into the one carrier used by Codex SearchClient. Parseable objects
// retain unknown fields; opaque values remain byte-for-byte intact. A canonical
// object is created only when the client did not send this carrier at all.
func applyOpenAICodexAlphaSearchIdentityHeader(headers http.Header, plan OpenAIOAuthIdentityPlan) {
	if headers == nil {
		return
	}
	deleteOpenAICodexIdentityHeaders(headers)
	deleteOpenAIHeaderEqualFold(headers, codexInstallationIDKey)

	projectTurn := plan.TurnIdentityEnabled
	projectInstallation := plan.InstallationPolicy == OpenAIOAuthInstallationAccountPin &&
		plan.InstallationEnabled && strings.TrimSpace(plan.InstallationID) != ""
	if !projectTurn && !projectInstallation {
		return
	}

	values := headerValuesCaseInsensitive(headers, openAIWSTurnMetadataHeader)
	projected := make([]string, 0, len(values)+1)
	for _, value := range values {
		var metadata map[string]json.RawMessage
		if json.Unmarshal([]byte(strings.TrimSpace(value)), &metadata) != nil || metadata == nil {
			projected = append(projected, value)
			continue
		}
		rewritten := value
		if projectTurn {
			rewritten, _ = rewriteOpenAICodexTurnMetadata(rewritten, plan.TurnIdentity)
		}
		if projectInstallation {
			if withInstallation, changed := rewriteCodexTurnMetadataInstallationID(rewritten, plan.InstallationID); changed {
				rewritten = withInstallation
			}
		}
		projected = append(projected, rewritten)
	}
	if len(values) == 0 {
		canonical := "{}"
		if projectTurn {
			canonical, _ = rewriteOpenAICodexTurnMetadata(canonical, plan.TurnIdentity)
		}
		if projectInstallation {
			canonical, _ = rewriteCodexTurnMetadataInstallationID(canonical, plan.InstallationID)
		}
		projected = append(projected, canonical)
	}

	deleteOpenAIHeaderEqualFold(headers, openAIWSTurnMetadataHeader)
	for _, value := range projected {
		headers.Add(openAIWSTurnMetadataHeader, value)
	}
}

func (s *OpenAIGatewayService) openAICodexFingerprintPolicyForRequest(
	ctx context.Context,
	c *gin.Context,
) CodexFingerprintPolicySnapshot {
	if c != nil {
		if value, ok := c.Get(openAICodexFingerprintPolicyContextKey); ok {
			if snapshot, valid := value.(CodexFingerprintPolicySnapshot); valid {
				return snapshot
			}
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var snapshot CodexFingerprintPolicySnapshot
	if s == nil || s.settingService == nil {
		snapshot = CodexFingerprintPolicySnapshot{
			MasterEnabled:         true,
			InstallationIDEnabled: true,
			TurnIdentityEnabled:   true,
			ClientIdentityEnabled: true,
		}
	} else {
		snapshot = s.settingService.GetOpenAICodexFingerprintPolicy(ctx)
	}
	if c != nil {
		c.Set(openAICodexFingerprintPolicyContextKey, snapshot)
	}
	return snapshot
}

func isJSONObject(value any) bool {
	switch value.(type) {
	case map[string]any, map[string]string:
		return true
	default:
		return false
	}
}

func rewriteExistingOpenAITurnMetadataInstallationInBody(root map[string]any, installationID string) bool {
	if root == nil || strings.TrimSpace(installationID) == "" {
		return false
	}
	modified := false
	if raw, ok := root[openAIWSTurnMetadataHeader].(string); ok {
		if rewritten, changed := rewriteCodexTurnMetadataInstallationID(raw, installationID); changed {
			root[openAIWSTurnMetadataHeader] = rewritten
			modified = true
		}
	}
	if metadata, ok := root["client_metadata"].(map[string]any); ok {
		if raw, exists := metadata[openAIWSTurnMetadataHeader].(string); exists {
			if rewritten, changed := rewriteCodexTurnMetadataInstallationID(raw, installationID); changed {
				metadata[openAIWSTurnMetadataHeader] = rewritten
				modified = true
			}
		}
	}
	return modified
}

func applyOpenAICodexExistingTurnMetadataHeader(headers http.Header, identity OpenAICodexTurnIdentity) {
	if headers == nil {
		return
	}
	var projected []string
	for key, values := range headers {
		if !strings.EqualFold(key, openAIWSTurnMetadataHeader) {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				if rewritten, err := rewriteOpenAICodexTurnMetadata(value, identity); err == nil {
					value = rewritten
				}
			}
			projected = append(projected, value)
		}
		delete(headers, key)
	}
	for _, value := range projected {
		headers.Add(openAIWSTurnMetadataHeader, value)
	}
}

func mergeOpenAICodexExistingTurnMetadataBody(body []byte, identity OpenAICodexTurnIdentity) ([]byte, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return body, nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return body, nil
	}
	modified := false
	rewriteField := func(object map[string]json.RawMessage) error {
		raw, present := object[openAIWSTurnMetadataHeader]
		if !present {
			return nil
		}
		current := normalizeOpenAICodexTurnMetadataRaw(raw)
		if current == "" {
			return nil
		}
		rewritten, err := rewriteOpenAICodexTurnMetadata(current, identity)
		if err != nil {
			// Opaque metadata is a client-owned compatibility carrier. Direct
			// alpha projection must leave it byte-for-byte intact.
			return nil
		}
		object[openAIWSTurnMetadataHeader] = mustMarshalJSONString(rewritten)
		modified = true
		return nil
	}
	if err := rewriteField(root); err != nil {
		return body, err
	}
	if raw, present := root["client_metadata"]; present {
		var metadata map[string]json.RawMessage
		if json.Unmarshal(raw, &metadata) == nil && metadata != nil {
			if err := rewriteField(metadata); err != nil {
				return body, err
			}
			if modified {
				encoded, err := marshalJSONWithoutHTMLEscape(metadata)
				if err != nil {
					return body, err
				}
				root["client_metadata"] = encoded
			}
		}
	}
	if !modified {
		return body, nil
	}
	return marshalJSONWithoutHTMLEscape(root)
}

func cloneOpenAIOAuthIdentityCapture(capture OpenAIOAuthIdentityCapture) OpenAIOAuthIdentityCapture {
	capture.Aliases = append([]OpenAICodexLogicalTurnAlias(nil), capture.Aliases...)
	return capture
}

func SetOpenAIOAuthIdentityCapture(c *gin.Context, capture OpenAIOAuthIdentityCapture) {
	if c != nil {
		// A capture defines one logical inbound turn. Replacing it is the only
		// unconditional invalidation boundary for a materialized plan; transport
		// retries on the same Gin request keep the capture and let PlanMatches
		// decide whether the selected credential owner may reuse the plan.
		if current, ok := OpenAIOAuthIdentityCaptureFromContext(c); ok &&
			openAIOAuthIdentityCapturesEqual(current, capture) {
			return
		}
		ClearOpenAIOAuthIdentityPlan(c)
		c.Set(openAIOAuthIdentityCaptureContextKey, cloneOpenAIOAuthIdentityCapture(capture))
	}
}

func OpenAIOAuthIdentityCaptureFromContext(c *gin.Context) (OpenAIOAuthIdentityCapture, bool) {
	if c == nil {
		return OpenAIOAuthIdentityCapture{}, false
	}
	value, ok := c.Get(openAIOAuthIdentityCaptureContextKey)
	capture, valid := value.(OpenAIOAuthIdentityCapture)
	return cloneOpenAIOAuthIdentityCapture(capture), ok && valid
}

func ClearOpenAIOAuthIdentityCapture(c *gin.Context) {
	if c != nil {
		ClearOpenAIOAuthIdentityPlan(c)
		c.Set(openAIOAuthIdentityCaptureContextKey, nil)
	}
}

func SetOpenAIOAuthIdentityPlan(c *gin.Context, plan OpenAIOAuthIdentityPlan) {
	if c != nil {
		plan.Capture = cloneOpenAIOAuthIdentityCapture(plan.Capture)
		c.Set(openAIOAuthIdentityPlanContextKey, plan)
	}
}

func OpenAIOAuthIdentityPlanFromContext(c *gin.Context) (OpenAIOAuthIdentityPlan, bool) {
	if c == nil {
		return OpenAIOAuthIdentityPlan{}, false
	}
	value, ok := c.Get(openAIOAuthIdentityPlanContextKey)
	plan, valid := value.(OpenAIOAuthIdentityPlan)
	plan.Capture = cloneOpenAIOAuthIdentityCapture(plan.Capture)
	return plan, ok && valid
}

func ClearOpenAIOAuthIdentityPlan(c *gin.Context) {
	if c != nil {
		c.Set(openAIOAuthIdentityPlanContextKey, nil)
	}
}
