package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func daybreakTestContext(group *Group) context.Context {
	return context.WithValue(context.Background(), ctxkey.Group, group)
}

func daybreakTestGroup(blue, red bool) *Group {
	return &Group{
		ID: 321, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
		OpenAIDaybreakBlueEnabled: blue, OpenAIDaybreakRedEnabled: red,
	}
}

func daybreakEnabledTestContext() context.Context {
	return daybreakTestContext(daybreakTestGroup(true, true))
}

func daybreakPolicyTestService(enabled bool) *OpenAIGatewayService {
	return &OpenAIGatewayService{settingService: NewSettingService(&dailyRotationSettingRepo{
		values: map[string]string{SettingKeyOpenAIDaybreakEnabled: fmt.Sprint(enabled)},
	}, nil)}
}

func daybreakPolicyTestAccount(blue, red bool) *Account {
	return &Account{ID: 802, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		OpenAIDaybreakBlueEnabledKey: blue, OpenAIDaybreakRedEnabledKey: red,
	}}
}

func TestOpenAIDaybreakPolicyRequiresSystemGroupAndAccountTier(t *testing.T) {
	for _, tc := range []struct {
		name, model, want                            string
		groupBlue, groupRed, accountBlue, accountRed bool
	}{
		{name: "all Blue enabled", model: "gpt-6-sol", groupBlue: true, accountBlue: true, want: "daybreak_blue"},
		{name: "group default off", model: "gpt-6-sol", accountBlue: true, accountRed: true},
		{name: "account default off", model: "gpt-6-sol", groupBlue: true, groupRed: true},
		{name: "group Red without Blue", model: "gpt-6-astra", groupRed: true, accountBlue: true, accountRed: true},
		{name: "account Red without Blue", model: "gpt-6-astra", groupBlue: true, groupRed: true, accountRed: true},
		{name: "Astra group requires Red", model: "gpt-6-astra", groupBlue: true, accountBlue: true, accountRed: true},
		{name: "Astra account requires Red", model: "gpt-6-astra", groupBlue: true, groupRed: true, accountBlue: true},
		{name: "Astra Red tier sends Blue", model: "gpt-6-astra", groupBlue: true, groupRed: true, accountBlue: true, accountRed: true, want: "daybreak_blue"},
		{name: "6.1 Sol Red tier sends Blue", model: "gpt-6.1-sol", groupBlue: true, groupRed: true, accountBlue: true, accountRed: true, want: "daybreak_blue"},
		{name: "Cyber requires group Red", model: "gpt-5.6-cyber", groupBlue: true, accountBlue: true, accountRed: true},
		{name: "Cyber all Red enabled", model: "gpt-5.6-cyber", groupBlue: true, groupRed: true, accountBlue: true, accountRed: true, want: "daybreak_red"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := daybreakPolicyTestService(true)
			account := daybreakPolicyTestAccount(tc.accountBlue, tc.accountRed)
			manifest := fmt.Sprintf(`{"models":[{"slug":%q,"available_access_programs":{"cyber":["daybreak_blue","daybreak_red"]}}]}`, tc.model)
			svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(manifest), time.Now())
			body := []byte(fmt.Sprintf(`{"model":%q,"input":"history","opaque":9007199254740993}`, tc.model))
			before := string(body)
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(daybreakTestGroup(tc.groupBlue, tc.groupRed)), account, body)
			require.NoError(t, err)
			require.Equal(t, before, string(body), "the retry source remains unchanged")
			require.Equal(t, "9007199254740993", gjson.GetBytes(wire, "opaque").Raw)
			if tc.want == "" {
				require.Equal(t, body, wire)
				require.NotEqual(t, "automatic", reason)
			} else {
				require.Equal(t, tc.want, gjson.GetBytes(wire, "access_programs.cyber").String())
				require.Equal(t, "automatic", reason)
			}
		})
	}
}

func TestOpenAIDaybreakPolicyUsesAuthorizedGroupNotAccountMemberships(t *testing.T) {
	svc := daybreakPolicyTestService(true)
	account := daybreakPolicyTestAccount(true, true)
	svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(account), []byte(`{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`), time.Now())
	allowed := daybreakTestGroup(true, true)
	blocked := daybreakTestGroup(false, false)
	blocked.ID++
	account.Groups = []*Group{allowed, blocked}
	account.GroupIDs = []int64{allowed.ID, blocked.ID}
	body := []byte(`{"model":"gpt-6-sol"}`)

	for _, group := range []*Group{nil, {ID: allowed.ID}, blocked} {
		wire, _, err := svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(group), account, body)
		require.NoError(t, err)
		require.Equal(t, body, wire, "account membership cannot grant the request a group preference")
	}
	wire, reason, err := svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(allowed), account, body)
	require.NoError(t, err)
	require.Equal(t, "automatic", reason)
	require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String())

	composite := *allowed
	composite.Platform = PlatformComposite
	composite.ID += 10
	account.Groups = []*Group{blocked}
	wire, reason, err = svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(&composite), account, body)
	require.NoError(t, err)
	require.Equal(t, "automatic", reason, "Composite parent authorizes regardless of child account groups")
	require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String())

}

func TestOpenAIDaybreakPolicyRejectsInvalidRequestGroupWithoutCatalog(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[]}`)
	svc := daybreakPolicyTestService(true)
	account := newCodexModelsTestAccount()
	account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}
	registerAuxiliaryOSFixture(t, svc, account)
	body := []byte(`{"model":"gpt-6-sol"}`)
	for _, mutate := range []func(*Group){
		func(g *Group) { g.ID = 0 },
		func(g *Group) { g.Hydrated = false },
		func(g *Group) { g.Status = "inactive" },
		func(g *Group) { g.Platform = PlatformAnthropic },
	} {
		group := daybreakTestGroup(true, true)
		mutate(group)
		wire, reason, err := svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(group), account, body)
		require.NoError(t, err)
		require.Equal(t, body, wire)
		require.Equal(t, "group_unavailable", reason)
	}
	require.Zero(t, calls.Load())
}

func TestOpenAIDaybreakPolicyGlobalOffScrubsEveryOpenAIAccount(t *testing.T) {
	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{Platform: PlatformOpenAI, Type: AccountTypeSetupToken},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{"openai_api_key_mode": "codex_engine"}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModePersonalAccessToken}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity}},
	} {
		t.Run(account.Type+"/"+fmt.Sprint(account.Extra)+"/"+fmt.Sprint(account.Credentials), func(t *testing.T) {
			svc := daybreakPolicyTestService(false)
			body := []byte(`{"model":"gpt-6-sol","access_programs":{"cyber":null,"other":"keep"},"generate":false}`)
			before := string(body)
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(context.Background(), account, body)
			require.NoError(t, err)
			require.Equal(t, before, string(body))
			require.False(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
			require.Equal(t, "keep", gjson.GetBytes(wire, "access_programs.other").String())
			require.Equal(t, "global_disabled_stripped", reason)
			observation := observeOpenAIDaybreak(wire, reason)
			require.False(t, observation.CyberPresent)
			require.Equal(t, "global_disabled_stripped", observation.Reason)
			require.NotEqual(t, "client", observation.Source)
		})
	}
	for _, account := range []*Account{nil, {Platform: PlatformAnthropic, Type: AccountTypeOAuth}, {Platform: PlatformGemini, Type: AccountTypeAPIKey}} {
		body := []byte(`{"access_programs":{"cyber":"daybreak_blue"}}`)
		wire, _, err := daybreakPolicyTestService(false).applyOpenAIDaybreakWithDecision(daybreakEnabledTestContext(), account, body)
		require.NoError(t, err)
		require.Equal(t, body, wire, "global OpenAI setting must not rewrite other platforms")
	}
}

func TestOpenAIDaybreakPolicySkippedPathsDoNotQueryCatalog(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[]}`)
	for _, tc := range []struct {
		name                         string
		enabled, groupBlue, groupRed bool
		model, extra, reason         string
	}{
		{name: "system off", model: "gpt-6-sol", reason: "global_disabled"},
		{name: "system off explicit", model: "gpt-6-sol", extra: `,"access_programs":{"cyber":null}`, reason: "global_disabled_stripped"},
		{name: "group Blue off", enabled: true, model: "gpt-6-sol", reason: "group_blue_disabled"},
		{name: "group Red off", enabled: true, groupBlue: true, model: "gpt-6-astra", reason: "group_red_disabled"},
		{name: "explicit client field", enabled: true, groupBlue: true, groupRed: true, model: "gpt-6-sol", extra: `,"access_programs":{"cyber":"client-choice"}`, reason: "client_supplied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := daybreakPolicyTestService(tc.enabled)
			account := newCodexModelsTestAccount()
			account.Extra = map[string]any{OpenAIDaybreakBlueEnabledKey: true, OpenAIDaybreakRedEnabledKey: true}
			registerAuxiliaryOSFixture(t, svc, account)
			body := []byte(fmt.Sprintf(`{"model":%q%s}`, tc.model, tc.extra))
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(daybreakTestGroup(tc.groupBlue, tc.groupRed)), account, body)
			require.NoError(t, err)
			require.Equal(t, tc.reason, reason)
			if tc.enabled && tc.extra != "" {
				require.Equal(t, body, wire)
			}
		})
	}
	require.Zero(t, calls.Load())
}

func TestOpenAIDaybreakPolicyKeepsClientValuesWhenOnlyGroupDisabled(t *testing.T) {
	svc := daybreakPolicyTestService(true)
	account := daybreakPolicyTestAccount(true, true)
	for _, raw := range []string{`null`, `false`, `42`, `"daybreak_red"`, `"unexpected"`, `{}`, `[]`} {
		body := []byte(`{"model":"gpt-6-astra","access_programs":{"cyber":` + raw + `,"other":true}}`)
		wire, _, err := svc.applyOpenAIDaybreakWithDecision(daybreakTestContext(daybreakTestGroup(false, false)), account, body)
		require.NoError(t, err)
		require.Equal(t, body, wire)
		require.Same(t, &body[0], &wire[0])
	}
}

func TestOpenAIDaybreakPolicyDisablePrecedesExcludedEndpointAndMalformedInput(t *testing.T) {
	svc := daybreakPolicyTestService(false)
	account := daybreakPolicyTestAccount(false, false)
	ctx := withOpenAIDaybreakInjectionDisabled(context.Background())
	body := []byte(`{"access_programs":{"cyber":"daybreak_red"},"input":[]}`)
	wire, reason, err := svc.applyOpenAIDaybreakWithDecision(ctx, account, body)
	require.NoError(t, err)
	require.Equal(t, "global_disabled_stripped", reason, "count/compact exclusion must not bypass forced removal")
	require.JSONEq(t, `{"access_programs":{},"input":[]}`, string(wire))

	wire, _, err = svc.applyOpenAIDaybreakWithDecision(ctx, account, []byte(`{"access_programs":{"cyber":true}`))
	require.Error(t, err)
	require.Nil(t, wire)
}

func TestOpenAIDaybreakPolicyRetryAndPreferenceChangesDoNotContaminateSource(t *testing.T) {
	svc := daybreakPolicyTestService(true)
	first := daybreakPolicyTestAccount(true, true)
	second := daybreakPolicyTestAccount(false, false)
	second.ID++
	svc.codexModelCapabilities.observeManifest(openAICodexModelCapabilitiesNamespace(first), []byte(`{"models":[{"slug":"gpt-6-sol","available_access_programs":{"cyber":["daybreak_blue"]}}]}`), time.Now())
	body := []byte(`{"model":"gpt-6-sol","input":[{"encrypted_content":"keep"}]}`)
	original := string(body)
	group := daybreakTestGroup(true, true)
	ctx := daybreakTestContext(group)
	wire, reason, err := svc.applyOpenAIDaybreakWithDecision(ctx, first, body)
	require.NoError(t, err)
	require.Equal(t, "automatic", reason)
	require.True(t, gjson.GetBytes(wire, "access_programs.cyber").Exists())
	require.Equal(t, original, string(body))
	wire, _, err = svc.applyOpenAIDaybreakWithDecision(ctx, second, body)
	require.NoError(t, err)
	require.Equal(t, original, string(wire), "retry on disabled account starts from clean input")
	group.OpenAIDaybreakBlueEnabled = false
	group.OpenAIDaybreakRedEnabled = false
	wire, _, err = svc.applyOpenAIDaybreakWithDecision(ctx, first, body)
	require.NoError(t, err)
	require.Equal(t, original, string(wire), "group preference changes do not inherit previous injection")
	group.OpenAIDaybreakBlueEnabled = true
	wire, reason, err = svc.applyOpenAIDaybreakWithDecision(ctx, first, body)
	require.NoError(t, err)
	require.Equal(t, "automatic", reason)
	require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String())
	require.Equal(t, original, string(body))
}
