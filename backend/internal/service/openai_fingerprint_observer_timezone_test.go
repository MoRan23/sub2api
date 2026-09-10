package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCloneFingerprintObservationHeadersKeepsOnlySafePhysicalValues(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	input := http.Header{
		"Authorization":                     {"Bearer must-not-retain"},
		"authorization":                     {"Bearer lowercase-secret"},
		"Proxy-Authorization":               {"Basic secret"},
		"Cookie":                            {"session=secret"},
		"X-Api-Key":                         {"sk-secret"},
		"Chatgpt-Account-Id":                {"private-account-id"},
		"X-Agent-Identity-Token":            {"agent-secret"},
		"x-openai-internal-codex-residency": {"eu", "EU"},
		"X-OpenAI-Internal-Codex-Residency": {"us"},
		"User-Agent":                        {"codex/1"},
		"session_id":                        {fingerprintObserverSessionV7},
		"thread-id":                         {fingerprintObserverThreadV7},
		"X-Codex-Turn-Metadata":             {`{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `","installation_id":"install","authorization":"secret","unknown":{"token":"secret"},"parent_thread_id":{"token":"nested-secret"}}`, "malformed-secret"},
	}
	got := cloneFingerprintObservationHeaders(input)
	require.Len(t, got, 6)
	require.Equal(t, []string{"eu", "EU"}, got["x-openai-internal-codex-residency"])
	require.Equal(t, []string{"us"}, got["X-OpenAI-Internal-Codex-Residency"])
	require.Equal(t, "null", got["X-Codex-Turn-Metadata"][1])
	var metadata map[string]any
	require.NoError(t, json.Unmarshal([]byte(got["X-Codex-Turn-Metadata"][0]), &metadata))
	require.Equal(t, fingerprintObserverSessionV7, metadata["session_id"])
	require.Equal(t, "install", metadata["installation_id"])
	require.Contains(t, metadata, "parent_thread_id")
	require.Nil(t, metadata["parent_thread_id"])
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "private-account")
	input["User-Agent"][0] = "mutated-input"
	input["x-openai-internal-codex-residency"][0] = "mutated-input"
	require.Equal(t, []string{"codex/1"}, got["User-Agent"])
	require.Equal(t, []string{"eu", "EU"}, got["x-openai-internal-codex-residency"])
	got["session_id"][0] = "mutated-copy"
	require.Equal(t, fingerprintObserverSessionV7, input["session_id"][0])
	require.Nil(t, cloneFingerprintObservationHeaders(nil))
	require.Equal(t, " \t", fingerprintObservationSafeTurnMetadata(" \t"), "an empty carrier must remain absent rather than become an invalid field")
}

func fingerprintTimezoneTestEntry() FingerprintObservationEntry {
	return FingerprintObservationEntry{
		Timestamp: time.Now(), UserID: 1, APIKeyID: 2,
		InboundTimezoneObservations: &TimezoneScanResult{ScanStatus: "complete", Items: []TimezoneScanItem{
			{Source: "web_search", Path: "tools.0.user_location.timezone", Value: "Asia/Shanghai", Current: true},
		}},
		OutboundTimezoneObservations: &TimezoneScanResult{ScanStatus: "complete", Items: []TimezoneScanItem{
			{Source: "web_search", Path: "tools.0.user_location.timezone", Value: OpenAIRequestTimezone, Current: true},
		}},
		TimezoneConversions: []TimezoneConversion{{Source: "web_search", Path: "tools.0.user_location.timezone",
			Original: "Asia/Shanghai", Output: OpenAIRequestTimezone, Status: "converted"}},
	}
}

func TestFingerprintObserverTimezoneRecordAndSnapshotsAreDeepCopies(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 2)}
	observer.setEnabled(true)
	entry := fingerprintTimezoneTestEntry()
	observer.record(entry)
	entry.InboundTimezoneObservations.Items[0].Value = "mutated-input"
	entry.OutboundTimezoneObservations.ScanStatus = "mutated-status"
	entry.TimezoneConversions[0].Output = "mutated-output"
	first := observer.snapshot(1)
	require.Equal(t, "Asia/Shanghai", first[0].InboundTimezoneObservations.Items[0].Value)
	require.Equal(t, "complete", first[0].OutboundTimezoneObservations.ScanStatus)
	require.Equal(t, OpenAIRequestTimezone, first[0].TimezoneConversions[0].Output)
	first[0].InboundTimezoneObservations.Items[0].Value = "mutated-return"
	first[0].TimezoneConversions[0].Reason = "mutated-return"
	through, _ := observer.snapshotThrough(0)
	require.Equal(t, "Asia/Shanghai", through[0].InboundTimezoneObservations.Items[0].Value)
	require.Empty(t, through[0].TimezoneConversions[0].Reason)
	through[0].OutboundTimezoneObservations.Items[0].Value = "mutated-through"
	require.Equal(t, OpenAIRequestTimezone, observer.snapshot(1)[0].OutboundTimezoneObservations.Items[0].Value)
}

func TestFingerprintObserverTimezoneEvictionAndDisableScrubNestedStorage(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 1)}
	observer.setEnabled(true)
	observer.record(fingerprintTimezoneTestEntry())
	retained := observer.ring[0]
	retainedItems := retained.InboundTimezoneObservations.Items
	retainedConversions := retained.TimezoneConversions
	observer.record(fingerprintTimezoneTestEntry())
	require.Empty(t, retained.InboundTimezoneObservations.ScanStatus)
	require.Equal(t, TimezoneScanItem{}, retainedItems[0])
	require.Equal(t, TimezoneConversion{}, retainedConversions[0])
	retained = observer.ring[0]
	retainedItems = retained.OutboundTimezoneObservations.Items
	observer.setEnabled(false)
	require.Equal(t, FingerprintObservationEntry{}, observer.ring[0])
	require.Equal(t, TimezoneScanItem{}, retainedItems[0])
	require.Empty(t, retained.OutboundTimezoneObservations.ScanStatus)
}

func TestFingerprintObservationTimezoneComparisonUsesActualFinalBody(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	entry := fingerprintTimezoneTestEntry()
	state := &RequestTimezoneState{Inbound: entry.InboundTimezoneObservations, Conversions: entry.TimezoneConversions}
	tests := []struct {
		name, body, want string
		paths            map[string]string
	}{
		{name: "converted", body: `{"tools":[{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`, want: "matched"},
		{name: "adapted requires explicit identity mapping", body: `{"tools":[{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`, paths: map[string]string{}, want: "unmatched"},
		{name: "adapted explicit unchanged path", body: `{"tools":[{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`, paths: map[string]string{"tools.0.user_location.timezone": "tools.0.user_location.timezone"}, want: "matched"},
		{name: "actual original", body: `{"tools":[{"type":"web_search","user_location":{"timezone":"Asia/Shanghai"}}]}`, want: "mismatched"},
		{name: "removed", body: `{"input":"hello"}`, want: "not_sent"},
		{name: "relocated", body: `{"tools":[{"type":"function"},{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`, want: "unmatched"},
		{name: "mapped", body: `{"tools":[{"type":"function"},{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`, paths: map[string]string{"tools.0.user_location.timezone": "tools.1.user_location.timezone"}, want: "matched"},
		{name: "explicit removed", body: `{"input":"hello"}`, paths: map[string]string{"tools.0.user_location.timezone": ""}, want: "not_sent"},
		{name: "malformed final", body: `{"tools":`, want: "incomplete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FingerprintObservationEntry{}
			populateFingerprintObservationTimezones(&got, state, []byte(tt.body), tt.paths)
			require.Equal(t, tt.want, got.TimezoneComparisonStatus)
			require.Equal(t, "Asia/Shanghai", got.InboundTimezoneObservations.Items[0].Value)
			require.Equal(t, "converted", state.Conversions[0].Status, "observation must not mutate the frozen conversion state")
		})
	}
}

func TestFingerprintObservationTimezoneAdapterCannotMatchDeletedPreviewByShiftedTool(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	// The preview tool at index 0 was deleted. A different retained web_search
	// tool moved from index 1 to index 0, with the same converted timezone value.
	state := &RequestTimezoneState{
		Inbound: &TimezoneScanResult{ScanStatus: "complete", Items: []TimezoneScanItem{
			{Source: "web_search", Path: "tools.0.user_location.timezone", Value: "Asia/Shanghai", Status: "valid", Current: true},
			{Source: "web_search", Path: "tools.1.user_location.timezone", Value: "Europe/London", Status: "valid", Current: true},
		}},
		Conversions: []TimezoneConversion{
			{Source: "web_search", Path: "tools.0.user_location.timezone", Original: "Asia/Shanghai", Output: OpenAIRequestTimezone, Status: "converted"},
			{Source: "web_search", Path: "tools.1.user_location.timezone", Original: "Europe/London", Output: OpenAIRequestTimezone, Status: "converted"},
		},
	}
	body := []byte(`{"tools":[{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`)
	entry := FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&entry, state, body, map[string]string{})
	require.Equal(t, "unmatched", entry.TimezoneComparisonStatus)
	require.Equal(t, "unmatched", entry.TimezoneConversions[0].Status, "deleted index 0 cannot claim the shifted tool's matching output")
	require.Equal(t, "unmatched", entry.TimezoneConversions[1].Status)
	entry = FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&entry, state, body, map[string]string{
		"tools.0.user_location.timezone": "",
		"tools.1.user_location.timezone": "tools.0.user_location.timezone",
	})
	require.Equal(t, "not_sent", entry.TimezoneComparisonStatus)
	require.Equal(t, "not_sent", entry.TimezoneConversions[0].Status)
	require.Equal(t, "converted", entry.TimezoneConversions[1].Status)
}

func TestFingerprintObservationTimezoneDisabledDoesNotScanOrChangeState(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	entry := fingerprintTimezoneTestEntry()
	state := &RequestTimezoneState{Inbound: entry.InboundTimezoneObservations, Conversions: entry.TimezoneConversions}
	got := FingerprintObservationEntry{}
	populateFingerprintObservationTimezones(&got, state, []byte(`not-json`), nil)
	require.Equal(t, FingerprintObservationEntry{}, got)
	(&OpenAIGatewayService{}).recordFingerprintObservationWSFrame(nil,
		&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, state, []byte(`not-json`), nil, nil)
	require.Empty(t, SnapshotFingerprintObservations(0))
	require.Equal(t, "converted", state.Conversions[0].Status)
}

func TestFingerprintObservationTimezoneComparisonDoesNotMatchRedactedValues(t *testing.T) {
	entry := FingerprintObservationEntry{
		InboundTimezoneObservations: &TimezoneScanResult{ScanStatus: "complete", Items: []TimezoneScanItem{
			{Source: "web_search", Path: "tools.0.user_location.timezone", Status: "invalid", Reason: "timezone_not_string"},
		}},
		OutboundTimezoneObservations: &TimezoneScanResult{ScanStatus: "complete", Items: []TimezoneScanItem{
			{Source: "web_search", Path: "tools.0.user_location.timezone", Status: "invalid", Reason: "timezone_not_string"},
		}},
	}
	require.Equal(t, "unmatched", compareFingerprintObservationTimezones(&entry, nil))
}

func TestFingerprintObservationOpenAIAPIKeyAndExplicitRequestSource(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/custom/model/route", nil)
	entry := fingerprintTimezoneTestEntry()
	body := []byte(`{"tools":[{"type":"web_search","user_location":{"timezone":"America/Los_Angeles"}}]}`)
	SetRequestTimezoneState(c, &RequestTimezoneState{Inbound: entry.InboundTimezoneObservations, Conversions: entry.TimezoneConversions, preparedBody: body})
	// A reused context's Codex identity is not authority for an API-key upstream.
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7})
	headers := http.Header{"Session-Id": {fingerprintObserverSessionV7}, "X-Openai-Internal-Codex-Residency": {"us"}}
	svc := &OpenAIGatewayService{}
	svc.recordFingerprintObservationFromContextWithBody(c, &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, headers, body)
	got := SnapshotFingerprintObservations(1)
	require.Len(t, got, 1)
	require.Equal(t, FingerprintObservationEventHTTP, got[0].EventKind)
	require.Equal(t, "matched", got[0].TimezoneComparisonStatus)
	require.Equal(t, "us", got[0].OutboundCodexResidency)
	require.Empty(t, got[0].SessionID)
	require.Empty(t, got[0].ThreadID)
	svc.recordFingerprintObservationFromContextWithBody(c, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, headers, body)
	require.Len(t, SnapshotFingerprintObservations(0), 1)
}

func TestFingerprintObservationWSFrameUsesCurrentPlanAndHandshakeOnlyMetadata(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	account := newOpenAIOAuthPinAccount(1, nil)
	headers := http.Header{"Session-Id": {fingerprintObserverSessionV7}, "Thread-Id": {fingerprintObserverSessionV7}, "X-Openai-Internal-Codex-Residency": {"us"}}
	plan := &OpenAIOAuthIdentityPlan{TurnIdentityEnabled: true, TurnIdentity: OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverThreadV7, Relation: OpenAICodexTurnRelationDescendant,
	}}
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7})
	body := []byte(`{"type":"response.create","input":"hello","client_metadata":{"session_id":"` + fingerprintObserverSessionV7 + `","thread_id":"` + fingerprintObserverThreadV7 + `"}}`)
	svc := &OpenAIGatewayService{}
	svc.recordFingerprintObservationWSHandshake(c, account, headers)
	svc.recordFingerprintObservationWSFrame(c, account, nil, body, headers, plan)
	got := SnapshotFingerprintObservations(0)
	require.Len(t, got, 2)
	require.Equal(t, FingerprintObservationEventWSFrame, got[0].EventKind)
	require.Equal(t, fingerprintObserverThreadV7, got[0].ThreadID)
	require.Equal(t, "ws_handshake", got[0].OutboundCodexResidencySource)
	require.Equal(t, "us", got[0].OutboundCodexResidency)
	require.Equal(t, FingerprintObservationEventWSHandshake, got[1].EventKind)
	require.Equal(t, "not_applicable", got[1].TimezoneComparisonStatus)
	require.Nil(t, got[1].InboundTimezoneObservations)
	require.Nil(t, got[1].OutboundTimezoneObservations)
	svc.recordFingerprintObservationWSFrame(c, account, nil, body, headers, nil)
	require.Empty(t, SnapshotFingerprintObservations(1)[0].ThreadID, "a missing current plan must not reuse context or handshake identity")
}

func TestFrozenFingerprintWSHandshakeRecorderOwnsAttributionAndChecksLiveSwitch(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	account := newOpenAIOAuthPinAccount(800, nil)
	account.Name = "original-account"
	account.Credentials = map[string]any{"access_token": "upstream-secret-must-not-retain"}
	apiKey := &APIKey{ID: 10, Name: "original-key", Key: "downstream-secret-must-not-retain", UserID: 20,
		User: &User{ID: 20, Username: "original-user", Email: "original@example.com"}}
	c.Set("api_key", apiKey)
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7,
	})
	recorder := freezeFingerprintObservationWSHandshake(c, account)
	require.NotNil(t, recorder, "a pool request built while disabled may dial after observation is enabled")
	headers := http.Header{"Session-Id": {fingerprintObserverSessionV7}, "Thread-Id": {fingerprintObserverSessionV7},
		"x-openai-internal-codex-residency": {"us"}}
	recorder(headers)
	require.Empty(t, SnapshotFingerprintObservations(0))
	// Background dials must use immutable attribution, not these now-reused
	// context/account/actor objects or any of their credential containers.
	account.ID, account.Name = 801, "changed-account"
	apiKey.ID, apiKey.Name = 11, "changed-key"
	apiKey.User.Username = "changed-user"
	c.Request.URL.Path = "/changed/path"
	clearFingerprintObservationOutboundIdentity(c)
	SetFingerprintObservationEnabled(true)
	recorder(headers)
	got := SnapshotFingerprintObservations(1)
	require.Len(t, got, 1)
	require.Equal(t, int64(800), got[0].AccountID)
	require.Equal(t, "original-account", got[0].AccountName)
	require.Equal(t, int64(10), got[0].APIKeyID)
	require.Equal(t, "original-key", got[0].APIKeyName)
	require.Equal(t, "original-user", got[0].Username)
	require.Equal(t, "GET /v1/responses", got[0].InboundEndpoint)
	require.Equal(t, fingerprintObserverSessionV7, got[0].SessionID)
	require.Equal(t, "us", got[0].OutboundCodexResidency)
	require.Equal(t, FingerprintObservationEventWSHandshake, got[0].EventKind)
	require.Equal(t, "not_applicable", got[0].TimezoneComparisonStatus)
	require.Nil(t, got[0].InboundTimezoneObservations)
	require.Nil(t, got[0].OutboundTimezoneObservations)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-must-not-retain")
	SetFingerprintObservationEnabled(false)
	recorder(headers)
	require.Empty(t, SnapshotFingerprintObservations(0), "late background completion must not reopen the closed observation window")
	require.Nil(t, freezeFingerprintObservationWSHandshake(c, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}))
}

func TestFrozenFingerprintWSHandshakeRecorderCannotForgeAPIKeyIdentity(t *testing.T) {
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setFingerprintObservationOutboundIdentity(c, OpenAICodexTurnIdentity{
		SessionID: fingerprintObserverSessionV7, ThreadID: fingerprintObserverSessionV7,
	})
	recorder := freezeFingerprintObservationWSHandshake(c, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	recorder(http.Header{"Session-Id": {fingerprintObserverSessionV7}, "Thread-Id": {fingerprintObserverSessionV7}})
	got := SnapshotFingerprintObservations(1)
	require.Len(t, got, 1)
	require.Empty(t, got[0].SessionID)
	require.Empty(t, got[0].ThreadID)
}

func TestFingerprintObserverTimezoneSnapshotsCanBeMutatedConcurrentlyWithDisable(t *testing.T) {
	observer := &fingerprintObserver{ring: make([]FingerprintObservationEntry, 8)}
	observer.setEnabled(true)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				observer.record(fingerprintTimezoneTestEntry())
				for _, entry := range observer.snapshot(0) {
					entry.InboundTimezoneObservations.Items[0].Value = "caller-owned"
					entry.TimezoneConversions[0].Output = "caller-owned"
				}
			}
		}()
	}
	observer.setEnabled(false)
	wg.Wait()
	require.Empty(t, observer.snapshot(0))
}
