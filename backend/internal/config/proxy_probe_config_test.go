//go:build unit

package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeProxyProbeURLs(t *testing.T) {
	t.Parallel()

	got, err := normalizeProxyProbeURLs([]ProbeURLConfig{
		{URL: " https://chatgpt.com/cdn-cgi/trace ", Parser: " CHATGPT-TRACE "},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	})
	require.NoError(t, err)
	require.Equal(t, []ProbeURLConfig{
		{URL: "https://chatgpt.com/cdn-cgi/trace", Parser: "chatgpt-trace"},
		{URL: "https://api64.ipify.org?format=json", Parser: "ipify"},
	}, got)
}

func TestNormalizeProxyProbeURLsRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		target  ProbeURLConfig
		wantErr string
	}{
		{name: "missing URL", target: ProbeURLConfig{Parser: "ipify"}, wantErr: "url is required"},
		{name: "missing parser", target: ProbeURLConfig{URL: "https://example.com"}, wantErr: "parser is required"},
		{name: "unknown parser", target: ProbeURLConfig{URL: "https://example.com", Parser: "ip_api"}, wantErr: "unsupported parser"},
		{name: "relative URL", target: ProbeURLConfig{URL: "/cdn-cgi/trace", Parser: "chatgpt-trace"}, wantErr: "invalid url"},
		{name: "unsupported scheme", target: ProbeURLConfig{URL: "ftp://example.com/file", Parser: "ipify"}, wantErr: "scheme must be http or https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeProxyProbeURLs([]ProbeURLConfig{tt.target})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestNormalizeProxyProbeGeoLookupURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "default", want: DefaultProxyProbeGeoLookupURL},
		{name: "whitespace", url: " \t ", want: DefaultProxyProbeGeoLookupURL},
		{name: "trim custom path", url: " https://geo.example.com/json/{ip}?lang=en ", want: "https://geo.example.com/json/{ip}?lang=en"},
		{name: "custom query", url: "https://geo.example.com/json?ip={ip}&lang=en", want: "https://geo.example.com/json?ip={ip}&lang=en"},
		{name: "IPv6 host", url: "http://[2001:db8::2]:8080/json/{ip}?lang=en", want: "http://[2001:db8::2]:8080/json/{ip}?lang=en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeProxyProbeGeoLookupURL(tt.url)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeProxyProbeGeoLookupURLRejectsInvalidTemplates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "missing placeholder", url: "https://geo.example.com/json", wantErr: "exactly one {ip} placeholder"},
		{name: "duplicate placeholder", url: "https://geo.example.com/{ip}?ip={ip}", wantErr: "exactly one {ip} placeholder"},
		{name: "relative URL", url: "/json/{ip}", wantErr: "invalid url"},
		{name: "missing hostname", url: "https://:443/json/{ip}", wantErr: "invalid url"},
		{name: "invalid escape", url: "https://geo.example.com/%zz/{ip}", wantErr: "path or query"},
		{name: "placeholder hostname", url: "https://{ip}/json", wantErr: "path or query"},
		{name: "placeholder IPv6 hostname", url: "https://[{ip}]/json", wantErr: "path or query"},
		{name: "placeholder userinfo", url: "https://{ip}@geo.example.com/json", wantErr: "path or query"},
		{name: "placeholder fragment", url: "https://geo.example.com/json#{ip}", wantErr: "path or query"},
		{name: "unsupported scheme", url: "ftp://geo.example.com/{ip}", wantErr: "scheme must be http or https"},
		{name: "userinfo", url: "https://user:pass@geo.example.com/{ip}", wantErr: "must not contain userinfo"},
		{name: "fragment", url: "https://geo.example.com/{ip}#section", wantErr: "must not contain a fragment"},
		{name: "empty fragment", url: "https://geo.example.com/{ip}#", wantErr: "must not contain a fragment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeProxyProbeGeoLookupURL(tt.url)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestLoadProxyProbeGeoLookupURL(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, DefaultProxyProbeGeoLookupURL, cfg.Security.ProxyProbe.GeoLookupURL)
	})
	t.Run("environment override", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("SECURITY_PROXY_PROBE_GEO_LOOKUP_URL", " https://geo.example.com/{ip}?lang=en ")
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, "https://geo.example.com/{ip}?lang=en", cfg.Security.ProxyProbe.GeoLookupURL)
	})
	t.Run("invalid environment override", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("SECURITY_PROXY_PROBE_GEO_LOOKUP_URL", "https://geo.example.com/json")
		_, err := Load()
		require.ErrorContains(t, err, "security.proxy_probe.geo_lookup_url: url must contain exactly one {ip} placeholder")
	})
}
