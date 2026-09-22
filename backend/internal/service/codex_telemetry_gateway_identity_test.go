package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryGatewayFreezesSamplingAndRoute(t *testing.T) {
	account := osIdentityTestAccount(t, 83)
	proxyID := int64(7)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "127.0.0.1", Port: 8111}
	c := osIdentityTestContext(t, "")
	profile := account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux]
	plan := OpenAIOAuthIdentityPlan{OSOwnerID: account.ID, OSFamily: profile.OSFamily, OSProfile: profile}
	first := withCodexTelemetryGatewayContext(context.Background(), c, account, "ws:1", &plan)
	one := first.Value(codexTelemetryGatewayContextKey{}).(codexTelemetryGatewaySnapshot)
	account.Proxy = &Proxy{ID: 8, Protocol: "http", Host: "127.0.0.2", Port: 8112}
	second := withCodexTelemetryGatewayContext(context.Background(), c, account, "ws:1", &plan)
	two := second.Value(codexTelemetryGatewayContextKey{}).(codexTelemetryGatewaySnapshot)
	require.Equal(t, one, two, "retry must preserve sampling and physical route")
	require.NotEqual(t, plan.RequestTurn.ID, one.samplingID)
	third := withCodexTelemetryGatewayContext(context.Background(), c, account, "ws:2", &plan)
	three := third.Value(codexTelemetryGatewayContextKey{}).(codexTelemetryGatewaySnapshot)
	require.NotEqual(t, one.samplingID, three.samplingID, "same turn ID can contain a new sampling")
	require.Equal(t, one.route, three.route)
}

func TestCodexTelemetryGatewayUsesWireInstallationAndOS(t *testing.T) {
	telemetry := NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	account := osIdentityTestAccount(t, 84)
	account.Extra[openAIInstallationPinEnabledKey] = true
	svc := &OpenAIGatewayService{codexTelemetry: telemetry}
	for _, os := range OpenAIOAuthOSFamilies() {
		t.Run(os, func(t *testing.T) {
			profile := account.OpenAIOAuthOSProfiles.Profiles[os]
			plan := OpenAIOAuthIdentityPlan{OSOwnerID: account.ID, OSFamily: os, OSProfile: profile}
			c := osIdentityTestContext(t, "")
			ctx := withCodexTelemetryGatewayContext(context.Background(), c, account, "http", &plan)
			headers := http.Header{"User-Agent": {profile.UserAgent}, "Authorization": {"Bearer test-token"}, "Chatgpt-Account-Id": {"test-account"}}
			body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "client_metadata": map[string]string{codexInstallationIDKey: profile.InstallationID}})
			require.NoError(t, err)
			attempt := svc.beginCodexTelemetryFromWire(ctx, account, headers, body, "", false)
			require.NotNil(t, attempt)
			require.Equal(t, os, attempt.profile.input.OSFamily)
			require.Equal(t, profile.InstallationID, attempt.profile.input.InstallationID)
			require.True(t, attempt.profile.input.ManagedInstallation)
			require.Equal(t, account.ID, attempt.profile.input.OwnerAccountID)
			require.Equal(t, profile.UserAgent, attempt.profile.input.nativeHTTPScope.SourceUserAgent)

			missing := svc.beginCodexTelemetryFromWire(ctx, account, headers, []byte(`{"model":"gpt-6-astra"}`), "", false)
			require.NotNil(t, missing)
			require.Empty(t, missing.profile.input.InstallationID, "never synthesize a missing wire installation ID")
			require.False(t, missing.profile.input.ManagedInstallation)

			headers.Set("User-Agent", "codex-tui/0.155.1 (unknown; x86_64)")
			unknown := svc.beginCodexTelemetryFromWire(ctx, account, headers, body, "", false)
			require.NotNil(t, unknown)
			require.Equal(t, "unknown", unknown.profile.input.OSFamily)
			require.False(t, unknown.profile.input.ManagedInstallation)
		})
	}
	account.Extra[openAIInstallationPinEnabledKey] = false
	profile := account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows]
	ctx := withCodexTelemetryGatewayContext(context.Background(), osIdentityTestContext(t, ""), account, "http", &OpenAIOAuthIdentityPlan{OSOwnerID: account.ID, OSFamily: profile.OSFamily, OSProfile: profile})
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "client_metadata": map[string]string{codexInstallationIDKey: profile.InstallationID}})
	require.NoError(t, err)
	attempt := svc.beginCodexTelemetryFromWire(ctx, account, http.Header{"User-Agent": {profile.UserAgent}, "Authorization": {"Bearer test-token"}, "Chatgpt-Account-Id": {"test-account"}}, body, "", false)
	require.NotNil(t, attempt)
	require.False(t, attempt.profile.input.ManagedInstallation, "matching client-supplied ID is not proof that pinning was enabled")
}

func TestCodexTelemetryGatewaySparkOwnerAndAccountBoundary(t *testing.T) {
	telemetry := NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	owner := osIdentityTestAccount(t, 91)
	shadow := &Account{ID: 92, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &owner.ID}
	repo := newAuthorizedOpenAIOAuthTestRepo(owner)
	var err error
	shadow, err = ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, shadow, OpenAIOSMacOS)
	require.NoError(t, err)
	svc := &OpenAIGatewayService{codexTelemetry: telemetry, accountRepo: repo}
	profile := owner.OpenAIOAuthOSProfiles.Profiles[OpenAIOSMacOS]
	ctx := withCodexTelemetryGatewayContext(context.Background(), osIdentityTestContext(t, ""), shadow, "http", &OpenAIOAuthIdentityPlan{OSOwnerID: owner.ID, OSFamily: profile.OSFamily, OSProfile: profile, CredentialOS: OpenAIOSMacOS, AuthorizationGeneration: shadow.OpenAIOAuthAuthorizationGeneration})
	attempt := svc.beginCodexTelemetryFromWire(ctx, shadow, http.Header{"User-Agent": {profile.UserAgent}, "Authorization": {"Bearer test-token"}, "Chatgpt-Account-Id": {"test-account"}}, []byte(`{"model":"gpt-6-astra"}`), "", false)
	require.NotNil(t, attempt)
	require.Equal(t, owner.ID, attempt.profile.input.OwnerAccountID)
	require.Equal(t, shadow.ID, attempt.profile.input.AccountID)
	owner.Credentials[openAIAuthModeLegacyCredentialKey] = OpenAIAuthModeAgentIdentity
	require.Nil(t, svc.beginCodexTelemetryFromWire(ctx, shadow, nil, []byte(`{"model":"gpt-6-astra"}`), "", false))
}

func TestCodexTelemetryShellOnlyReadsCurrentStandaloneEnvironment(t *testing.T) {
	message := func(text string) map[string]any {
		return map[string]any{"role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}
	}
	const env = `<environment_context><cwd>C:\work</cwd><shell>C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe</shell></environment_context>`
	for _, test := range []struct {
		name  string
		input []map[string]any
		want  string
	}{
		{"current", []map[string]any{message(env)}, "powershell"},
		{"quoted", []map[string]any{message("Here is a quote: " + env)}, ""},
		{"history", []map[string]any{message(env), {"role": "assistant", "content": "done"}, message("new request")}, ""},
		{"newest wins", []map[string]any{message(env), message(`<environment_context><shell>/bin/zsh</shell></environment_context>`)}, "zsh"},
		{"malformed blocks older", []map[string]any{message(env), message(`<environment_context><shell>bash</shell>`)}, ""},
		{"nested shell", []map[string]any{message(`<environment_context><example><shell>bash</shell></example></environment_context>`)}, ""},
		{"multiple shell", []map[string]any{message(`<environment_context><shell>bash</shell><shell>zsh</shell></environment_context>`)}, ""},
		{"arbitrary command", []map[string]any{message(`<environment_context><shell>bash -c whoami</shell></environment_context>`)}, ""},
		{"skill label", []map[string]any{{"role": "user", "content": env, "metadata": map[string]any{"content_item_kinds": []string{"skills.skill"}}}}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"input": test.input})
			require.NoError(t, err)
			require.Equal(t, test.want, codexTelemetryShellFromBody(body))
		})
	}
}

func TestCodexTelemetryReturnedToolsExcludeHistory(t *testing.T) {
	oldID, newID := uuid.NewString(), uuid.NewString()
	body, err := json.Marshal(map[string]any{"input": []map[string]any{
		{"type": "function_call_output", "call_id": oldID, "output": "private history"},
		{"role": "user", "content": "next sampling"},
		{"type": "function_call_output", "call_id": newID, "output": "private output"},
		{"type": "custom_tool_call_output", "call_id": newID, "output": "duplicate"},
	}})
	require.NoError(t, err)
	require.Equal(t, []string{newID}, codexTelemetryReturnedToolCallIDs(body))
}

func TestCodexTelemetryGatewayIdentityFailuresRemainLocal(t *testing.T) {
	for _, reason := range []string{"identity_unavailable", "stale_identity", "proxy_unavailable"} {
		t.Run(reason, func(t *testing.T) {
			telemetry := NewCodexTelemetryService(nil)
			t.Cleanup(telemetry.Stop)
			account := osIdentityTestAccount(t, 97)
			svc := &OpenAIGatewayService{codexTelemetry: telemetry}
			ctx := context.Background()
			proxyURL := ""
			switch reason {
			case "identity_unavailable":
				parentID := int64(98)
				account.ParentAccountID = &parentID
				svc.accountRepo = &outboundIdentityAccountRepoStub{accounts: map[int64]*Account{}}
			case "stale_identity":
				ctx = context.WithValue(ctx, codexTelemetryGatewayContextKey{}, codexTelemetryGatewaySnapshot{ownerID: 99})
			case "proxy_unavailable":
				proxyURL = "http://PRIVATE_PROXY_USER:PRIVATE_PROXY_PASSWORD@127.0.0.1:9000"
				ctx = context.WithValue(ctx, codexTelemetryGatewayContextKey{}, codexTelemetryGatewaySnapshot{ownerID: account.ID})
			}
			ua := account.OpenAIOAuthOSProfiles.Profiles[OpenAIOSLinux].UserAgent
			headers := http.Header{"User-Agent": {ua}, "Authorization": {"Bearer PRIVATE_TOKEN"}, "Chatgpt-Account-Id": {"PRIVATE_ACCOUNT"}}
			body := []byte(`{"model":"PRIVATE_MODEL","input":"PRIVATE_BODY"}`)
			require.Nil(t, svc.beginCodexTelemetryFromWire(ctx, account, headers, body, proxyURL, false))
			snapshot := telemetry.Observations(CodexTelemetryObservationQuery{})
			require.Len(t, snapshot.Items, 1)
			entry := snapshot.Items[0]
			require.Equal(t, account.ID, entry.AccountID)
			require.Equal(t, account.Name, entry.AccountName)
			require.Equal(t, ua, entry.UserAgent)
			require.Equal(t, OpenAIOSLinux, entry.OSFamily)
			require.Equal(t, "skipped", entry.Status)
			require.Equal(t, "observed", entry.Source)
			require.Equal(t, reason, entry.Error)
			require.Equal(t, []string{reason}, entry.Reasons)
			require.Empty(t, entry.PoolID)
			require.Empty(t, entry.BatchID)
			require.False(t, entry.ContainsSimulated)
			encoded, err := json.Marshal(snapshot)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "PRIVATE_")
			require.EqualValues(t, 1, snapshot.Counters.Skipped)
			require.Zero(t, snapshot.Counters.Queued)
			store := telemetry.store.(*MemoryCodexTelemetryStore)
			store.mu.Lock()
			poolCount, batchCount := len(store.pools), len(store.batches)
			store.mu.Unlock()
			require.Zero(t, poolCount)
			require.Zero(t, batchCount)
		})
	}
}

func TestCodexTelemetryGatewayExcludedRequestsDoNotRecordIdentityFailure(t *testing.T) {
	telemetry := NewCodexTelemetryService(nil)
	t.Cleanup(telemetry.Stop)
	account := osIdentityTestAccount(t, 97)
	parentID := int64(98)
	account.ParentAccountID = &parentID
	svc := &OpenAIGatewayService{codexTelemetry: telemetry, accountRepo: &outboundIdentityAccountRepoStub{accounts: map[int64]*Account{}}}
	require.Nil(t, svc.beginCodexTelemetryFromWire(context.Background(), account, nil, []byte(`{"request_kind":"prewarm"}`), "", false))
	account.ParentAccountID = nil
	account.Credentials[openAIAuthModeLegacyCredentialKey] = OpenAIAuthModeAgentIdentity
	require.Nil(t, svc.beginCodexTelemetryFromWire(context.Background(), account, nil, []byte(`{"model":"gpt-6-astra"}`), "", false))
	require.Empty(t, telemetry.Observations(CodexTelemetryObservationQuery{}).Items)
}
