package service

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenCodeRememberedSessionOwnsOnlyScalar(t *testing.T) {
	c := newOpenCodeSessionTestContext(t, "")
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	body := []byte(`{"prompt_cache_key":"small-session","input":"` + strings.Repeat("x", 1<<20) + `"}`)
	rememberOpenCodeInboundBody(c, account, body)
	value, ok := c.Get(openCodeInboundSessionContextKey)
	require.True(t, ok)
	require.Equal(t, "small-session", value)
	require.Len(t, c.Keys, 1, "the request context must not retain the payload alongside its scalar")

	// The raw JSON view borrows body storage; the saved scalar must not.
	start := bytes.Index(body, []byte("small-session"))
	require.NotEqual(t, -1, start)
	copy(body[start:], "other-session")
	require.Equal(t, "small-session", c.GetString(openCodeInboundSessionContextKey))

	for _, account := range []*Account{
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	} {
		rememberOpenCodeInboundBody(c, account, body)
		require.Empty(t, c.GetString(openCodeInboundSessionContextKey), "non-OpenCode accounts clear previous attempt state without retaining the payload")
		headers := make(http.Header)
		applyOpenCodeSessionHeader(c, account, "https://api.example.com/v1/responses", headers)
		require.Empty(t, headers, "ordinary OpenAI attempts must not acquire OpenCode headers")
	}
}

func TestOpenCodeRememberedSessionMatchesBodyPrecedence(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	for _, tc := range []struct {
		name    string
		body    string
		headers http.Header
	}{
		{name: "prompt cache", body: `{"prompt_cache_key":"inbound"}`},
		{name: "metadata", body: `{"metadata":{"user_id":"metadata-session"}}`},
		{name: "nested metadata", body: `{"metadata":{"user_id":"{\"session_id\":\"nested-session\"}"}}`},
		{name: "prompt before metadata", body: `{"prompt_cache_key":"prompt","metadata":{"user_id":"metadata"}}`},
		{name: "blank prompt falls back to metadata", body: `{"prompt_cache_key":" ","metadata":{"user_id":"metadata"}}`},
		{name: "invalid prompt does not fall back to metadata", body: `{"prompt_cache_key":"a\nb","metadata":{"user_id":"metadata"}}`},
		{name: "oversize prompt", body: `{"prompt_cache_key":"` + strings.Repeat("x", 256) + `"}`},
		{name: "missing session", body: `{"input":"hello"}`},
		{name: "explicit OpenCode header wins", body: `{"prompt_cache_key":"inbound"}`, headers: http.Header{openCodeSessionHeader: {"caller"}}},
		{name: "explicit session header wins", body: `{"prompt_cache_key":"inbound"}`, headers: http.Header{"Session_id": {"caller-session"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newOpenCodeSessionTestContext(t, "")
			legacyContext := newOpenCodeSessionTestContext(t, "")
			for name, values := range tc.headers {
				for _, value := range values {
					c.Request.Header.Add(name, value)
					legacyContext.Request.Header.Add(name, value)
				}
			}
			inbound := []byte(tc.body)
			for _, converted := range [][]byte{[]byte(`{"prompt_cache_key":"converted"}`), []byte(`{"input":"hello"}`)} {
				outboundHeaders := http.Header{openCodeSessionHeader: {"account-override"}}
				want := resolveOpenCodeSessionID(legacyContext, outboundHeaders, false, inbound, converted)
				rememberOpenCodeInboundBody(c, account, inbound)
				require.Equal(t, want, resolveOpenCodeSessionID(c, outboundHeaders, false, converted))
			}
		})
	}
}

func TestOpenCodeRememberedSessionReplacedAcrossAttempts(t *testing.T) {
	c := newOpenCodeSessionTestContext(t, "")
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	headers := http.Header{openCodeSessionHeader: {"account-override"}}
	for _, tc := range []struct {
		body []byte
		want string
	}{
		{[]byte(`{"prompt_cache_key":"first-attempt"}`), "first-attempt"},
		{[]byte(`{"metadata":{"user_id":"second-attempt"}}`), "second-attempt"},
		{[]byte(`{"input":"no session"}`), ""},
		{[]byte(`{"prompt_cache_key":"third-attempt"}`), "third-attempt"},
		{nil, ""},
		{[]byte(`{"prompt_cache_key":"fourth-attempt"}`), "fourth-attempt"},
		{[]byte{}, ""},
		{[]byte(`{"prompt_cache_key":"fifth-attempt"}`), "fifth-attempt"},
		{[]byte(`{"prompt_cache_key":"bad\nvalue"}`), ""},
	} {
		rememberOpenCodeInboundBody(c, account, tc.body)
		require.Equal(t, tc.want, c.GetString(openCodeInboundSessionContextKey))
		want := tc.want
		if want == "" {
			want = "account-override"
			require.Equal(t, "converted", resolveOpenCodeSessionID(c, headers, false, []byte(`{"prompt_cache_key":"converted"}`)))
		}
		require.Equal(t, want, resolveOpenCodeSessionID(c, headers, false, []byte(`{"input":"no session"}`)))
	}
	rememberOpenCodeInboundBody(nil, account, []byte(`{"prompt_cache_key":"no context"}`))
}

func TestOpenCodeRememberedSessionAccountGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "nil"},
		{name: "OAuth", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
		{name: "default OpenAI API Key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
		{name: "ordinary custom API Key", account: openCodeSessionTestAccount("https://api.example.com/v1")},
		{name: "default Grok API Key", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}},
		{name: "explicit Grok API Key", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.x.ai/v1"}}},
		{name: "Grok custom relay", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example.com/v1"}}},
		{name: "Grok OpenCode relay", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://opencode.ai/zen/v1"}}, want: true},
		{name: "official OpenCode API Key", account: openCodeSessionTestAccount("https://opencode.ai/zen/v1"), want: true},
		{name: "OpenCode Go relay", account: &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example.com/v1"}}, want: true},
		{name: "adaptive unknown final endpoint", account: &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_protocol": APIProtocolAdaptive}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newOpenCodeSessionTestContext(t, "")
			c.Set(openCodeInboundSessionContextKey, "stale")
			rememberOpenCodeInboundBody(c, tc.account, []byte(`{"prompt_cache_key":"current"}`))
			if tc.want {
				require.Equal(t, "current", c.GetString(openCodeInboundSessionContextKey))
			} else {
				require.Empty(t, c.GetString(openCodeInboundSessionContextKey))
			}
		})
	}
}
