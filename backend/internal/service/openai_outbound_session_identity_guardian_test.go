package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func guardianIdentityBody(t *testing.T, session, thread, turn, source, parentTurn string) []byte {
	t.Helper()
	metadata := map[string]any{"session_id": session, "thread_id": thread, "turn_id": turn}
	flat := map[string]any{"session_id": session, "thread_id": thread}
	root := map[string]any{"model": "configured-classifier", "stream": true, "input": "test"}
	if source != "" {
		metadata["guardian_classifier_source_thread_id"] = source
		metadata["thread_source"] = "guardian_classifier"
		metadata["parent_turn_id"] = parentTurn
		flat["x-openai-subagent"] = "guardian"
		root["prompt_cache_key"] = "guardian-v2:" + source
	}
	nested, err := json.Marshal(metadata)
	require.NoError(t, err)
	flat[openAIWSTurnMetadataHeader] = string(nested)
	root["client_metadata"] = flat
	body, err := json.Marshal(root)
	require.NoError(t, err)
	return body
}

func guardianIdentityService(t *testing.T, daily bool) (*OpenAIGatewayService, *Account) {
	t.Helper()
	root := uuid.Must(uuid.NewV7()).String()
	repo := &dailyRotationSettingRepo{values: map[string]string{
		SettingKeyEnableOpenAICodexFingerprintNormalization: "true",
		SettingKeyEnableOpenAIUUIDv7SessionIdentity:         "true",
		SettingKeyEnableOpenAIOAuthDailySessionRotation:     strconv.FormatBool(daily),
	}}
	svc := &OpenAIGatewayService{
		cfg:            &config.Config{JWT: config.JWTConfig{Secret: "guardian-identity-test"}},
		settingService: NewSettingService(repo, nil),
		oauthDailySessionRepo: &fakeOAuthDailyAffinityRepository{
			pool:     OAuthDailySessionPool{AccountID: 9971, BusinessDate: OAuthDailyBusinessDate(time.Now()), Generation: root},
			affinity: OAuthDailySessionAffinity{AccountID: 9971, APIKeyID: 9972, StreamSessionID: root},
		},
	}
	return svc, &Account{ID: 9971, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func guardianResolvePlan(t *testing.T, svc *OpenAIGatewayService, account *Account, apiKeyID int64, body []byte) (OpenAIOAuthIdentityPlan, http.Header, []byte) {
	t.Helper()
	c := newOutboundIdentityTestContext(t, nil)
	c.Set("api_key", &APIKey{ID: apiKeyID})
	setOpenAIClientRequestedStream(c, true)
	plan, err := svc.ResolveOpenAIOAuthIdentityPlan(context.Background(), c, account, CaptureOpenAIOAuthIdentity(c, body, ""), OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve})
	require.NoError(t, err)
	require.True(t, plan.TurnIdentityEnabled)
	plan, err = FinalizeOpenAICodexWirePlan(plan, "turn", CodexModelCapabilities{})
	require.NoError(t, err)
	headers := make(http.Header)
	out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, plan)
	require.NoError(t, err)
	return plan, headers, out
}

func TestGuardianV2IdentityMapsSourceAndCacheAcrossDailyRootModes(t *testing.T) {
	for _, daily := range []bool{false, true} {
		for _, sourceIsRoot := range []bool{false, true} {
			t.Run("daily="+strconv.FormatBool(daily)+"/root-source="+strconv.FormatBool(sourceIsRoot), func(t *testing.T) {
				resetProcessCodexIdentityStore(t)
				svc, account := guardianIdentityService(t, daily)
				session := uuid.Must(uuid.NewV7()).String()
				source := uuid.Must(uuid.NewV7()).String()
				if sourceIsRoot {
					source = session
				}
				parentTurn := uuid.Must(uuid.NewV7()).String()
				parentBody := guardianIdentityBody(t, session, source, parentTurn, "", "")
				parent, parentHeaders, parentOut := guardianResolvePlan(t, svc, account, 9972, parentBody)
				recordOpenAICodexGuardianSourceThread(parent, parentHeaders, parentOut)
				body := guardianIdentityBody(t, session, uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), source, parentTurn)
				metadata, err := sjson.Set(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String(), "parent_thread_id", source)
				require.NoError(t, err)
				body, err = sjson.SetBytes(body, "client_metadata.x-codex-turn-metadata", metadata)
				require.NoError(t, err)
				plan, headers, out := guardianResolvePlan(t, svc, account, 9972, body)
				require.Equal(t, OpenAICodexPromptCacheKeyGuardianV2, plan.PromptCacheKey.Kind)
				require.Equal(t, parent.TurnIdentity.ThreadID, plan.TurnIdentity.GuardianClassifierSourceThreadID)
				require.Equal(t, parent.TurnIdentity.ThreadID, plan.TurnIdentity.ParentThreadID, "an explicit parent remains the mapped source thread, including beneath a daily root")
				require.NotEqual(t, plan.TurnIdentity.ThreadID, plan.TurnIdentity.GuardianClassifierSourceThreadID)
				require.Equal(t, "guardian-v2:"+parent.TurnIdentity.ThreadID, gjson.GetBytes(out, "prompt_cache_key").String())
				for _, raw := range []string{headers.Get(openAIWSTurnMetadataHeader), gjson.GetBytes(out, "client_metadata.x-codex-turn-metadata").String()} {
					require.Equal(t, parent.TurnIdentity.ThreadID, gjson.Get(raw, "guardian_classifier_source_thread_id").String())
					require.NotContains(t, raw, source)
				}
				if daily {
					require.NotEqual(t, plan.TurnIdentity.SessionID, plan.TurnIdentity.GuardianClassifierSourceThreadID)
				}
			})
		}
	}
}

func TestGuardianV2IdentityMissingDailySourceNeverInventsLineage(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, account := guardianIdentityService(t, true)
	session := uuid.Must(uuid.NewV7()).String()
	body := guardianIdentityBody(t, session, uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), session, uuid.Must(uuid.NewV7()).String())
	plan, headers, out := guardianResolvePlan(t, svc, account, 9972, body)
	require.Empty(t, plan.TurnIdentity.GuardianClassifierSourceThreadID)
	require.Contains(t, plan.PromptCacheKey.Value, "pc_")
	require.NotContains(t, plan.PromptCacheKey.Value, "guardian-v2:")
	require.NotContains(t, headers.Get(openAIWSTurnMetadataHeader), "guardian_classifier_source_thread_id")
	require.NotContains(t, string(out), session)
}

func TestGuardianV2ReusedWSClassifierBindsEachParentTurnToStableSource(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, account := guardianIdentityService(t, true)
	session, classifier := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	firstTurn, secondTurn := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	firstSource, firstHeaders, firstBody := guardianResolvePlan(t, svc, account, 9972, guardianIdentityBody(t, session, session, firstTurn, "", ""))
	recordOpenAICodexGuardianSourceThread(firstSource, firstHeaders, firstBody)
	first, _, _ := guardianResolvePlan(t, svc, account, 9972, guardianIdentityBody(t, session, classifier, uuid.Must(uuid.NewV7()).String(), session, firstTurn))
	require.Equal(t, firstSource.TurnIdentity.ThreadID, first.TurnIdentity.GuardianClassifierSourceThreadID)

	secondSource, secondHeaders, secondBody := guardianResolvePlan(t, svc, account, 9972, guardianIdentityBody(t, session, session, secondTurn, "", ""))
	unbound, _, _ := guardianResolvePlan(t, svc, account, 9972, guardianIdentityBody(t, session, classifier, uuid.Must(uuid.NewV7()).String(), session, secondTurn))
	require.Empty(t, unbound.TurnIdentity.GuardianClassifierSourceThreadID, "a stable thread does not prove that the new parent turn was physically sent")
	recordOpenAICodexGuardianSourceThread(secondSource, secondHeaders, secondBody)
	require.Equal(t, firstSource.TurnIdentity.ThreadID, secondSource.TurnIdentity.ThreadID, "new turns in one logical thread reuse the daily child")
	body := guardianIdentityBody(t, session, classifier, uuid.Must(uuid.NewV7()).String(), session, secondTurn)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(body, &frame))
	frame["type"] = "response.create"
	body, err := json.Marshal(frame)
	require.NoError(t, err)
	capture := captureOpenAIWSFrameIdentity(body, &first)
	// A new parent turn needs its own physical-send binding while both the source
	// and socket's classifier thread remain stable. The passthrough compatibility
	// gate must continue to accept it.
	require.True(t, openAICodexLogicalTurnIdentityEqual(capture.Logical, first.Capture.Logical))
	c := newOutboundIdentityTestContext(t, nil)
	c.Set("api_key", &APIKey{ID: 9972})
	setOpenAIClientRequestedStream(c, true)
	next, err := svc.GetOrResolveOpenAIOAuthOutboundIdentity(context.Background(), c, account, capture,
		OpenAIOAuthIdentityPlanOptions{TurnIdentityEnabled: true, InstallationPolicy: OpenAIOAuthInstallationPreserve}, &first)
	require.NoError(t, err)
	next, err = FinalizeOpenAICodexWirePlan(next, "turn", CodexModelCapabilities{})
	require.NoError(t, err)
	out, err := ApplyOpenAIOAuthIdentityPlan(make(http.Header), body, next)
	require.NoError(t, err)
	require.Equal(t, first.TurnIdentity.ThreadID, next.TurnIdentity.ThreadID)
	require.Equal(t, secondSource.TurnIdentity.ThreadID, next.TurnIdentity.GuardianClassifierSourceThreadID)
	require.Equal(t, "guardian-v2:"+secondSource.TurnIdentity.ThreadID, gjson.GetBytes(out, "prompt_cache_key").String())
}

func TestGuardianV2SourceCaptureRejectsInvalidConflictingAndSelfLineage(t *testing.T) {
	session := uuid.Must(uuid.NewV7()).String()
	thread := uuid.Must(uuid.NewV7()).String()
	source := uuid.Must(uuid.NewV7()).String()
	for name, value := range map[string]string{"invalid": "not-a-uuid", "self": thread, "conflict": source} {
		t.Run(name, func(t *testing.T) {
			body := guardianIdentityBody(t, session, thread, uuid.Must(uuid.NewV7()).String(), value, uuid.Must(uuid.NewV7()).String())
			c := newOutboundIdentityTestContext(t, nil)
			if name == "conflict" {
				c.Request.Header.Set(openAIWSTurnMetadataHeader, `{"guardian_classifier_source_thread_id":"`+uuid.Must(uuid.NewV7()).String()+`"}`)
			}
			capture := CaptureOpenAIOAuthIdentity(c, body, "")
			require.Empty(t, capture.Logical.GuardianClassifierSourceThreadKey)
			require.NotEqual(t, OpenAICodexPromptCacheKeyGuardianV2, capture.PromptCacheKey.Kind)
		})
	}
}

func TestGuardianV2SourceMappingIsolatesCredentialAndAPIKey(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, account := guardianIdentityService(t, false)
	session, source := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	body := guardianIdentityBody(t, session, uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), source, uuid.Must(uuid.NewV7()).String())
	first, _, _ := guardianResolvePlan(t, svc, account, 9972, body)
	second, _, _ := guardianResolvePlan(t, svc, account, 9973, body)
	otherAccount := *account
	otherAccount.ID++
	third, _, _ := guardianResolvePlan(t, svc, &otherAccount, 9972, body)
	require.NotEqual(t, first.TurnIdentity.GuardianClassifierSourceThreadID, second.TurnIdentity.GuardianClassifierSourceThreadID)
	require.NotEqual(t, first.TurnIdentity.GuardianClassifierSourceThreadID, third.TurnIdentity.GuardianClassifierSourceThreadID)
}

func TestGuardianV2SourceTurnBindingsRejectAmbiguityAndExpire(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, account := guardianIdentityService(t, true)
	session, turn := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	body := guardianIdentityBody(t, session, session, turn, "", "")
	plan, headers, out := guardianResolvePlan(t, svc, account, 9972, body)
	logical := OpenAICodexLogicalTurnIdentity{SessionKey: session, GuardianClassifierSourceThreadKey: session, GuardianClassifierParentTurnKey: turn}
	lookup := func(namespace string, apiKeyID int64, root string) string {
		return lookupOpenAICodexGuardianSourceThread(namespace, apiKeyID, root, logical)
	}
	recordOpenAICodexGuardianSourceThread(plan, headers, out)
	require.Equal(t, plan.TurnIdentity.ThreadID, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID))
	require.Empty(t, lookup(plan.CredentialOwnerNamespace+"-other", plan.APIKeyID, plan.TurnIdentity.SessionID))
	require.Empty(t, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID+1, plan.TurnIdentity.SessionID))
	require.Empty(t, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID, uuid.Must(uuid.NewV7()).String()))
	key := guardianSourceThreadBindingKey(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID, session, session, turn)
	guardianSourceThreadBindings.Lock()
	previous := guardianSourceThreadBindings.entries[key]
	previous.expiresAt = time.Now().Add(-time.Second)
	guardianSourceThreadBindings.entries[key] = previous
	guardianSourceThreadBindings.Unlock()
	require.Empty(t, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID))
	recordOpenAICodexGuardianSourceThread(plan, headers, out)
	rematerialized, rematHeaders, rematOut := guardianResolvePlan(t, svc, account, 9972, body)
	require.Equal(t, plan.TurnIdentity.ThreadID, rematerialized.TurnIdentity.ThreadID)
	recordOpenAICodexGuardianSourceThread(rematerialized, rematHeaders, rematOut)
	require.Equal(t, plan.TurnIdentity.ThreadID, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID), "normal rematerialization cannot invalidate the physical source binding")

	// Explicitly simulate inconsistent physical identities from separate senders.
	// Normal daily-root rematerialization must no longer create this conflict.
	conflicting := cloneOpenAIOAuthIdentityPlan(plan)
	conflicting.TurnIdentity.ThreadID = uuid.Must(uuid.NewV7()).String()
	conflicting.WireProfile.ThreadID = conflicting.TurnIdentity.ThreadID
	conflictHeaders := headers.Clone()
	for name, values := range conflictHeaders {
		for i, value := range values {
			conflictHeaders[name][i] = string(bytes.ReplaceAll([]byte(value), []byte(plan.TurnIdentity.ThreadID), []byte(conflicting.TurnIdentity.ThreadID)))
		}
	}
	conflictBody := bytes.ReplaceAll(out, []byte(plan.TurnIdentity.ThreadID), []byte(conflicting.TurnIdentity.ThreadID))
	recordOpenAICodexGuardianSourceThread(conflicting, conflictHeaders, conflictBody)
	require.Empty(t, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID))
	// Repeating the old identity cannot erase the recorded ambiguity.
	recordOpenAICodexGuardianSourceThread(plan, headers, out)
	require.Empty(t, lookup(plan.CredentialOwnerNamespace, plan.APIKeyID, plan.TurnIdentity.SessionID))
}

func TestGuardianV2SourceTurnBindingCacheIsBounded(t *testing.T) {
	resetProcessCodexIdentityStore(t)
	svc, account := guardianIdentityService(t, true)
	session, turn := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	plan, headers, out := guardianResolvePlan(t, svc, account, 9972, guardianIdentityBody(t, session, session, turn, "", ""))
	guardianSourceThreadBindings.Lock()
	old := guardianSourceThreadBindings.entries
	guardianSourceThreadBindings.entries = make(map[string]guardianSourceThreadBinding, guardianSourceBindingCapacity)
	for i := 0; i < guardianSourceBindingCapacity; i++ {
		guardianSourceThreadBindings.entries[fmt.Sprintf("capacity-test-%d", i)] = guardianSourceThreadBinding{expiresAt: time.Now().Add(time.Hour)}
	}
	guardianSourceThreadBindings.Unlock()
	t.Cleanup(func() {
		guardianSourceThreadBindings.Lock()
		guardianSourceThreadBindings.entries = old
		guardianSourceThreadBindings.Unlock()
	})
	recordOpenAICodexGuardianSourceThread(plan, headers, out)
	guardianSourceThreadBindings.Lock()
	length := len(guardianSourceThreadBindings.entries)
	guardianSourceThreadBindings.Unlock()
	require.Equal(t, guardianSourceBindingCapacity, length)
}
