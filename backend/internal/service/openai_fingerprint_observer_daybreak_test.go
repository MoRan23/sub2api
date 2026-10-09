package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func enableDaybreakObservationTest(t *testing.T) {
	t.Helper()
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	t.Cleanup(func() { SetFingerprintObservationEnabled(false) })
}

func TestFingerprintDaybreakDecisionsAndWireValues(t *testing.T) {
	for _, tc := range []struct {
		name, model, fields, programs, source, reason, kind, value string
		blue, red                                                  bool
	}{
		{name: "blue injected", model: "gpt-5.6-sol", blue: true, programs: `["daybreak_blue"]`, source: "automatic", reason: "automatic", kind: "string", value: "daybreak_blue"},
		{name: "astra red switch blue wire", model: "gpt-6-astra", blue: true, red: true, programs: `["daybreak_blue"]`, source: "automatic", reason: "automatic", kind: "string", value: "daybreak_blue"},
		{name: "client same value", fields: `,"access_programs":{"cyber":"daybreak_blue"}`, source: "client", reason: "client_supplied", kind: "string", value: "daybreak_blue"},
		{name: "client null", fields: `,"access_programs":{"cyber":null}`, source: "client", reason: "client_supplied", kind: "null", value: "null"},
		{name: "client empty", fields: `,"access_programs":{"cyber":""}`, source: "client", reason: "client_supplied", kind: "string"},
		{name: "client invalid parent", fields: `,"access_programs":null`, source: "not_added", reason: "invalid_access_programs", kind: "missing"},
		{name: "disabled", source: "not_added", reason: "blue_disabled", kind: "missing"},
		{name: "red required", model: "gpt-6-astra", blue: true, source: "not_added", reason: "red_disabled", kind: "missing"},
		{name: "unknown model", model: "unknown", blue: true, source: "not_added", reason: "unrecognized_model", kind: "missing"},
		{name: "unsupported", model: "gpt-5.6-sol", blue: true, programs: `["standard"]`, source: "not_added", reason: "capability_unavailable", kind: "missing"},
		{name: "prewarm", blue: true, fields: `,"generate":false`, source: "not_added", reason: "prewarm", kind: "missing"},
		{name: "control", blue: true, fields: `,"type":"response.cancel"`, source: "not_added", reason: "non_inference", kind: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account := &Account{ID: 8331, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAIDaybreakBlueEnabledKey: tc.blue, OpenAIDaybreakRedEnabledKey: tc.red}}
			if tc.programs != "" {
				manifest := `{"models":[{"slug":"` + tc.model + `","available_access_programs":{"cyber":` + tc.programs + `}}]}`
				svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(manifest), time.Now())
			}
			body := []byte(`{"model":"` + tc.model + `","input":"private prompt"` + tc.fields + `}`)
			wire, decision, err := svc.applyOpenAIDaybreakWithDecision(daybreakEnabledTestContext(), account, body)
			require.NoError(t, err)
			observation := observeOpenAIDaybreak(wire, decision)
			require.Equal(t, tc.source, observation.Source)
			require.Equal(t, tc.reason, observation.Reason)
			require.Equal(t, tc.kind, observation.CyberType)
			require.Equal(t, tc.kind != "missing", observation.CyberPresent)
			require.Equal(t, tc.value, observation.CyberValue)
			recorded, err := json.Marshal(observation)
			require.NoError(t, err)
			require.NotContains(t, string(recorded), "private prompt")
		})
	}
}

func TestFingerprintDaybreakBoundsAndUnknownEvidence(t *testing.T) {
	require.Nil(t, observeOpenAIDaybreak(nil, "automatic"))
	legacy := observeOpenAIDaybreak([]byte(`{"access_programs":{"cyber":"daybreak_blue"}}`))
	require.Equal(t, "unobserved", legacy.Source, "a value alone is not evidence of injection")
	changed := observeOpenAIDaybreak([]byte(`{}`), "automatic")
	require.Equal(t, "final_value_changed", changed.Reason)
	require.Equal(t, "unobserved", changed.Source)
	invalid := observeOpenAIDaybreak([]byte(`{"access_programs":{"cyber":{"private":"secret"}}}`), "client_supplied")
	require.Equal(t, "object", invalid.CyberType)
	require.Empty(t, invalid.CyberValue)
	long := observeOpenAIDaybreak([]byte(`{"access_programs":{"cyber":"`+strings.Repeat("蓝", 200)+`"}}`), "client_supplied")
	require.Len(t, []rune(long.CyberValue), 128)
	require.True(t, long.ValueTruncated)
}

func TestFingerprintDaybreakFreezesEachWSTurnAndSnapshot(t *testing.T) {
	enableDaybreakObservationTest(t)
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 8332, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"type":"response.create","access_programs":{"cyber":"daybreak_blue"}}`)
	publish := svc.freezeFingerprintObservationWSFrame(nil, account, nil, body, http.Header{}, nil, "automatic")
	client := svc.freezeFingerprintObservationWSFrame(nil, account, nil, body, http.Header{}, nil, "client_supplied")
	clear(body)
	require.Empty(t, SnapshotFingerprintObservations(10), "a prepared frame has not been sent")
	publish()
	client()
	publish()
	entries := SnapshotFingerprintObservations(10)
	require.Len(t, entries, 2)
	require.Equal(t, "client", entries[0].Daybreak.Source)
	require.Equal(t, "automatic", entries[1].Daybreak.Source)
	require.Equal(t, "daybreak_blue", entries[1].Daybreak.CyberValue)
	entries[1].Daybreak.Source = "tampered"
	require.Equal(t, "automatic", SnapshotFingerprintObservations(10)[1].Daybreak.Source)
	SetFingerprintObservationEnabled(false)
	require.Empty(t, SnapshotFingerprintObservations(10))
}
