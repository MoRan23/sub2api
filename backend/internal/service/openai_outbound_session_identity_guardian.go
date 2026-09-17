package service

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const guardianSourceBindingTTL = 15 * time.Minute
const guardianSourceBindingCapacity = 4096

type guardianSourceThreadBinding struct {
	threadID  string
	ambiguous bool
	expiresAt time.Time
}

var guardianSourceThreadBindings = struct {
	sync.Mutex
	entries map[string]guardianSourceThreadBinding
}{entries: make(map[string]guardianSourceThreadBinding)}

func guardianSourceThreadBindingKey(namespace string, apiKeyID int64, sessionID, logicalSession, sourceThread, sourceTurn string) string {
	if namespace == "" || sessionID == "" || logicalSession == "" || sourceThread == "" || sourceTurn == "" {
		return ""
	}
	return strings.Join([]string{namespace, strconv.FormatInt(apiKeyID, 10), sessionID, logicalSession, sourceThread, sourceTurn}, "\x00")
}

// recordOpenAICodexGuardianSourceThread is called only at a physical send
// boundary, independently of fingerprint observation. It binds the real source
// turn to its projected child. HTTP headers are authoritative; WS callers pass
// nil headers and the final response.create frame instead of an old handshake.
func recordOpenAICodexGuardianSourceThread(plan OpenAIOAuthIdentityPlan, headers http.Header, body []byte) {
	// A logical root projected beneath the daily root has a stable child ID,
	// but this binding still proves the particular source turn was actually sent.
	if !plan.TurnIdentityEnabled || plan.Capture.Logical.SessionKey != plan.Capture.Logical.ThreadKey ||
		plan.TurnIdentity.SessionID == plan.TurnIdentity.ThreadID ||
		strings.EqualFold(plan.WireProfile.ThreadSource, "guardian_classifier") {
		return
	}
	payload := []byte(openAIRequestPayloadView(body).Raw)
	profile := finalFingerprintCodexWireProfile(headers, payload)
	physical := captureOpenAICodexLogicalTurnIdentity(&gin.Context{Request: &http.Request{Header: headers}}, payload, "", "", false, false)
	if physical.ConflictCount != 0 || physical.InvalidMetadataCount != 0 {
		return
	}
	if headers != nil {
		sessionID := guardianSourceHeaderIdentity(headers, "session-id", "session_id")
		threadID := guardianSourceHeaderIdentity(headers, "thread-id", "thread_id")
		if (profile.SessionID != "" && profile.SessionID != sessionID) ||
			(profile.ThreadID != "" && profile.ThreadID != threadID) {
			return
		}
		profile.SessionID, profile.ThreadID = sessionID, threadID
	}
	if profile.SessionID != plan.TurnIdentity.SessionID || profile.ThreadID != plan.TurnIdentity.ThreadID ||
		profile.TurnID.Value == "" || profile.TurnID.Value != plan.RequestTurn.ID {
		return
	}
	key := guardianSourceThreadBindingKey(plan.CredentialOwnerNamespace, plan.APIKeyID, profile.SessionID,
		plan.Capture.Logical.SessionKey, plan.Capture.Logical.ThreadKey, profile.TurnID.Value)
	if key == "" {
		return
	}
	now := time.Now()
	guardianSourceThreadBindings.Lock()
	defer guardianSourceThreadBindings.Unlock()
	if previous, ok := guardianSourceThreadBindings.entries[key]; ok && now.Before(previous.expiresAt) {
		// Rematerializing one source turn into multiple physical child identities
		// is ambiguous. A retry must not silently overwrite the first binding.
		if previous.threadID != profile.ThreadID {
			previous.ambiguous = true
		}
		guardianSourceThreadBindings.entries[key] = previous
		return
	}
	if len(guardianSourceThreadBindings.entries) >= guardianSourceBindingCapacity {
		oldestKey := ""
		var oldest time.Time
		for candidate, binding := range guardianSourceThreadBindings.entries {
			if !now.Before(binding.expiresAt) {
				delete(guardianSourceThreadBindings.entries, candidate)
				continue
			}
			if oldestKey == "" || binding.expiresAt.Before(oldest) {
				oldestKey, oldest = candidate, binding.expiresAt
			}
		}
		if len(guardianSourceThreadBindings.entries) >= guardianSourceBindingCapacity {
			delete(guardianSourceThreadBindings.entries, oldestKey)
		}
	}
	guardianSourceThreadBindings.entries[key] = guardianSourceThreadBinding{threadID: profile.ThreadID, expiresAt: now.Add(guardianSourceBindingTTL)}
}

func guardianSourceHeaderIdentity(headers http.Header, aliases ...string) string {
	winner := ""
	for _, alias := range aliases {
		for _, raw := range headerValuesCaseInsensitive(headers, alias) {
			value, err := canonicalUUIDv7(raw)
			if err != nil || (winner != "" && winner != value) {
				return ""
			}
			winner = value
		}
	}
	return winner
}

func lookupOpenAICodexGuardianSourceThread(namespace string, apiKeyID int64, sessionID string, logical OpenAICodexLogicalTurnIdentity) string {
	key := guardianSourceThreadBindingKey(namespace, apiKeyID, sessionID, logical.SessionKey,
		logical.GuardianClassifierSourceThreadKey, logical.GuardianClassifierParentTurnKey)
	if key == "" {
		return ""
	}
	guardianSourceThreadBindings.Lock()
	defer guardianSourceThreadBindings.Unlock()
	binding, ok := guardianSourceThreadBindings.entries[key]
	if !ok || binding.ambiguous || !time.Now().Before(binding.expiresAt) {
		return ""
	}
	return binding.threadID
}

// The classifier's source thread is a separate identity from parent_thread_id:
// daily roots may replace the latter without changing the reviewed thread.
func (profile *CodexWireProfile) captureGuardianClassifierSource(metadata map[string]json.RawMessage) {
	raw, present := metadata["guardian_classifier_source_thread_id"]
	if !present {
		return
	}
	profile.guardianSourcePresent = true
	value := codexGuardianClassifierUUID(codexWireString(raw))
	profile.guardianSourceInvalid = value == ""
	profile.GuardianClassifierSourceThreadID = value
}

func (profile *CodexWireProfile) mergeGuardianClassifierSource(source CodexWireProfile) {
	if profile.guardianSourcePresent || source.guardianSourcePresent {
		for _, pair := range [][2]string{{profile.ThreadSource, source.ThreadSource}, {profile.SubagentHeader, source.SubagentHeader}} {
			if pair[0] != "" && pair[1] != "" && !strings.EqualFold(pair[0], pair[1]) {
				profile.guardianSourceInvalid = true
				profile.GuardianClassifierSourceThreadID = ""
			}
		}
	}
	if !source.guardianSourcePresent {
		return
	}
	if source.guardianSourceInvalid || profile.guardianSourceInvalid ||
		(profile.guardianSourcePresent && profile.GuardianClassifierSourceThreadID != source.GuardianClassifierSourceThreadID) {
		profile.GuardianClassifierSourceThreadID = ""
		profile.guardianSourceInvalid = true
	} else {
		profile.GuardianClassifierSourceThreadID = source.GuardianClassifierSourceThreadID
	}
	profile.guardianSourcePresent = true
}

func (profile CodexWireProfile) guardianClassifierSourceThread(threadID string) string {
	if profile.guardianSourceInvalid || profile.GuardianClassifierSourceThreadID == "" ||
		strings.EqualFold(profile.GuardianClassifierSourceThreadID, threadID) ||
		!strings.EqualFold(strings.TrimSpace(profile.ThreadSource), "guardian_classifier") {
		return ""
	}
	if !hasUnambiguousOpenAICodexReviewSubagent(profile.SubagentHeader, profile.SubagentKind) {
		return ""
	}
	return profile.GuardianClassifierSourceThreadID
}
