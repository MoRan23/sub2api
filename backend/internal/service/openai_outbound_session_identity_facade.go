package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	OpenAIOAuthIdentityProjectionPassthrough              OpenAIOAuthIdentityProjectionMode = "passthrough"
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

// OpenAICodexRequestTurnSnapshot is the request-scoped Codex turn instance.
// Unlike the account-scoped session/thread mapping, it is never persisted:
// retries and credential failover reuse the capture while a new ingress turn
// receives a new UUIDv7.
type OpenAICodexRequestTurnSnapshot struct {
	ID              string
	TypedID         CodexTurnID
	StartedAtUnixMS int64
	Source          string
	Explicit        bool
	Generated       bool
}

// OpenAICodexPromptCacheKeyKind records the semantic role of the inbound
// prompt_cache_key. Codex normally uses the logical session id, while
// guardian reviews and future callers may intentionally provide an override.
type OpenAICodexPromptCacheKeyKind string

const (
	OpenAICodexPromptCacheKeyMissing  OpenAICodexPromptCacheKeyKind = "missing"
	OpenAICodexPromptCacheKeyDefault  OpenAICodexPromptCacheKeyKind = "default"
	OpenAICodexPromptCacheKeyGuardian OpenAICodexPromptCacheKeyKind = "guardian"
	OpenAICodexPromptCacheKeyOverride OpenAICodexPromptCacheKeyKind = "override"
	OpenAICodexPromptCacheKeyInvalid  OpenAICodexPromptCacheKeyKind = "invalid"
)

// OpenAICodexPromptCacheKeySnapshot is captured from the untouched ingress
// body. It remains account-independent so retries and credential failover can
// materialize a new owner-scoped outbound value without reparsing a narrowed
// compact body.
type OpenAICodexPromptCacheKeySnapshot struct {
	Value      string
	Kind       OpenAICodexPromptCacheKeyKind
	Present    bool
	Valid      bool
	Applicable bool
}

// OpenAICodexPromptCacheKeyPlan is the immutable final wire value. Generic
// overrides are mapped with an owner-scoped HMAC; no prompt-cache mapping is
// persisted in Redis or the UUIDv7 identity store.
type OpenAICodexPromptCacheKeyPlan struct {
	Value   string
	Kind    OpenAICodexPromptCacheKeyKind
	Enabled bool
}

const (
	openAICodexRequestTurnSourceClientMetadata = "client_metadata.x_codex_turn_metadata"
	openAICodexRequestTurnSourceHeader         = "header.x_codex_turn_metadata"
	openAICodexRequestTurnSourceWS             = "ws.x_codex_turn_metadata"
	openAICodexRequestTurnSourceBody           = "body.x_codex_turn_metadata"
	openAICodexRequestTurnSourceFlatMetadata   = "client_metadata.turn_id"
	openAICodexRequestTurnSourceCompatBody     = "body.turn_id"
	openAICodexRequestTurnSourceGenerated      = "generated"
)

// OpenAIOAuthIdentityCapture is immutable request input captured before any
// compatibility or compact body transformation.
type OpenAIOAuthIdentityCapture struct {
	Logical              OpenAICodexLogicalTurnIdentity
	Aliases              []OpenAICodexLogicalTurnAlias
	RequestTurn          OpenAICodexRequestTurnSnapshot
	PromptCacheKey       OpenAICodexPromptCacheKeySnapshot
	WireProfile          CodexWireProfile
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
	RequestTurn              OpenAICodexRequestTurnSnapshot
	PromptCacheKey           OpenAICodexPromptCacheKeyPlan
	WireProfile              CodexWireProfile
	Window                   OpenAICodexWindowSnapshot
	WindowMappingKey         string
	WindowResolveOutcome     OpenAICodexWindowResolveOutcome
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

type OpenAICodexWindowResolveOutcome string

const (
	OpenAICodexWindowResolveNone     OpenAICodexWindowResolveOutcome = "none"
	OpenAICodexWindowResolveResolved OpenAICodexWindowResolveOutcome = "resolved"
	OpenAICodexWindowResolveError    OpenAICodexWindowResolveOutcome = "error"
)

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
	return captureOpenAIOAuthIdentity(c, body, callerSeed, explicitTurnMetadata, false, false, true, "")
}

// CaptureOpenAIOAuthIdentityForCompatTurn freezes compatibility conversions as
// ordinary turns before request-turn capture. Inbound Responses metadata must
// not be able to suppress the UUIDv7 that Chat/Messages conversion later needs.
func CaptureOpenAIOAuthIdentityForCompatTurn(c *gin.Context, body []byte, callerSeed string) OpenAIOAuthIdentityCapture {
	return captureOpenAIOAuthIdentity(c, body, callerSeed, "", false, false, true, CodexWireRequestTurn)
}

// CaptureOpenAIOAuthIdentityWithEndpointAlias captures an endpoint-native
// legacy identifier as the lowest-priority compatibility alias.
func CaptureOpenAIOAuthIdentityWithEndpointAlias(c *gin.Context, body []byte, endpointAlias string) OpenAIOAuthIdentityCapture {
	return captureOpenAIOAuthIdentity(c, body, endpointAlias, "", true, false, true, "")
}

// CaptureOpenAIOAuthIdentityForAlphaSearch retains the endpoint alias while
// marking alpha's unsupported inbound prompt_cache_key as non-applicable. If
// alpha falls back to Responses, resolution emits the mapped session key.
func CaptureOpenAIOAuthIdentityForAlphaSearch(c *gin.Context, body []byte, endpointAlias string) OpenAIOAuthIdentityCapture {
	return captureOpenAIOAuthIdentity(c, body, endpointAlias, "", true, true, false, "")
}

func captureOpenAIOAuthIdentity(c *gin.Context, body []byte, callerSeed, explicitTurnMetadata string, appendEndpointAlias, preferEndpointAlias, promptCacheKeyApplicable bool, forcedRequestKind CodexWireRequestKind) OpenAIOAuthIdentityCapture {
	capture := captureOpenAICodexLogicalTurnIdentity(c, body, callerSeed, explicitTurnMetadata, appendEndpointAlias, preferEndpointAlias)
	capture.WireProfile = captureCodexWireProfile(c, body, explicitTurnMetadata)
	if forcedRequestKind.valid() {
		capture.WireProfile.RequestKind = forcedRequestKind
		capture.WireProfile.resolveTurnIDs(forcedRequestKind)
	}
	capture.PromptCacheKey = captureOpenAICodexPromptCacheKey(
		body, capture.Logical, capture.Aliases, capture.WireProfile, promptCacheKeyApplicable,
	)
	requestTurn, conflicts, invalid := captureOpenAICodexRequestTurn(c, body, explicitTurnMetadata, capture.WireProfile.RequestKind)
	if strings.HasPrefix(capture.WireProfile.InvalidReason, "turn_id ") {
		requestTurn = OpenAICodexRequestTurnSnapshot{}
	}
	capture.RequestTurn = requestTurn
	if capture.WireProfile.InvalidReason == "" {
		if typedID, ok := requestTurn.codexTurnID(capture.WireProfile.RequestKind); ok {
			capture.WireProfile.TurnID = typedID
			if !capture.WireProfile.turnIDPresent {
				capture.WireProfile.turnIDPresent = true
				capture.WireProfile.turnIDCandidates = appendCodexTurnIDCandidate(capture.WireProfile.turnIDCandidates, typedID.Value)
			}
			if !capture.WireProfile.TurnStartedAtSet && requestTurn.StartedAtUnixMS > 0 {
				capture.WireProfile.TurnStartedAtUnixMS = requestTurn.StartedAtUnixMS
				capture.WireProfile.TurnStartedAtSet = true
			}
		}
	}
	capture.ConflictCount += conflicts
	capture.InvalidMetadataCount += invalid
	if conflicts > 0 {
		openAICodexIdentityConflictTotal.Add(int64(conflicts))
	}
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

func captureOpenAICodexRequestTurn(c *gin.Context, body []byte, explicitTurnMetadata string, requestKind CodexWireRequestKind) (OpenAICodexRequestTurnSnapshot, int, int) {
	type candidate struct {
		snapshot OpenAICodexRequestTurnSnapshot
		valid    bool
		invalid  bool
		carrier  openAICodexMetadataCarrier
	}
	candidates := make([]candidate, 0, 8)
	appendMetadata := func(raw []byte, source string, carrier openAICodexMetadataCarrier, requireString bool) {
		snapshot, present, valid := parseOpenAICodexRequestTurnMetadata(raw, source, requireString, requestKind)
		if !present {
			return
		}
		candidates = append(candidates, candidate{snapshot: snapshot, valid: valid, invalid: !valid, carrier: carrier})
	}
	appendFlat := func(rawID, rawStartedAt json.RawMessage, source string, carrier openAICodexMetadataCarrier) {
		snapshot, present, valid := parseOpenAICodexRequestTurnFields(rawID, rawStartedAt, source, requestKind)
		if !present {
			return
		}
		candidates = append(candidates, candidate{snapshot: snapshot, valid: valid, invalid: !valid, carrier: carrier})
	}

	var root map[string]json.RawMessage
	var clientMetadata map[string]json.RawMessage
	if len(body) > 0 && utf8.Valid(body) && json.Unmarshal(body, &root) == nil && root != nil {
		if raw, ok := root["client_metadata"]; ok {
			if json.Unmarshal(raw, &clientMetadata) == nil && clientMetadata != nil {
				if rawMetadata, ok := clientMetadata[openAIWSTurnMetadataHeader]; ok {
					appendMetadata(rawMetadata, openAICodexRequestTurnSourceClientMetadata, openAICodexMetadataCarrierClientTurnMetadata, true)
				}
			} else {
				candidates = append(candidates, candidate{invalid: true, carrier: openAICodexMetadataCarrierClientMetadataContainer})
			}
		}
	}
	if c != nil && c.Request != nil {
		for _, raw := range headerValuesCaseInsensitive(c.Request.Header, openAIWSTurnMetadataHeader) {
			appendMetadata([]byte(raw), openAICodexRequestTurnSourceHeader, openAICodexMetadataCarrierHeaderTurnMetadata, false)
		}
	}
	if raw := strings.TrimSpace(explicitTurnMetadata); raw != "" {
		appendMetadata([]byte(raw), openAICodexRequestTurnSourceWS, openAICodexMetadataCarrierWSTurnMetadata, false)
	}
	if root != nil {
		if raw, ok := root[openAIWSTurnMetadataHeader]; ok {
			appendMetadata(raw, openAICodexRequestTurnSourceBody, openAICodexMetadataCarrierBodyTurnMetadata, false)
		}
	}
	if clientMetadata != nil {
		appendFlat(clientMetadata["turn_id"], clientMetadata["turn_started_at_unix_ms"], openAICodexRequestTurnSourceFlatMetadata, openAICodexMetadataCarrierClientMetadataFlat)
	}
	if root != nil {
		appendFlat(root["turn_id"], root["turn_started_at_unix_ms"], openAICodexRequestTurnSourceCompatBody, openAICodexMetadataCarrierBodyFlat)
	}

	winner := OpenAICodexRequestTurnSnapshot{}
	conflicts := 0
	invalid := 0
	for _, candidate := range candidates {
		if candidate.invalid {
			invalid++
			observeOpenAICodexMetadataInvalid(candidate.carrier)
			continue
		}
		if !candidate.valid {
			continue
		}
		if winner.ID == "" {
			winner = candidate.snapshot
			continue
		}
		if winner.ID != candidate.snapshot.ID {
			conflicts++
			observeOpenAICodexMetadataConflict(candidate.carrier, 1)
		}
	}
	// Memory consolidation deliberately has no request-turn identity. Keep the
	// scan above for invalid/conflict metrics, but do not retain an explicit
	// value or generate a hidden UUID for this request kind.
	if requestKind == CodexWireRequestMemory {
		return OpenAICodexRequestTurnSnapshot{}, conflicts, invalid
	}
	if winner.ID != "" {
		return winner, conflicts, invalid
	}
	generated, err := uuid.NewV7()
	if err != nil {
		return OpenAICodexRequestTurnSnapshot{}, conflicts, invalid
	}
	id := generated.String()
	return OpenAICodexRequestTurnSnapshot{
		ID:              id,
		TypedID:         CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: id},
		StartedAtUnixMS: openAICodexRequestTurnUUIDUnixMilli(generated),
		Source:          openAICodexRequestTurnSourceGenerated,
		Generated:       true,
	}, conflicts, invalid
}

func captureOpenAICodexPromptCacheKey(
	body []byte,
	logical OpenAICodexLogicalTurnIdentity,
	aliases []OpenAICodexLogicalTurnAlias,
	profile CodexWireProfile,
	applicable bool,
) OpenAICodexPromptCacheKeySnapshot {
	snapshot := OpenAICodexPromptCacheKeySnapshot{
		Kind: OpenAICodexPromptCacheKeyMissing, Applicable: applicable,
	}
	if len(body) == 0 || !utf8.Valid(body) {
		return snapshot
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return snapshot
	}
	raw, present := root["prompt_cache_key"]
	if !present {
		return snapshot
	}
	snapshot.Present = true
	var value string
	if json.Unmarshal(raw, &value) != nil {
		snapshot.Kind = OpenAICodexPromptCacheKeyInvalid
		return snapshot
	}
	value = sanitizeSessionID(value)
	if value == "" {
		snapshot.Kind = OpenAICodexPromptCacheKeyInvalid
		return snapshot
	}
	snapshot.Value = value
	snapshot.Valid = true

	subagent := strings.ToLower(strings.TrimSpace(profile.SubagentHeader))
	if (subagent == "review" || subagent == "guardian") && strings.HasPrefix(value, "guardian:") {
		parent := sanitizeSessionID(strings.TrimPrefix(value, "guardian:"))
		if parent != "" && (parent == logical.ParentThreadKey || parent == logical.ForkedFromThreadKey) {
			snapshot.Kind = OpenAICodexPromptCacheKeyGuardian
			return snapshot
		}
	}
	if value == logical.SessionKey || logical.Source == OpenAIOutboundSessionLogicalKeySourcePromptCacheKey {
		snapshot.Kind = OpenAICodexPromptCacheKeyDefault
		return snapshot
	}
	for _, alias := range aliases {
		if value == alias.SessionKey {
			snapshot.Kind = OpenAICodexPromptCacheKeyDefault
			return snapshot
		}
	}
	snapshot.Kind = OpenAICodexPromptCacheKeyOverride
	return snapshot
}

func parseOpenAICodexRequestTurnMetadata(raw []byte, source string, requireString bool, requestKind CodexWireRequestKind) (OpenAICodexRequestTurnSnapshot, bool, bool) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return OpenAICodexRequestTurnSnapshot{}, false, false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return OpenAICodexRequestTurnSnapshot{}, false, false
	}
	if requireString {
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil {
			// The logical metadata parser owns invalid accounting for this
			// carrier, including non-string values.
			return OpenAICodexRequestTurnSnapshot{}, false, false
		}
		trimmed = strings.TrimSpace(encoded)
	} else if strings.HasPrefix(trimmed, `"`) {
		var encoded string
		if json.Unmarshal(raw, &encoded) == nil {
			trimmed = strings.TrimSpace(encoded)
		}
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &metadata) != nil || metadata == nil {
		// Malformed carriers are already counted by logical identity capture.
		return OpenAICodexRequestTurnSnapshot{}, false, false
	}
	return parseOpenAICodexRequestTurnFields(metadata["turn_id"], metadata["turn_started_at_unix_ms"], source, requestKind)
}

func parseOpenAICodexRequestTurnFields(rawID, rawStartedAt json.RawMessage, source string, requestKind CodexWireRequestKind) (OpenAICodexRequestTurnSnapshot, bool, bool) {
	if len(rawID) == 0 {
		return OpenAICodexRequestTurnSnapshot{}, false, false
	}
	var id string
	if json.Unmarshal(rawID, &id) != nil {
		return OpenAICodexRequestTurnSnapshot{}, true, false
	}
	typedID, valid := ResolveCodexTurnID(id, requestKind)
	if !valid {
		return OpenAICodexRequestTurnSnapshot{}, true, false
	}
	startedAt := parseOpenAICodexRequestTurnStartedAt(rawStartedAt)
	if startedAt <= 0 && typedID.Kind == CodexTurnIDUserUUIDv7 {
		parsed, _ := uuid.Parse(typedID.Value)
		startedAt = openAICodexRequestTurnUUIDUnixMilli(parsed)
	}
	return OpenAICodexRequestTurnSnapshot{
		ID: typedID.Value, TypedID: typedID, StartedAtUnixMS: startedAt, Source: source, Explicit: true,
	}, true, true
}

func parseOpenAICodexRequestTurnStartedAt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		if value, err := strconv.ParseInt(string(number), 10, 64); err == nil && value > 0 {
			return value
		}
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		if value, err := strconv.ParseInt(strings.TrimSpace(encoded), 10, 64); err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func openAICodexRequestTurnUUIDUnixMilli(value uuid.UUID) int64 {
	seconds, nanoseconds := value.Time().UnixTime()
	return seconds*int64(time.Second/time.Millisecond) + nanoseconds/int64(time.Millisecond)
}

func openAICodexRequestTurnSnapshotValid(snapshot OpenAICodexRequestTurnSnapshot) bool {
	_, err := canonicalUUIDv7(snapshot.ID)
	return err == nil && snapshot.StartedAtUnixMS > 0
}

func (snapshot OpenAICodexRequestTurnSnapshot) codexTurnID(kind CodexWireRequestKind) (CodexTurnID, bool) {
	if snapshot.TypedID.Value == snapshot.ID && snapshot.TypedID.ValidFor(kind) {
		return snapshot.TypedID, true
	}
	return ResolveCodexTurnID(snapshot.ID, kind)
}

func openAICodexRequestTurnSnapshotValidForWire(snapshot OpenAICodexRequestTurnSnapshot, kind CodexWireRequestKind) bool {
	_, valid := snapshot.codexTurnID(kind)
	return valid
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
	case OpenAIOAuthIdentityProjectionRegular, OpenAIOAuthIdentityProjectionPassthrough,
		OpenAIOAuthIdentityProjectionCompact,
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
		Capture: cloneOpenAIOAuthIdentityCapture(capture), ProjectionMode: options.ProjectionMode,
		RequestTurn:        capture.RequestTurn,
		WireProfile:        cloneCodexWireProfile(capture.WireProfile),
		InstallationPolicy: options.InstallationPolicy,
		PolicySnapshot:     policy,
		APIKeyID:           getAPIKeyIDFromContext(c), ResolveSource: capture.Logical.Source,
		ResolveOutcome:       OpenAIOAuthIdentityResolveNone,
		WindowResolveOutcome: OpenAICodexWindowResolveNone,
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return plan, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	plan.ClientIdentityEnabled = true
	plan.TurnIdentityRequested = options.TurnIdentityEnabled && policy.TurnIdentityNormalizationEnabled()
	if plan.TurnIdentityRequested {
		validationKind := plan.WireProfile.RequestKind
		if !validationKind.valid() {
			validationKind = CodexWireRequestTurn
		}
		plan.WireProfile.resolveTurnIDs(validationKind)
		if err := plan.WireProfile.Validate(); err != nil {
			return plan, err
		}
	}
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
			plan.WireProfile.SessionID = identity.SessionID
			plan.WireProfile.ThreadID = identity.ThreadID
			plan.WireProfile.TurnLineage.ParentThreadID = identity.ParentThreadID
			plan.WireProfile.TurnLineage.ForkedFromThreadID = identity.ForkedFromThreadID
			plan.WireProfile.WindowID = ""
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
		if plan.InstallationEnabled {
			plan.WireProfile.InstallationID = plan.InstallationID
		}
	}
	var err error
	plan.PromptCacheKey, err = s.resolveOpenAICodexPromptCacheKeyPlan(plan)
	if err != nil {
		return plan, err
	}
	plan = s.resolveOpenAICodexWindowForPlan(ctx, plan)
	return plan, nil
}

func openAICodexProjectionCarriesPromptCacheKey(mode OpenAIOAuthIdentityProjectionMode) bool {
	switch mode {
	case OpenAIOAuthIdentityProjectionRegular,
		OpenAIOAuthIdentityProjectionPassthrough,
		OpenAIOAuthIdentityProjectionCompact:
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) resolveOpenAICodexPromptCacheKeyPlan(plan OpenAIOAuthIdentityPlan) (OpenAICodexPromptCacheKeyPlan, error) {
	snapshot := plan.Capture.PromptCacheKey
	result := OpenAICodexPromptCacheKeyPlan{Kind: snapshot.Kind}
	if !openAICodexProjectionCarriesPromptCacheKey(plan.ProjectionMode) ||
		!plan.TurnIdentityRequested || !plan.TurnIdentityEnabled {
		return result, nil
	}
	sessionID := strings.TrimSpace(plan.TurnIdentity.SessionID)
	if sessionID == "" {
		return result, nil
	}
	result.Enabled = true
	if !snapshot.Applicable {
		result.Kind = OpenAICodexPromptCacheKeyDefault
		result.Value = sessionID
		return result, nil
	}
	switch snapshot.Kind {
	case OpenAICodexPromptCacheKeyGuardian:
		if parentThreadID := strings.TrimSpace(plan.TurnIdentity.ParentThreadID); parentThreadID != "" {
			result.Value = "guardian:" + parentThreadID
			return result, nil
		}
		// A guardian-shaped key without a resolved parent cannot preserve the
		// parent-scoped contract, so isolate it as a generic override instead.
		fallthrough
	case OpenAICodexPromptCacheKeyOverride:
		secret := ""
		if s != nil && s.cfg != nil {
			secret = s.cfg.JWT.Secret
		}
		var mapped string
		var err error
		if strings.TrimSpace(secret) == "" {
			mapped, err = openAICodexPromptCacheOverrideFallbackKey(
				plan.CredentialOwnerNamespace, plan.APIKeyID, snapshot.Value,
			)
			if err == nil {
				openAIOutboundSessionIdentityMetrics.promptCacheFallbackTotal.Add(1)
			}
		} else {
			mapped, err = OpenAICodexPromptCacheOverrideKey(
				secret, plan.CredentialOwnerNamespace, plan.APIKeyID, snapshot.Value,
			)
		}
		if err != nil {
			return OpenAICodexPromptCacheKeyPlan{}, fmt.Errorf("map OpenAI Codex prompt_cache_key override: %w", err)
		}
		result.Value = mapped
	default:
		// Missing, invalid, and normal Codex values all converge on the mapped
		// session id. This restores the official session/cache-key invariant.
		result.Value = sessionID
	}
	return result, nil
}

func (s *OpenAIGatewayService) resolveOpenAICodexWindowForPlan(ctx context.Context, plan OpenAIOAuthIdentityPlan) OpenAIOAuthIdentityPlan {
	if !plan.TurnIdentityRequested || !plan.TurnIdentityEnabled ||
		strings.TrimSpace(plan.CredentialOwnerNamespace) == "" ||
		strings.TrimSpace(plan.TurnIdentity.ThreadID) == "" {
		return plan
	}
	secret := ""
	if s != nil && s.cfg != nil {
		secret = s.cfg.JWT.Secret
	}
	mappingKey, err := OpenAICodexWindowMappingKey(
		secret,
		plan.CredentialOwnerNamespace,
		plan.APIKeyID,
		plan.TurnIdentity.ThreadID,
	)
	if err != nil {
		plan.WindowResolveOutcome = OpenAICodexWindowResolveError
		return plan
	}
	snapshot, err := s.ResolveOpenAICodexWindowSnapshot(ctx, mappingKey, plan.TurnIdentity.ThreadID)
	if err != nil {
		plan.WindowResolveOutcome = OpenAICodexWindowResolveError
		return plan
	}
	bound, err := BindOpenAICodexWindowToPlan(plan, snapshot, mappingKey)
	if err != nil {
		plan.WindowResolveOutcome = OpenAICodexWindowResolveError
		return plan
	}
	return bound
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
		plan := cloneOpenAIOAuthIdentityPlan(*pinnedPlan)
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
		left.RequestTurn != right.RequestTurn ||
		left.PromptCacheKey != right.PromptCacheKey ||
		!codexWireProfilesEqual(left.WireProfile, right.WireProfile) ||
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
	if mode == OpenAIOAuthIdentityProjectionPassthrough {
		applyOpenAICodexIdentityHeadersForPlan(headers, plan, false)
		out, err := mergeOpenAIOAuthPassthroughIdentityBody(body, plan)
		if err != nil {
			return body, err
		}
		out, err = applyOpenAICodexPromptCacheKeyBody(out, plan)
		if err != nil {
			return body, err
		}
		if plan.ClientIdentityEnabled {
			applyCodexClientIdentityPlan(headers, plan.ClientIdentity)
		}
		return out, nil
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
	projection := openAICodexMetadataProjectionFromPlan(plan)
	switch mode {
	case OpenAIOAuthIdentityProjectionExistingTurnMetadataOnly:
		if projection.installation && headers != nil {
			deleteOpenAIHeaderEqualFold(headers, codexInstallationIDKey)
			headers.Set(codexInstallationIDKey, projection.installationID)
		}
		applyOpenAICodexCanonicalTurnMetadataHeader(headers, projection, false)
		var err error
		out, err = mergeOpenAICodexIdentityBodyForPlan(out, plan, true)
		if err != nil {
			return body, err
		}
	case OpenAIOAuthIdentityProjectionCompact:
		applyOpenAICodexIdentityHeadersForPlan(headers, plan, true)
	case OpenAIOAuthIdentityProjectionHeadersOnly:
		applyOpenAICodexIdentityHeadersForPlan(headers, plan, false)
	default:
		applyOpenAICodexIdentityHeadersForPlan(headers, plan, false)
		var err error
		out, err = mergeOpenAICodexIdentityBodyForPlan(out, plan, false)
		if err != nil {
			return body, err
		}
	}
	var err error
	out, err = applyOpenAICodexPromptCacheKeyBody(out, plan)
	if err != nil {
		return body, err
	}
	if plan.ClientIdentityEnabled {
		applyCodexClientIdentityPlan(headers, plan.ClientIdentity)
	}
	return out, nil
}

// applyOpenAICodexAlphaSearchIdentityHeader projects the account-owned
// identity into the one carrier used by Codex SearchClient. Alpha keeps its
// native body byte-exact and never emits the independent identity headers.
func applyOpenAICodexAlphaSearchIdentityHeader(headers http.Header, plan OpenAIOAuthIdentityPlan) {
	if headers == nil {
		return
	}
	deleteOpenAICodexIdentityHeaders(headers)
	deleteOpenAIHeaderEqualFold(headers, codexInstallationIDKey)
	// When installation normalization is disabled, an independent inbound
	// installation header must not be promoted into SearchClient turn metadata.
	// A valid installation already present in that metadata remains available
	// to the carrier merge below, preserving the legacy passthrough behavior.
	if !plan.InstallationEnabled {
		plan.WireProfile.InstallationID = ""
	}

	applyOpenAICodexCanonicalTurnMetadataHeader(headers, openAICodexMetadataProjectionFromPlan(plan), true)
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
				if rewritten, err := rewriteOpenAICodexTurnMetadataForCarrier(value, identity, false); err == nil {
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
	capture.WireProfile = cloneCodexWireProfile(capture.WireProfile)
	return capture
}

func cloneOpenAIOAuthIdentityPlan(plan OpenAIOAuthIdentityPlan) OpenAIOAuthIdentityPlan {
	plan.Capture = cloneOpenAIOAuthIdentityCapture(plan.Capture)
	plan.WireProfile = cloneCodexWireProfile(plan.WireProfile)
	return plan
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
		c.Set(openAIOAuthIdentityPlanContextKey, cloneOpenAIOAuthIdentityPlan(plan))
	}
}

func OpenAIOAuthIdentityPlanFromContext(c *gin.Context) (OpenAIOAuthIdentityPlan, bool) {
	if c == nil {
		return OpenAIOAuthIdentityPlan{}, false
	}
	value, ok := c.Get(openAIOAuthIdentityPlanContextKey)
	plan, valid := value.(OpenAIOAuthIdentityPlan)
	return cloneOpenAIOAuthIdentityPlan(plan), ok && valid
}

func ClearOpenAIOAuthIdentityPlan(c *gin.Context) {
	if c != nil {
		c.Set(openAIOAuthIdentityPlanContextKey, nil)
	}
}
