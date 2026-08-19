package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/net/http/httpguts"
)

// CodexWireProfileRevision and CodexWireProfileCommit pin the private Codex
// metadata contract implemented here. They are plan bookkeeping and are never
// serialized onto the wire.
const (
	CodexWireProfileRevision = "responses_metadata"
	CodexWireProfileCommit   = "3929c99a"
)

type CodexTurnIDKind string

const (
	CodexTurnIDUserUUIDv7     CodexTurnIDKind = "user_uuidv7"
	CodexTurnIDOpaqueInternal CodexTurnIDKind = "opaque_internal"
)

// CodexTurnID keeps the source-compatible string ID separate from its wire
// family. Normal turns accept UUIDv7 only; explicitly internal request kinds
// may carry Codex-owned opaque IDs.
type CodexTurnID struct {
	Kind  CodexTurnIDKind
	Value string
}

func (id CodexTurnID) String() string { return id.Value }

func (id CodexTurnID) ValidFor(kind CodexWireRequestKind) bool {
	if id.Value == "" {
		return false
	}
	switch id.Kind {
	case CodexTurnIDUserUUIDv7:
		_, err := canonicalUUIDv7(id.Value)
		return err == nil
	case CodexTurnIDOpaqueInternal:
		return kind.internal() && validCodexOpaqueTurnID(id.Value)
	default:
		return false
	}
}

type CodexWireRequestKind string

const (
	CodexWireRequestTurn       CodexWireRequestKind = "turn"
	CodexWireRequestPrewarm    CodexWireRequestKind = "prewarm"
	CodexWireRequestCompaction CodexWireRequestKind = "compaction"
	CodexWireRequestMemory     CodexWireRequestKind = "memory"
)

func ParseCodexWireRequestKind(value string) (CodexWireRequestKind, bool) {
	kind := CodexWireRequestKind(strings.TrimSpace(value))
	return kind, kind.valid()
}

func (kind CodexWireRequestKind) valid() bool {
	switch kind {
	case CodexWireRequestTurn, CodexWireRequestPrewarm, CodexWireRequestCompaction, CodexWireRequestMemory:
		return true
	default:
		return false
	}
}

func (kind CodexWireRequestKind) internal() bool {
	return kind == CodexWireRequestPrewarm || kind == CodexWireRequestCompaction || kind == CodexWireRequestMemory
}

func (kind CodexWireRequestKind) hasTurnIdentity() bool {
	return kind != CodexWireRequestMemory
}

type CodexTurnLineage struct {
	ForkedFromThreadID string
	ParentThreadID     string
	ParentTurnID       CodexTurnID
	RootTurnID         CodexTurnID
}

type CodexCompactionTurnMetadata struct {
	Trigger        string `json:"trigger"`
	Reason         string `json:"reason"`
	Implementation string `json:"implementation"`
	Phase          string `json:"phase"`
	Strategy       string `json:"strategy"`
}

const (
	CodexCompactionImplementationResponses = "responses"
	CodexCompactionImplementationRemoteV2  = "responses_compaction_v2"
	CodexCompactionImplementationLegacy    = "responses_compact"
	CodexCompactionDefaultTrigger          = "manual"
	CodexCompactionDefaultReason           = "user_requested"
	CodexCompactionDefaultPhase            = "standalone_turn"
	CodexCompactionDefaultStrategy         = "memento"
)

func DefaultCodexCompactionTurnMetadata(implementation string) CodexCompactionTurnMetadata {
	if !validCodexCompactionImplementation(implementation) {
		implementation = CodexCompactionImplementationResponses
	}
	return CodexCompactionTurnMetadata{
		Trigger:        CodexCompactionDefaultTrigger,
		Reason:         CodexCompactionDefaultReason,
		Implementation: implementation,
		Phase:          CodexCompactionDefaultPhase,
		Strategy:       CodexCompactionDefaultStrategy,
	}
}

func (metadata CodexCompactionTurnMetadata) Valid() bool {
	return oneOf(metadata.Trigger, "manual", "auto") &&
		oneOf(metadata.Reason, "user_requested", "context_limit", "model_downshift", "comp_hash_changed") &&
		validCodexCompactionImplementation(metadata.Implementation) &&
		oneOf(metadata.Phase, "standalone_turn", "pre_turn", "mid_turn") &&
		oneOf(metadata.Strategy, "memento", "prefix_compaction")
}

func validCodexCompactionImplementation(value string) bool {
	return oneOf(value,
		CodexCompactionImplementationResponses,
		CodexCompactionImplementationRemoteV2,
		CodexCompactionImplementationLegacy,
	)
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalizeCodexCompactionMetadata(raw json.RawMessage) (json.RawMessage, bool) {
	var object map[string]json.RawMessage
	var metadata CodexCompactionTurnMetadata
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || len(object) != 5 ||
		json.Unmarshal(raw, &metadata) != nil || !metadata.Valid() {
		return nil, false
	}
	for _, field := range []string{"trigger", "reason", "implementation", "phase", "strategy"} {
		if _, present := object[field]; !present {
			return nil, false
		}
	}
	encoded, err := json.Marshal(metadata)
	return encoded, err == nil
}

func marshalCodexCompactionMetadata(metadata CodexCompactionTurnMetadata) json.RawMessage {
	if !metadata.Valid() {
		return nil
	}
	encoded, _ := json.Marshal(metadata)
	return encoded
}

// CodexWireProfile is the immutable request metadata snapshot corresponding to
// codex 3929c99a responses_metadata.rs. Structured inventories stay as JSON so
// accepted client snapshots are not lossy-reencoded before projection.
type CodexWireProfile struct {
	Revision  string
	Commit    string
	Finalized bool
	// RoutingHint and tool namespace policy freeze final model-routing/config
	// decisions alongside the wire snapshot but are not metadata fields.
	RoutingHint               string
	ToolNamespacesAllowed     bool
	ToolNamespacesInfoAllowed bool

	InstallationID string
	SessionID      string
	ThreadID       string
	WindowID       string
	RequestKind    CodexWireRequestKind
	Compaction     json.RawMessage

	TurnID              CodexTurnID
	TurnStartedAtUnixMS int64
	TurnStartedAtSet    bool
	TurnLineage         CodexTurnLineage

	AgentName                  string
	ThreadSource               string
	Sandbox                    string
	SandboxMode                string
	AutoReviewEnabled          *bool
	NodeREPLAutoReviewRequired *bool
	NodeREPLDisabled           *bool
	SubagentHeader             string
	SubagentKind               string
	Workspaces                 json.RawMessage
	ToolNamespacesInfo         json.RawMessage
	ExtraMetadata              map[string]string
	InvalidReason              string

	turnIDCandidates       []string
	parentTurnIDCandidates []string
	rootTurnIDCandidates   []string
	turnIDPresent          bool
	parentTurnIDPresent    bool
	rootTurnIDPresent      bool
	turnIDMalformed        bool
	parentTurnIDMalformed  bool
	rootTurnIDMalformed    bool
}

var (
	ErrInvalidOpenAICodexWireProfile  = errors.New("invalid OpenAI Codex wire profile")
	ErrOpenAICodexRequestKindConflict = errors.New("OpenAI Codex request kind conflicts with the physical request shape")
)

func resolveOpenAICodexWireRequestKind(captured, fallback, forced CodexWireRequestKind) (CodexWireRequestKind, error) {
	if captured == CodexWireRequestMemory && forced.valid() && forced != CodexWireRequestMemory {
		return "", fmt.Errorf("%w: request_kind memory cannot be used for %s", ErrOpenAICodexRequestKindConflict, forced)
	}
	if forced.valid() {
		return forced, nil
	}
	if captured.valid() {
		return captured, nil
	}
	if fallback.valid() {
		return fallback, nil
	}
	return CodexWireRequestTurn, nil
}

func (profile CodexWireProfile) Validate() error {
	if profile.InvalidReason == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidOpenAICodexWireProfile, profile.InvalidReason)
}

func newCodexWireProfile() CodexWireProfile {
	return CodexWireProfile{Revision: CodexWireProfileRevision, Commit: CodexWireProfileCommit}
}

func validCodexOpaqueTurnID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func ResolveCodexTurnID(value string, kind CodexWireRequestKind) (CodexTurnID, bool) {
	value = strings.TrimSpace(value)
	if canonical, err := canonicalUUIDv7(value); err == nil {
		return CodexTurnID{Kind: CodexTurnIDUserUUIDv7, Value: canonical}, true
	}
	if kind.internal() && validCodexOpaqueTurnID(value) {
		return CodexTurnID{Kind: CodexTurnIDOpaqueInternal, Value: value}, true
	}
	return CodexTurnID{}, false
}

func parseCodexTurnID(value string, kind CodexWireRequestKind) (CodexTurnID, bool) {
	return ResolveCodexTurnID(value, kind)
}

var codexWireReservedMetadataKeys = map[string]struct{}{
	"installation_id": {}, "x-codex-installation-id": {}, "session_id": {}, "thread_id": {}, "agent_name": {},
	"turn_id": {}, "window_id": {}, "x-codex-window-id": {}, "x-codex-turn-metadata": {}, "x-codex-parent-thread-id": {},
	"x-openai-subagent": {}, "request_kind": {}, "compaction": {}, "code_mode_tool_names": {}, "tool_namespaces_info": {},
	"turn_started_at_unix_ms": {}, "forked_from_thread_id": {}, "parent_thread_id": {}, "parent_turn_id": {}, "root_turn_id": {},
	"subagent_kind": {}, "thread_source": {}, "sandbox": {}, "sandbox_mode": {}, "auto_review_enabled": {},
	"node_repl_auto_review_required": {}, "node_repl_disabled": {}, "workspaces": {},
}

func validCodexExtraMetadata(key, value string) bool {
	if len(key) == 0 || len(key) > 64 || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	if _, reserved := codexWireReservedMetadataKeys[key]; reserved {
		return false
	}
	for index := 0; index < len(key); index++ {
		char := key[index]
		if index == 0 {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')) {
				return false
			}
			continue
		}
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '.' || char == '-') {
			return false
		}
	}
	return true
}

func rawJSONObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func codexWireString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return ""
	}
	return value
}

func codexWireBool(raw json.RawMessage) *bool {
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return boolPointer(value)
}

func codexWireInt64(raw json.RawMessage) (int64, bool) {
	var value int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func boolPointer(value bool) *bool {
	copy := value
	return &copy
}

func appendCodexTurnIDCandidate(candidates []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return candidates
	}
	for _, existing := range candidates {
		if existing == value {
			return candidates
		}
	}
	return append(candidates, value)
}

func firstResolvedCodexTurnID(candidates []string, kind CodexWireRequestKind) CodexTurnID {
	for _, value := range candidates {
		if id, ok := ResolveCodexTurnID(value, kind); ok {
			return id
		}
	}
	return CodexTurnID{}
}

func (profile *CodexWireProfile) resolveTurnIDs(kind CodexWireRequestKind) {
	if profile == nil {
		return
	}
	profile.InvalidReason = ""
	profile.TurnID = firstResolvedCodexTurnID(profile.turnIDCandidates, kind)
	profile.TurnLineage.ParentTurnID = firstResolvedCodexTurnID(profile.parentTurnIDCandidates, kind)
	profile.TurnLineage.RootTurnID = firstResolvedCodexTurnID(profile.rootTurnIDCandidates, kind)
	if !kind.valid() {
		return
	}
	for _, candidate := range []struct {
		present   bool
		malformed bool
		valid     bool
		field     string
	}{
		{profile.turnIDPresent, profile.turnIDMalformed, profile.TurnID.ValidFor(kind), "turn_id"},
		{profile.parentTurnIDPresent, profile.parentTurnIDMalformed, profile.TurnLineage.ParentTurnID.ValidFor(kind), "parent_turn_id"},
		{profile.rootTurnIDPresent, profile.rootTurnIDMalformed, profile.TurnLineage.RootTurnID.ValidFor(kind), "root_turn_id"},
	} {
		if candidate.present && (candidate.malformed || !candidate.valid) {
			profile.InvalidReason = candidate.field + " is invalid for request_kind " + string(kind)
			return
		}
	}
}

func readCodexTurnIDCandidate(metadata map[string]json.RawMessage, key string) (string, bool, bool) {
	raw, present := metadata[key]
	if !present {
		return "", false, false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", true, true
	}
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", true, true
	}
	return value, true, false
}

// ParseCodexWireProfile parses one canonical nested turn-metadata object. It
// never fills generated defaults; those are applied only after the final model
// and request kind are known.
func ParseCodexWireProfile(raw string) CodexWireProfile {
	profile := newCodexWireProfile()
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &metadata) != nil || metadata == nil {
		return profile
	}
	if kind, ok := ParseCodexWireRequestKind(codexWireString(metadata["request_kind"])); ok {
		profile.RequestKind = kind
	}
	profile.InstallationID = codexWireString(metadata["installation_id"])
	profile.SessionID = codexWireString(metadata["session_id"])
	profile.ThreadID = codexWireString(metadata["thread_id"])
	profile.AgentName = codexWireString(metadata["agent_name"])
	profile.WindowID = codexWireString(metadata["window_id"])
	profile.ThreadSource = codexWireString(metadata["thread_source"])
	profile.Sandbox = codexWireString(metadata["sandbox"])
	profile.SandboxMode = codexWireString(metadata["sandbox_mode"])
	profile.SubagentKind = codexWireString(metadata["subagent_kind"])
	profile.TurnStartedAtUnixMS, profile.TurnStartedAtSet = codexWireInt64(metadata["turn_started_at_unix_ms"])
	profile.AutoReviewEnabled = codexWireBool(metadata["auto_review_enabled"])
	profile.NodeREPLAutoReviewRequired = codexWireBool(metadata["node_repl_auto_review_required"])
	profile.NodeREPLDisabled = codexWireBool(metadata["node_repl_disabled"])
	profile.Compaction = rawJSONObject(metadata["compaction"])
	profile.Workspaces = rawJSONObject(metadata["workspaces"])
	profile.ToolNamespacesInfo = rawJSONObject(metadata["tool_namespaces_info"])
	profile.TurnLineage.ForkedFromThreadID = codexWireString(metadata["forked_from_thread_id"])
	profile.TurnLineage.ParentThreadID = codexWireString(metadata["parent_thread_id"])
	if value, present, malformed := readCodexTurnIDCandidate(metadata, "turn_id"); present {
		profile.turnIDPresent, profile.turnIDMalformed = true, malformed
		profile.turnIDCandidates = appendCodexTurnIDCandidate(profile.turnIDCandidates, value)
	}
	if value, present, malformed := readCodexTurnIDCandidate(metadata, "parent_turn_id"); present {
		profile.parentTurnIDPresent, profile.parentTurnIDMalformed = true, malformed
		profile.parentTurnIDCandidates = appendCodexTurnIDCandidate(profile.parentTurnIDCandidates, value)
	}
	if value, present, malformed := readCodexTurnIDCandidate(metadata, "root_turn_id"); present {
		profile.rootTurnIDPresent, profile.rootTurnIDMalformed = true, malformed
		profile.rootTurnIDCandidates = appendCodexTurnIDCandidate(profile.rootTurnIDCandidates, value)
	}
	profile.resolveTurnIDs(profile.RequestKind)

	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(profile.ExtraMetadata) >= 16 {
			break
		}
		if _, reserved := codexWireReservedMetadataKeys[key]; reserved {
			continue
		}
		var value string
		if json.Unmarshal(metadata[key], &value) != nil || !validCodexExtraMetadata(key, value) {
			continue
		}
		if profile.ExtraMetadata == nil {
			profile.ExtraMetadata = make(map[string]string)
		}
		profile.ExtraMetadata[key] = value
	}
	return profile
}

func cloneCodexWireProfile(profile CodexWireProfile) CodexWireProfile {
	profile.Compaction = append(json.RawMessage(nil), profile.Compaction...)
	profile.Workspaces = append(json.RawMessage(nil), profile.Workspaces...)
	profile.ToolNamespacesInfo = append(json.RawMessage(nil), profile.ToolNamespacesInfo...)
	profile.turnIDCandidates = append([]string(nil), profile.turnIDCandidates...)
	profile.parentTurnIDCandidates = append([]string(nil), profile.parentTurnIDCandidates...)
	profile.rootTurnIDCandidates = append([]string(nil), profile.rootTurnIDCandidates...)
	if profile.AutoReviewEnabled != nil {
		profile.AutoReviewEnabled = boolPointer(*profile.AutoReviewEnabled)
	}
	if profile.NodeREPLAutoReviewRequired != nil {
		profile.NodeREPLAutoReviewRequired = boolPointer(*profile.NodeREPLAutoReviewRequired)
	}
	if profile.NodeREPLDisabled != nil {
		profile.NodeREPLDisabled = boolPointer(*profile.NodeREPLDisabled)
	}
	if profile.ExtraMetadata != nil {
		profile.ExtraMetadata = cloneCodexWireStringMap(profile.ExtraMetadata)
	}
	return profile
}

func codexWireProfilesEqual(left, right CodexWireProfile) bool {
	return reflect.DeepEqual(left, right)
}

func cloneCodexWireStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func appendUniqueCodexTurnCandidates(target []string, source []string) []string {
	for _, value := range source {
		target = appendCodexTurnIDCandidate(target, value)
	}
	return target
}

// mergeCodexWireProfileMissing applies a lower-priority carrier without
// replacing fields already captured from a canonical carrier.
func mergeCodexWireProfileMissing(target *CodexWireProfile, source CodexWireProfile) {
	if target == nil {
		return
	}
	if !target.RequestKind.valid() && source.RequestKind.valid() {
		target.RequestKind = source.RequestKind
	}
	for destination, candidate := range map[*string]string{
		&target.InstallationID:                 source.InstallationID,
		&target.SessionID:                      source.SessionID,
		&target.ThreadID:                       source.ThreadID,
		&target.WindowID:                       source.WindowID,
		&target.AgentName:                      source.AgentName,
		&target.ThreadSource:                   source.ThreadSource,
		&target.Sandbox:                        source.Sandbox,
		&target.SandboxMode:                    source.SandboxMode,
		&target.SubagentHeader:                 source.SubagentHeader,
		&target.SubagentKind:                   source.SubagentKind,
		&target.TurnLineage.ForkedFromThreadID: source.TurnLineage.ForkedFromThreadID,
		&target.TurnLineage.ParentThreadID:     source.TurnLineage.ParentThreadID,
	} {
		if *destination == "" && candidate != "" {
			*destination = candidate
		}
	}
	if !target.TurnStartedAtSet && source.TurnStartedAtSet {
		target.TurnStartedAtUnixMS, target.TurnStartedAtSet = source.TurnStartedAtUnixMS, true
	}
	if target.AutoReviewEnabled == nil && source.AutoReviewEnabled != nil {
		target.AutoReviewEnabled = boolPointer(*source.AutoReviewEnabled)
	}
	if target.NodeREPLAutoReviewRequired == nil && source.NodeREPLAutoReviewRequired != nil {
		target.NodeREPLAutoReviewRequired = boolPointer(*source.NodeREPLAutoReviewRequired)
	}
	if target.NodeREPLDisabled == nil && source.NodeREPLDisabled != nil {
		target.NodeREPLDisabled = boolPointer(*source.NodeREPLDisabled)
	}
	if len(target.Compaction) == 0 && len(source.Compaction) > 0 {
		target.Compaction = append(json.RawMessage(nil), source.Compaction...)
	}
	if len(target.Workspaces) == 0 && len(source.Workspaces) > 0 {
		target.Workspaces = append(json.RawMessage(nil), source.Workspaces...)
	}
	if len(target.ToolNamespacesInfo) == 0 && len(source.ToolNamespacesInfo) > 0 {
		target.ToolNamespacesInfo = append(json.RawMessage(nil), source.ToolNamespacesInfo...)
	}
	if !target.turnIDPresent && source.turnIDPresent {
		target.turnIDPresent, target.turnIDMalformed = true, source.turnIDMalformed
		target.turnIDCandidates = append([]string(nil), source.turnIDCandidates...)
	}
	if !target.parentTurnIDPresent && source.parentTurnIDPresent {
		target.parentTurnIDPresent, target.parentTurnIDMalformed = true, source.parentTurnIDMalformed
		target.parentTurnIDCandidates = append([]string(nil), source.parentTurnIDCandidates...)
	}
	if !target.rootTurnIDPresent && source.rootTurnIDPresent {
		target.rootTurnIDPresent, target.rootTurnIDMalformed = true, source.rootTurnIDMalformed
		target.rootTurnIDCandidates = append([]string(nil), source.rootTurnIDCandidates...)
	}
	mergeCodexWireExtras(target, source.ExtraMetadata, false)
	target.resolveTurnIDs(target.RequestKind)
}

func mergeCodexWireExtras(profile *CodexWireProfile, source map[string]string, overwrite bool) {
	if profile == nil || len(source) == 0 {
		return
	}
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := source[key]
		if !validCodexExtraMetadata(key, value) {
			continue
		}
		if profile.ExtraMetadata == nil {
			profile.ExtraMetadata = make(map[string]string)
		}
		if _, exists := profile.ExtraMetadata[key]; exists && !overwrite {
			continue
		}
		if _, exists := profile.ExtraMetadata[key]; !exists && len(profile.ExtraMetadata) >= 16 {
			continue
		}
		profile.ExtraMetadata[key] = value
	}
}

func parseCodexWireNestedCarrier(raw json.RawMessage, requireJSONString bool) (CodexWireProfile, bool) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return CodexWireProfile{}, false
	}
	value := strings.TrimSpace(string(raw))
	if requireJSONString || strings.HasPrefix(value, `"`) {
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil {
			return CodexWireProfile{}, false
		}
		value = strings.TrimSpace(encoded)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &object) != nil || object == nil {
		return CodexWireProfile{}, false
	}
	return ParseCodexWireProfile(value), true
}

func parseCodexWireFlatMetadata(metadata map[string]json.RawMessage) CodexWireProfile {
	profile := newCodexWireProfile()
	if metadata == nil {
		return profile
	}
	profile.InstallationID = firstValidOpenAICodexJSONField(metadata, "x-codex-installation-id", "installation_id")
	profile.SessionID = firstValidOpenAICodexJSONField(metadata, "session_id", "session-id")
	profile.ThreadID = firstValidOpenAICodexJSONField(metadata, "thread_id", "thread-id")
	profile.WindowID = firstValidOpenAICodexJSONField(metadata, "x-codex-window-id", "window_id")
	profile.SubagentHeader = firstValidOpenAICodexJSONField(metadata, "x-openai-subagent")
	profile.TurnLineage.ParentThreadID = firstValidOpenAICodexJSONField(metadata, "x-codex-parent-thread-id", "parent_thread_id")
	if raw, present := firstPresentOpenAICodexJSONField(metadata, "turn_id", "turn-id"); present {
		value, _, malformed := readCodexTurnIDCandidate(map[string]json.RawMessage{"value": raw}, "value")
		profile.turnIDPresent, profile.turnIDMalformed = true, malformed
		profile.turnIDCandidates = appendCodexTurnIDCandidate(profile.turnIDCandidates, value)
	}
	if raw, present := firstPresentOpenAICodexJSONField(metadata, "parent_turn_id"); present {
		value, _, malformed := readCodexTurnIDCandidate(map[string]json.RawMessage{"value": raw}, "value")
		profile.parentTurnIDPresent, profile.parentTurnIDMalformed = true, malformed
		profile.parentTurnIDCandidates = appendCodexTurnIDCandidate(profile.parentTurnIDCandidates, value)
	}
	if raw, present := firstPresentOpenAICodexJSONField(metadata, "root_turn_id"); present {
		value, _, malformed := readCodexTurnIDCandidate(map[string]json.RawMessage{"value": raw}, "value")
		profile.rootTurnIDPresent, profile.rootTurnIDMalformed = true, malformed
		profile.rootTurnIDCandidates = appendCodexTurnIDCandidate(profile.rootTurnIDCandidates, value)
	}
	if value, present := codexWireInt64(metadata["turn_started_at_unix_ms"]); present {
		profile.TurnStartedAtUnixMS, profile.TurnStartedAtSet = value, true
	}
	profile.resolveTurnIDs(profile.RequestKind)
	return profile
}

func firstPresentOpenAICodexJSONField(metadata map[string]json.RawMessage, fields ...string) (json.RawMessage, bool) {
	for _, field := range fields {
		if raw, present := metadata[field]; present {
			return raw, true
		}
	}
	return nil, false
}

func parseCodexWireHeaderProfile(c *gin.Context) CodexWireProfile {
	profile := newCodexWireProfile()
	if c == nil || c.Request == nil {
		return profile
	}
	headers := c.Request.Header
	profile.InstallationID = firstValidOpenAIOutboundSessionHeader(headers, []string{codexInstallationIDKey})
	profile.SessionID = firstValidOpenAIOutboundSessionHeader(headers, []string{"session-id", "session_id"})
	profile.ThreadID = firstValidOpenAIOutboundSessionHeader(headers, []string{"thread-id", "thread_id"})
	profile.WindowID = firstValidOpenAIOutboundSessionHeader(headers, []string{"x-codex-window-id"})
	profile.SubagentHeader = firstValidOpenAIOutboundSessionHeader(headers, []string{"x-openai-subagent"})
	profile.TurnLineage.ParentThreadID = firstValidOpenAIOutboundSessionHeader(headers, []string{"x-codex-parent-thread-id"})
	return profile
}

func captureCodexWireProfile(c *gin.Context, body []byte, explicitTurnMetadata string) CodexWireProfile {
	profile := newCodexWireProfile()
	var root map[string]json.RawMessage
	var clientMetadata map[string]json.RawMessage
	if len(body) > 0 && utf8.Valid(body) && json.Unmarshal(body, &root) == nil && root != nil {
		if raw, present := root["client_metadata"]; present {
			_ = json.Unmarshal(raw, &clientMetadata)
		}
	}
	if raw, present := clientMetadata[openAIWSTurnMetadataHeader]; present {
		if candidate, valid := parseCodexWireNestedCarrier(raw, true); valid {
			mergeCodexWireProfileMissing(&profile, candidate)
		}
	}
	if c != nil && c.Request != nil {
		for _, raw := range headerValuesCaseInsensitive(c.Request.Header, openAIWSTurnMetadataHeader) {
			if candidate, valid := parseCodexWireNestedCarrier(json.RawMessage(raw), false); valid {
				mergeCodexWireProfileMissing(&profile, candidate)
			}
		}
	}
	if raw := strings.TrimSpace(explicitTurnMetadata); raw != "" {
		if candidate, valid := parseCodexWireNestedCarrier(json.RawMessage(raw), false); valid {
			mergeCodexWireProfileMissing(&profile, candidate)
		}
	}
	if raw, present := root[openAIWSTurnMetadataHeader]; present {
		if candidate, valid := parseCodexWireNestedCarrier(raw, false); valid {
			mergeCodexWireProfileMissing(&profile, candidate)
		}
	}
	mergeCodexWireProfileMissing(&profile, parseCodexWireHeaderProfile(c))
	mergeCodexWireProfileMissing(&profile, parseCodexWireFlatMetadata(clientMetadata))
	mergeCodexWireProfileMissing(&profile, parseCodexWireFlatMetadata(root))
	validationKind := profile.RequestKind
	if !validationKind.valid() {
		// Missing request_kind is the compatibility spelling of a normal turn.
		// Validate against that eventual default without mutating the parsed
		// snapshot or teaching the parser to synthesize values.
		validationKind = CodexWireRequestTurn
	}
	profile.resolveTurnIDs(validationKind)
	return profile
}

func (profile CodexWireProfile) withDefaults(metadataProfile CodexMetadataProfile) CodexWireProfile {
	profile = cloneCodexWireProfile(profile)
	metadataProfile = metadataProfile.normalized()
	if profile.Revision == "" {
		profile.Revision = CodexWireProfileRevision
	}
	if profile.Commit == "" {
		profile.Commit = CodexWireProfileCommit
	}
	if !profile.RequestKind.valid() {
		profile.RequestKind = CodexWireRequestTurn
	}
	profile.resolveTurnIDs(profile.RequestKind)
	if profile.RequestKind != CodexWireRequestMemory && profile.AgentName == "" {
		profile.AgentName = metadataProfile.AgentName
	}
	if profile.Sandbox == "" {
		profile.Sandbox = metadataProfile.Sandbox
	}
	if profile.SandboxMode == "" {
		profile.SandboxMode = metadataProfile.SandboxMode
	}
	if profile.RequestKind != CodexWireRequestMemory && profile.AutoReviewEnabled == nil {
		profile.AutoReviewEnabled = boolPointer(metadataProfile.AutoReviewEnabled)
	}
	if profile.RequestKind != CodexWireRequestMemory && profile.NodeREPLAutoReviewRequired == nil {
		profile.NodeREPLAutoReviewRequired = boolPointer(false)
	}
	if profile.RequestKind != CodexWireRequestMemory && profile.NodeREPLDisabled == nil {
		profile.NodeREPLDisabled = boolPointer(false)
	}
	if profile.RequestKind != CodexWireRequestCompaction {
		profile.Compaction = nil
	}
	profile.Finalized = true
	return profile
}

func (profile CodexWireProfile) nestedObject(includeToolNamespaces bool) map[string]any {
	metadata := make(map[string]any, 24+len(profile.ExtraMetadata))
	kindKnown := profile.RequestKind.valid()
	hasTurnIdentity := !kindKnown || profile.RequestKind.hasTurnIdentity()
	hasRequestIdentity := kindKnown && profile.RequestKind.hasTurnIdentity()
	compatRequestIdentity := !profile.Finalized && !kindKnown
	if hasRequestIdentity || compatRequestIdentity {
		putNonEmptyString(metadata, "installation_id", profile.InstallationID)
		putNonEmptyString(metadata, "window_id", profile.WindowID)
	}
	if hasTurnIdentity {
		putNonEmptyString(metadata, "session_id", profile.SessionID)
		putNonEmptyString(metadata, "thread_id", profile.ThreadID)
		putNonEmptyString(metadata, "agent_name", profile.AgentName)
		if profile.TurnID.ValidFor(profile.RequestKind) || (!kindKnown && profile.TurnID.Kind == CodexTurnIDUserUUIDv7) {
			metadata["turn_id"] = profile.TurnID.Value
		}
	}
	if kindKnown {
		metadata["request_kind"] = string(profile.RequestKind)
	}
	putNonEmptyString(metadata, "forked_from_thread_id", profile.TurnLineage.ForkedFromThreadID)
	putNonEmptyString(metadata, "parent_thread_id", profile.TurnLineage.ParentThreadID)
	if profile.TurnLineage.ParentTurnID.ValidFor(profile.RequestKind) {
		metadata["parent_turn_id"] = profile.TurnLineage.ParentTurnID.Value
	}
	if profile.TurnLineage.RootTurnID.ValidFor(profile.RequestKind) {
		metadata["root_turn_id"] = profile.TurnLineage.RootTurnID.Value
	}
	putNonEmptyString(metadata, "subagent_kind", profile.SubagentKind)
	putNonEmptyString(metadata, "thread_source", profile.ThreadSource)
	putNonEmptyString(metadata, "sandbox", profile.Sandbox)
	putNonEmptyString(metadata, "sandbox_mode", profile.SandboxMode)
	if profile.AutoReviewEnabled != nil {
		metadata["auto_review_enabled"] = *profile.AutoReviewEnabled
	}
	if profile.NodeREPLAutoReviewRequired != nil {
		metadata["node_repl_auto_review_required"] = *profile.NodeREPLAutoReviewRequired
	}
	if profile.NodeREPLDisabled != nil {
		metadata["node_repl_disabled"] = *profile.NodeREPLDisabled
	}
	if object := rawJSONObject(profile.Workspaces); len(object) > 0 && string(object) != "{}" {
		metadata["workspaces"] = object
	}
	if includeToolNamespaces && profile.ToolNamespacesInfoAllowed {
		if object := rawJSONObject(profile.ToolNamespacesInfo); len(object) > 0 && string(object) != "{}" {
			metadata["tool_namespaces_info"] = object
		}
	}
	if profile.TurnStartedAtSet {
		metadata["turn_started_at_unix_ms"] = profile.TurnStartedAtUnixMS
	}
	if profile.RequestKind == CodexWireRequestCompaction {
		if object := rawJSONObject(profile.Compaction); len(object) > 0 {
			metadata["compaction"] = object
		}
	}
	keys := make([]string, 0, len(profile.ExtraMetadata))
	for key := range profile.ExtraMetadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := profile.ExtraMetadata[key]
		if validCodexExtraMetadata(key, value) {
			metadata[key] = value
		}
	}
	return metadata
}

func putNonEmptyString(target map[string]any, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		target[key] = value
	}
}

// MarshalNestedJSON emits the canonical ASCII-safe metadata string. The body
// form may include tool_namespaces_info; compatibility headers must not.
func (profile CodexWireProfile) MarshalNestedJSON(includeToolNamespaces bool) (string, error) {
	metadata := profile.nestedObject(includeToolNamespaces)
	orderedKeys := []string{
		"installation_id",
		"session_id",
		"thread_id",
		"agent_name",
		"turn_id",
		"window_id",
		"request_kind",
		"forked_from_thread_id",
		"parent_thread_id",
		"parent_turn_id",
		"root_turn_id",
		"subagent_kind",
		"thread_source",
		"sandbox",
		"sandbox_mode",
		"auto_review_enabled",
		"node_repl_auto_review_required",
		"node_repl_disabled",
		"workspaces",
		"tool_namespaces_info",
		"turn_started_at_unix_ms",
		"compaction",
	}
	extraKeys := make([]string, 0, len(profile.ExtraMetadata))
	for key := range profile.ExtraMetadata {
		if _, present := metadata[key]; present {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Strings(extraKeys)
	orderedKeys = append(orderedKeys, extraKeys...)

	var encoded strings.Builder
	encoded.WriteByte('{')
	written := 0
	for _, key := range orderedKeys {
		value, present := metadata[key]
		if !present {
			continue
		}
		keyJSON, err := marshalJSONWithoutHTMLEscape(key)
		if err != nil {
			return "", err
		}
		valueJSON, err := marshalJSONWithoutHTMLEscape(value)
		if err != nil {
			return "", err
		}
		if written > 0 {
			encoded.WriteByte(',')
		}
		encoded.Write(keyJSON)
		encoded.WriteByte(':')
		encoded.Write(valueJSON)
		written++
	}
	encoded.WriteByte('}')
	return escapeNonASCIIJSON([]byte(encoded.String())), nil
}

func codexUUIDv7Time(value string) int64 {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return 0
	}
	return openAICodexRequestTurnUUIDUnixMilli(parsed)
}

// BindOpenAICodexWindowToPlan returns a copied plan with a validated,
// server-owned window snapshot. The mapping key is retained for the response
// finalizer's compaction commit and is never projected upstream.
func BindOpenAICodexWindowToPlan(plan OpenAIOAuthIdentityPlan, snapshot OpenAICodexWindowSnapshot, mappingKey string) (OpenAIOAuthIdentityPlan, error) {
	plan = cloneOpenAIOAuthIdentityPlan(plan)
	if !plan.TurnIdentityEnabled {
		return plan, errors.New("bind OpenAI Codex window: turn identity is not enabled")
	}
	if err := ValidateOpenAICodexWindowSnapshot(snapshot); err != nil {
		return plan, fmt.Errorf("bind OpenAI Codex window: %w", err)
	}
	if snapshot.ThreadID != plan.TurnIdentity.ThreadID {
		return plan, errors.New("bind OpenAI Codex window: snapshot thread does not match plan")
	}
	if !validOpenAICodexWindowMappingKey(mappingKey) {
		return plan, errors.New("bind OpenAI Codex window: mapping key is invalid")
	}
	plan.Window = snapshot
	plan.WindowMappingKey = mappingKey
	plan.WindowResolveOutcome = OpenAICodexWindowResolveResolved
	plan.WireProfile.WindowID = snapshot.WindowID()
	return plan, nil
}

type FinalizeOpenAICodexWirePlanOptions struct {
	RequestKind       string
	ModelCapabilities CodexModelCapabilities
	MetadataProfile   CodexMetadataProfile
	Compaction        *CodexCompactionTurnMetadata
	FinalModel        string
	FinalServiceTier  string
}

// BuildOpenAICodexRoutingHint derives the gateway-owned routing value from the
// final upstream model and effective service tier. It performs no account or
// request lookup, so callers can freeze the result in an immutable plan.
func BuildOpenAICodexRoutingHint(model, serviceTier string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.ContainsAny(model, ";=\x00\r\n") {
		return ""
	}
	tier := normalizedOpenAIServiceTierValue(serviceTier)
	switch tier {
	case OpenAIFastTierPriority, OpenAIFastTierFlex:
	default:
		tier = ""
	}
	hint := "model=" + model
	if tier != "" {
		hint += ";tier=" + tier
	}
	if !httpguts.ValidHeaderFieldValue(hint) {
		return ""
	}
	return hint
}

func defaultCodexCompactionImplementation(mode OpenAIOAuthIdentityProjectionMode) string {
	if mode == OpenAIOAuthIdentityProjectionCompact {
		return CodexCompactionImplementationLegacy
	}
	return CodexCompactionImplementationRemoteV2
}

// FinalizeOpenAICodexWirePlan freezes values that are known only after model
// mapping and request-kind selection. It is pure: it reads no account, setting,
// context, clock, or store and returns a deep-copied plan.
func FinalizeOpenAICodexWirePlan(plan OpenAIOAuthIdentityPlan, requestKind string, modelCapabilities CodexModelCapabilities) (OpenAIOAuthIdentityPlan, error) {
	return FinalizeOpenAICodexWirePlanWithOptions(plan, FinalizeOpenAICodexWirePlanOptions{
		RequestKind: requestKind, ModelCapabilities: modelCapabilities,
	})
}

func FinalizeOpenAICodexWirePlanWithOptions(plan OpenAIOAuthIdentityPlan, options FinalizeOpenAICodexWirePlanOptions) (OpenAIOAuthIdentityPlan, error) {
	plan = cloneOpenAIOAuthIdentityPlan(plan)
	if strings.TrimSpace(options.FinalModel) != "" || strings.TrimSpace(options.FinalServiceTier) != "" {
		plan.WireProfile.RoutingHint = BuildOpenAICodexRoutingHint(options.FinalModel, options.FinalServiceTier)
	}
	if !plan.TurnIdentityRequested {
		return plan, nil
	}

	profile := cloneCodexWireProfile(plan.WireProfile)
	kind, suppliedKindValid := ParseCodexWireRequestKind(options.RequestKind)
	if strings.TrimSpace(options.RequestKind) != "" && !suppliedKindValid {
		return plan, fmt.Errorf("%w: request_kind is unsupported", ErrInvalidOpenAICodexWireProfile)
	}
	if !suppliedKindValid {
		kind = profile.RequestKind
	}
	if !kind.valid() {
		kind = CodexWireRequestTurn
	}
	profile.RequestKind = kind
	profile.resolveTurnIDs(kind)
	if kind == CodexWireRequestMemory {
		clearOpenAICodexMemoryTurnIdentity(&plan, &profile)
	}
	if err := profile.Validate(); err != nil {
		return plan, err
	}

	if profile.TurnID.ValidFor(kind) {
		if profile.TurnID.Value != plan.RequestTurn.ID {
			plan.RequestTurn.ID = profile.TurnID.Value
			plan.RequestTurn.TypedID = profile.TurnID
			plan.RequestTurn.Explicit = true
			plan.RequestTurn.Generated = false
			if profile.TurnStartedAtSet {
				plan.RequestTurn.StartedAtUnixMS = profile.TurnStartedAtUnixMS
			} else if profile.TurnID.Kind == CodexTurnIDUserUUIDv7 {
				plan.RequestTurn.StartedAtUnixMS = codexUUIDv7Time(profile.TurnID.Value)
			} else {
				plan.RequestTurn.StartedAtUnixMS = 0
			}
		}
	} else if turnID, valid := plan.RequestTurn.codexTurnID(kind); valid {
		profile.TurnID = turnID
		profile.turnIDPresent = true
		profile.turnIDCandidates = appendCodexTurnIDCandidate(profile.turnIDCandidates, turnID.Value)
	}
	if !profile.TurnStartedAtSet && plan.RequestTurn.StartedAtUnixMS > 0 {
		profile.TurnStartedAtUnixMS = plan.RequestTurn.StartedAtUnixMS
		profile.TurnStartedAtSet = true
	}
	if kind == CodexWireRequestPrewarm {
		// Prewarm establishes transport affinity but is not a dispatched turn.
		// Keep RequestTurn on the compatible plan lifecycle while omitting its
		// request-instance and turn-lineage fields from the wire snapshot.
		profile.TurnID = CodexTurnID{}
		profile.TurnLineage.ParentTurnID = CodexTurnID{}
		profile.TurnLineage.RootTurnID = CodexTurnID{}
		profile.TurnStartedAtUnixMS = 0
		profile.TurnStartedAtSet = false
		profile.turnIDCandidates = nil
		profile.parentTurnIDCandidates = nil
		profile.rootTurnIDCandidates = nil
		profile.turnIDPresent = false
		profile.parentTurnIDPresent = false
		profile.rootTurnIDPresent = false
		profile.turnIDMalformed = false
		profile.parentTurnIDMalformed = false
		profile.rootTurnIDMalformed = false
	}

	if plan.InstallationPolicy == OpenAIOAuthInstallationAccountPin && plan.InstallationEnabled {
		profile.InstallationID = strings.TrimSpace(plan.InstallationID)
	}
	if plan.TurnIdentityEnabled {
		profile.SessionID = plan.TurnIdentity.SessionID
		profile.ThreadID = plan.TurnIdentity.ThreadID
		if kind != CodexWireRequestMemory {
			profile.TurnLineage.ParentThreadID = plan.TurnIdentity.ParentThreadID
			profile.TurnLineage.ForkedFromThreadID = plan.TurnIdentity.ForkedFromThreadID
		}
	}
	profile.WindowID = ""
	if ValidateOpenAICodexWindowSnapshot(plan.Window) == nil && plan.Window.ThreadID == plan.TurnIdentity.ThreadID {
		profile.WindowID = plan.Window.WindowID()
	}

	if kind == CodexWireRequestCompaction {
		if normalized, valid := normalizeCodexCompactionMetadata(profile.Compaction); valid {
			profile.Compaction = normalized
		} else if options.Compaction != nil && options.Compaction.Valid() {
			profile.Compaction = marshalCodexCompactionMetadata(*options.Compaction)
		} else {
			profile.Compaction = marshalCodexCompactionMetadata(DefaultCodexCompactionTurnMetadata(defaultCodexCompactionImplementation(plan.ProjectionMode)))
		}
	}
	modelCapabilities := options.ModelCapabilities
	profile.ToolNamespacesAllowed = modelCapabilities.UseResponsesLite
	metadataProfile := options.MetadataProfile.normalized()
	profile.ToolNamespacesInfoAllowed = modelCapabilities.UseResponsesLite && metadataProfile.TurnMetadataIncludesToolInfo
	if modelCapabilities.Known && kind != CodexWireRequestMemory {
		profile.NodeREPLAutoReviewRequired = boolPointer(modelCapabilities.NodeREPLAutoReviewRequired)
		profile.NodeREPLDisabled = boolPointer(modelCapabilities.NodeREPLDisabled)
	}
	if !profile.ToolNamespacesInfoAllowed {
		profile.ToolNamespacesInfo = nil
	}
	if strings.TrimSpace(options.FinalModel) != "" || strings.TrimSpace(options.FinalServiceTier) != "" {
		profile.RoutingHint = BuildOpenAICodexRoutingHint(options.FinalModel, options.FinalServiceTier)
	}
	profile = profile.withDefaults(metadataProfile)
	if !profile.rootTurnIDPresent && plan.TurnIdentityEnabled &&
		normalizedOpenAICodexTurnRelation(plan.TurnIdentity) == OpenAICodexTurnRelationRoot &&
		plan.TurnIdentity.SessionID == plan.TurnIdentity.ThreadID &&
		plan.TurnIdentity.ParentThreadID == "" && plan.TurnIdentity.ForkedFromThreadID == "" &&
		profile.SubagentHeader == "" && profile.SubagentKind == "" &&
		profile.TurnID.ValidFor(kind) {
		profile.TurnLineage.RootTurnID = profile.TurnID
		profile.rootTurnIDPresent = true
		profile.rootTurnIDCandidates = appendCodexTurnIDCandidate(profile.rootTurnIDCandidates, profile.TurnID.Value)
	}
	if err := profile.Validate(); err != nil {
		return plan, err
	}
	plan.WireProfile = profile
	return plan, nil
}

func clearOpenAICodexMemoryTurnIdentity(plan *OpenAIOAuthIdentityPlan, profile *CodexWireProfile) {
	if plan != nil {
		plan.RequestTurn = OpenAICodexRequestTurnSnapshot{}
	}
	if profile == nil {
		return
	}
	profile.AgentName = ""
	profile.TurnID = CodexTurnID{}
	profile.TurnLineage = CodexTurnLineage{}
	profile.TurnStartedAtUnixMS = 0
	profile.TurnStartedAtSet = false
	profile.Compaction = nil
	profile.turnIDCandidates = nil
	profile.parentTurnIDCandidates = nil
	profile.rootTurnIDCandidates = nil
	profile.turnIDPresent = false
	profile.parentTurnIDPresent = false
	profile.rootTurnIDPresent = false
	profile.turnIDMalformed = false
	profile.parentTurnIDMalformed = false
	profile.rootTurnIDMalformed = false
	profile.InvalidReason = ""
}
