package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func daybreakInboundModelTestAPIKey(blue, red bool) *Account {
	account := daybreakPolicyTestAccount(blue, red)
	account.Type = AccountTypeAPIKey
	return account
}

func TestOpenAIDaybreakInboundModelHTTPPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, frozen, public, handler, fallback, want string
		hasFrozen, hasHandler                         bool
	}{
		{name: "frozen model wins", frozen: "gpt-6-cyber", hasFrozen: true, public: "gpt-6-sol", handler: "unknown-alias", hasHandler: true, fallback: "gpt-6-sol", want: "daybreak_red"},
		{name: "Composite public model precedes handler", public: "gpt-6-cyber", handler: "gpt-6-sol", hasHandler: true, fallback: "gpt-6-sol", want: "daybreak_red"},
		{name: "handler model precedes fallback", handler: "gpt-6-cyber", hasHandler: true, fallback: "gpt-6-sol", want: "daybreak_red"},
		{name: "fallback when unset", fallback: "gpt-6-sol", want: "daybreak_blue"},
		{name: "explicit empty frozen model", hasFrozen: true, public: "gpt-6-cyber", handler: "gpt-6-cyber", hasHandler: true, fallback: "gpt-6-sol"},
		{name: "explicit empty handler model", hasHandler: true, fallback: "gpt-6-sol"},
		{name: "unknown frozen model remains authoritative", frozen: "public-alias", hasFrozen: true, public: "gpt-6-cyber", handler: "gpt-6-sol", hasHandler: true, fallback: "gpt-6-sol"},
		{name: "unknown Composite public model remains authoritative", public: "public-alias", handler: "gpt-6-cyber", hasHandler: true, fallback: "gpt-6-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := daybreakEnabledTestContext()
			if tc.public != "" {
				ctx = WithCompositeRouteDecision(ctx, CompositeRouteDecision{Matched: true, PublicModel: tc.public, UpstreamModel: "mapped-backend", TargetPlatform: PlatformOpenAI})
			}
			if tc.hasFrozen {
				ctx = withOpenAIDaybreakInboundModel(ctx, tc.frozen)
			}
			requestCtx := context.Background()
			if tc.hasHandler {
				requestCtx = context.WithValue(requestCtx, ctxkey.Model, tc.handler)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
			selected := withOpenAIDaybreakHTTPInboundModel(ctx, c, tc.fallback)
			body := []byte(`{"model":"arbitrary-backend","input":"hello"}`)
			wire, reason, err := daybreakPolicyTestService(true).applyOpenAIDaybreakWithDecision(selected, daybreakInboundModelTestAPIKey(true, true), body)
			require.NoError(t, err)
			if tc.want == "" {
				require.Equal(t, body, wire)
				require.Equal(t, "unrecognized_model", reason)
			} else {
				require.Equal(t, tc.want, gjson.GetBytes(wire, "access_programs.cyber").String())
				require.Equal(t, "automatic", reason)
			}
		})
	}
}

func TestOpenAIDaybreakInboundModelFallbackAndWSTurnIsolation(t *testing.T) {
	svc := daybreakPolicyTestService(true)
	account := daybreakInboundModelTestAPIKey(true, true)
	body := []byte(`{"type":"response.create","model":"mapped-backend","input":"hello"}`)
	shared := withOpenAIDaybreakInboundModelFallback(daybreakEnabledTestContext(), "gpt-6-sol")
	firstTurn := withOpenAIDaybreakInboundModelFallback(shared, "gpt-6-cyber")
	secondTurn := withOpenAIDaybreakInboundModel(firstTurn, "gpt-6-cyber")
	emptyTurn := withOpenAIDaybreakInboundModel(secondTurn, "")
	emptyTurn = withOpenAIDaybreakInboundModelFallback(emptyTurn, "gpt-6-sol")
	thirdTurn := withOpenAIDaybreakInboundModel(secondTurn, "gpt-6-sol")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "fallback cannot replace captured model", ctx: firstTurn, want: "daybreak_blue"},
		{name: "new WS turn replaces old model", ctx: secondTurn, want: "daybreak_red"},
		{name: "explicit empty WS turn cannot fall back", ctx: emptyTurn},
		{name: "later WS turn may replace Red with Blue", ctx: thirdTurn, want: "daybreak_blue"},
		{name: "shared context remains unchanged", ctx: shared, want: "daybreak_blue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(tc.ctx, account, body)
			require.NoError(t, err)
			if tc.want == "" {
				require.Equal(t, body, wire)
				require.Equal(t, "unrecognized_model", reason)
			} else {
				require.Equal(t, tc.want, gjson.GetBytes(wire, "access_programs.cyber").String())
				require.Equal(t, "automatic", reason)
			}
		})
	}
}

func TestOpenAIDaybreakInboundModelAPIKeyMappingPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, inbound, outbound, want string
	}{
		{name: "Red mapped to Blue model stays Red", inbound: "gpt-6-cyber", outbound: "gpt-6-sol", want: "daybreak_red"},
		{name: "Red mapped to arbitrary ID stays Red", inbound: "gpt-6-cyber", outbound: "deployment-production-42", want: "daybreak_red"},
		{name: "Blue mapped to Red model stays Blue", inbound: "gpt-6-sol", outbound: "gpt-6-cyber", want: "daybreak_blue"},
		{name: "Red tier Blue wire stays Blue", inbound: "gpt-6-astra", outbound: "gpt-6-cyber", want: "daybreak_blue"},
		{name: "unknown public alias mapped to known model stays unrecognized", inbound: "company-chat", outbound: "gpt-6-cyber"},
		{name: "unknown suffix is not classified by mapping", inbound: "gpt-6-cyber-custom", outbound: "gpt-6-cyber"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No model catalog or upstream client is configured for this API key.
			svc := daybreakPolicyTestService(true)
			body := []byte(fmt.Sprintf(`{"model":%q,"input":"hello"}`, tc.outbound))
			ctx := withOpenAIDaybreakInboundModel(daybreakEnabledTestContext(), tc.inbound)
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(ctx, daybreakInboundModelTestAPIKey(true, true), body)
			require.NoError(t, err)
			require.Equal(t, tc.outbound, gjson.GetBytes(wire, "model").String())
			if tc.want == "" {
				require.Equal(t, body, wire)
				require.Equal(t, "unrecognized_model", reason)
			} else {
				require.Equal(t, "automatic", reason)
				require.Equal(t, tc.want, gjson.GetBytes(wire, "access_programs.cyber").String())
			}
		})
	}
}

func TestOpenAIDaybreakInboundModelOAuthChecksOutboundProgramGrant(t *testing.T) {
	for _, tc := range []struct {
		name, inbound, outbound, outboundGrant, want string
	}{
		{name: "Red inbound uses mapped model Red grant", inbound: "gpt-6-cyber", outbound: "gpt-6-sol", outboundGrant: "daybreak_red", want: "daybreak_red"},
		{name: "incoming Red grant does not authorize mapped Blue-only model", inbound: "gpt-6-cyber", outbound: "gpt-6-sol", outboundGrant: "daybreak_blue"},
		{name: "Blue inbound accepts arbitrary mapped model Blue grant", inbound: "gpt-6-sol", outbound: "oauth-deployment-42", outboundGrant: "daybreak_blue", want: "daybreak_blue"},
		{name: "incoming Blue grant does not authorize mapped Red-only model", inbound: "gpt-6-sol", outbound: "gpt-6-cyber", outboundGrant: "daybreak_red"},
		{name: "mapped standard-only model has no selected grant", inbound: "gpt-6-cyber", outbound: "oauth-deployment-42", outboundGrant: "standard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := daybreakPolicyTestService(true)
			account := daybreakPolicyTestAccount(true, true)
			manifest := fmt.Sprintf(`{"models":[{"slug":%q,"available_access_programs":{"cyber":["daybreak_blue","daybreak_red"]}},{"slug":%q,"available_access_programs":{"cyber":[%q]}}]}`, tc.inbound, tc.outbound, tc.outboundGrant)
			svc.codexModelCapabilities.observeManifest(openAIDaybreakCapabilitiesNamespace(account), []byte(manifest), time.Now())
			body := []byte(fmt.Sprintf(`{"model":%q,"input":"hello"}`, tc.outbound))
			ctx := withOpenAIDaybreakInboundModel(daybreakEnabledTestContext(), tc.inbound)
			wire, reason, err := svc.applyOpenAIDaybreakWithDecision(ctx, account, body)
			require.NoError(t, err)
			if tc.want == "" {
				require.Equal(t, body, wire)
				require.Equal(t, "capability_unavailable", reason)
			} else {
				require.Equal(t, "automatic", reason)
				require.Equal(t, tc.want, gjson.GetBytes(wire, "access_programs.cyber").String())
			}
		})
	}
}

func TestOpenAIDaybreakInboundModelAccountAndGroupGates(t *testing.T) {
	for _, tc := range []struct {
		name, inbound, outbound, reason              string
		accountBlue, accountRed, groupBlue, groupRed bool
	}{
		{name: "incoming Red needs account Red after mapping to Blue", inbound: "gpt-6-cyber", outbound: "gpt-6-sol", accountBlue: true, groupBlue: true, groupRed: true, reason: "red_disabled"},
		{name: "incoming Red needs group Red after mapping to Blue", inbound: "gpt-6-cyber", outbound: "gpt-6-sol", accountBlue: true, accountRed: true, groupBlue: true, reason: "group_red_disabled"},
		{name: "incoming Red still requires account Blue", inbound: "gpt-6-cyber", outbound: "mapped-backend", accountRed: true, groupBlue: true, groupRed: true, reason: "blue_disabled"},
		{name: "incoming Red still requires group Blue", inbound: "gpt-6-cyber", outbound: "mapped-backend", accountBlue: true, accountRed: true, groupRed: true, reason: "group_blue_disabled"},
		{name: "incoming Blue needs no Red gates after mapping to Red", inbound: "gpt-6-sol", outbound: "gpt-6-cyber", accountBlue: true, groupBlue: true, reason: "automatic"},
		{name: "Astra still needs account Red although wire program is Blue", inbound: "gpt-6-astra", outbound: "gpt-6-sol", accountBlue: true, groupBlue: true, groupRed: true, reason: "red_disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withOpenAIDaybreakInboundModel(daybreakTestContext(daybreakTestGroup(tc.groupBlue, tc.groupRed)), tc.inbound)
			body := []byte(fmt.Sprintf(`{"model":%q}`, tc.outbound))
			wire, reason, err := daybreakPolicyTestService(true).applyOpenAIDaybreakWithDecision(ctx, daybreakInboundModelTestAPIKey(tc.accountBlue, tc.accountRed), body)
			require.NoError(t, err)
			require.Equal(t, tc.reason, reason)
			if tc.reason == "automatic" {
				require.Equal(t, "daybreak_blue", gjson.GetBytes(wire, "access_programs.cyber").String())
			} else {
				require.Equal(t, body, wire)
			}
		})
	}
}

func TestOpenAIDaybreakInboundModelPreservesClientValuesAndRetrySource(t *testing.T) {
	svc := daybreakPolicyTestService(true)
	account := daybreakInboundModelTestAPIKey(true, true)
	redCtx := withOpenAIDaybreakInboundModel(daybreakEnabledTestContext(), "gpt-6-cyber")
	blueCtx := withOpenAIDaybreakInboundModel(redCtx, "gpt-6-sol")
	for _, programs := range []string{`{"cyber":"standard"}`, `{"cyber":"daybreak_blue"}`, `{"cyber":null}`, `null`, `[]`} {
		body := []byte(fmt.Sprintf(`{"model":"gpt-6-sol","access_programs":%s}`, programs))
		wire, reason, err := svc.applyOpenAIDaybreakWithDecision(redCtx, account, body)
		require.NoError(t, err)
		require.Equal(t, body, wire)
		require.NotEqual(t, "automatic", reason)
	}
	body := []byte(`{ "model":"mapped-backend", "opaque":9007199254740993,"input":"retry me"}`)
	original := string(body)
	redWire, _, err := svc.applyOpenAIDaybreakWithDecision(redCtx, account, body)
	require.NoError(t, err)
	blueWire, _, err := svc.applyOpenAIDaybreakWithDecision(blueCtx, account, body)
	require.NoError(t, err)
	require.Equal(t, original, string(body))
	require.Equal(t, "daybreak_red", gjson.GetBytes(redWire, "access_programs.cyber").String())
	require.Equal(t, "daybreak_blue", gjson.GetBytes(blueWire, "access_programs.cyber").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(redWire, "opaque").Raw)
	require.Equal(t, "9007199254740993", gjson.GetBytes(blueWire, "opaque").Raw)
	require.False(t, gjson.GetBytes(body, "access_programs").Exists())
}
