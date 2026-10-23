package proxyurl

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseShadowsocksSIP002(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	for _, tc := range []struct{ name, raw, method, password, host string }{
		{"plain", "ss://aes-128-gcm:p%40ss%3Aword%2F%23@ss.example:8388/#node", "aes-128-gcm", "p@ss:word/#", "ss.example:8388"},
		{"base64", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret")) + "@ss.example:8388", "chacha20-ietf-poly1305", "secret", "ss.example:8388"},
		{"padded-base64", "ss://" + base64.URLEncoding.EncodeToString([]byte("aes-256-gcm:secret")) + "@ss.example:8388", "aes-256-gcm", "secret", "ss.example:8388"},
		{"2022-ipv6", "SS://2022-blake3-aes-256-gcm:" + url.QueryEscape(key) + "@[::1]:8388", "2022-blake3-aes-256-gcm", key, "[::1]:8388"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canonical, parsed, err := Parse(tc.raw)
			require.NoError(t, err)
			require.Equal(t, "ss", parsed.Scheme)
			require.Equal(t, tc.host, parsed.Host)
			require.Equal(t, tc.method, parsed.User.Username())
			password, _ := parsed.User.Password()
			require.Equal(t, tc.password, password)
			require.Empty(t, parsed.Fragment)
			reparsed, _, err := Parse(canonical)
			require.NoError(t, err)
			require.Equal(t, canonical, reparsed)
		})
	}
}

func TestParseShadowsocksRejectsUnsupportedAndMalformedWithoutSecrets(t *testing.T) {
	secret := "do-not-log-this"
	for _, raw := range []string{
		"ss://aes-128-gcm:" + secret + "@node:8388/?plugin=obfs-local",
		"ss://aes-128-gcm:" + secret + "@node:8388?other=1",
		"ss://aes-128-gcm:" + secret + "@node:8388?",
		"ss://aes-128-gcm:" + secret + "@node:8388/path",
		"ss://aes-128-gcm:" + secret + "@node:0",
		"ss://aes-128-gcm:" + secret + "@node:65536",
		"ss://aes-128-gcm:" + secret + "@node",
		"ss://aes-128-gcm:" + secret + "@node:bad",
		"ss://aes-128-gcm:%xx" + secret + "@node:8388",
		"ss://rc4-md5:" + secret + "@node:8388",
		"ss://2022-blake3-aes-128-gcm:" + secret + "@node:8388",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:"+secret)) + "@node:8388",
		"http://user:" + secret + "@node:bad",
	} {
		_, parsed, err := Parse(raw)
		require.Error(t, err)
		require.Nil(t, parsed)
		require.NotContains(t, err.Error(), secret)
		require.NotContains(t, err.Error(), raw)
	}
	_, _, err := Parse("ss://" + strings.Repeat("=", 8) + "@node:8388")
	require.Error(t, err)
}
