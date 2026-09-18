package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyProbeIPinfoDefaultAndCustomLookup(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}} {
		probe := NewProxyExitInfoProber(cfg).(*proxyProbeService)
		require.Equal(t, "https://ipinfo.io/{ip}/json", probe.geoLookupURL)
	}
	cfg := &config.Config{}
	cfg.Security.ProxyProbe.GeoLookupURL = "http://ip-api.com/json/{ip}?lang=en"
	probe := NewProxyExitInfoProber(cfg).(*proxyProbeService)
	require.Equal(t, cfg.Security.ProxyProbe.GeoLookupURL, probe.geoLookupURL, "explicit deployed endpoint remains authoritative")
}

func TestProxyProbeIPinfoReplacesDiscoveryGeography(t *testing.T) {
	for _, tc := range []struct{ name, discoveredIP, returnedIP string }{
		{"ipv4", "203.0.113.5", "203.0.113.5"},
		{"ipv6", "2620:b9:e000:101::150", "2620:00b9:e000:0101:0000:0000:0000:0150"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lookupCalls, proxyCalls atomic.Int32
			geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lookupCalls.Add(1)
				require.Equal(t, "/"+tc.discoveredIP+"/json", r.URL.Path)
				require.Empty(t, r.URL.Query().Get("token"))
				require.Empty(t, r.Header.Get("Authorization"))
				_ = json.NewEncoder(w).Encode(map[string]string{
					"ip": tc.returnedIP, "country": "US", "region": "Washington",
					"city": "Seattle", "timezone": "America/Los_Angeles",
				})
			}))
			defer geo.Close()
			proxy := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxyCalls.Add(1)
				require.Equal(t, "discovery.example", r.URL.Host)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"status": "success", "query": tc.discoveredIP, "country": "United States",
					"countryCode": "US", "regionName": "Oregon", "city": "Portland", "timezone": "America/Los_Angeles",
				})
			}))
			defer proxy.Close()
			probe := &proxyProbeService{
				allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}/json",
				configuredProbeURLs: []configuredProbeTarget{{"http://discovery.example/json", "ip-api"}},
			}
			info, _, err := probe.ProbeProxy(context.Background(), proxy.URL)
			require.NoError(t, err)
			require.Equal(t, int32(1), proxyCalls.Load(), "only exit discovery traverses the tested proxy")
			require.Equal(t, int32(1), lookupCalls.Load(), "complete discovery geography must not bypass IPinfo")
			require.Equal(t, tc.discoveredIP, info.IP)
			require.Equal(t, "success", info.GeoStatus)
			require.Equal(t, "United States", info.Country)
			require.Equal(t, "US", info.CountryCode)
			require.Equal(t, "Washington", info.Region)
			require.Equal(t, "Seattle", info.City)
			require.Equal(t, "America/Los_Angeles", info.Timezone)
		})
	}
}

func TestProxyProbeIPinfoInvalidResultsDoNotRetainDiscoveryGeography(t *testing.T) {
	const valid = `{"ip":"203.0.113.5","country":"US","region":"Washington","city":"Seattle","timezone":"America/Los_Angeles"}`
	for _, tc := range []struct{ name, body, reason string }{
		{"wrong_ip", strings.Replace(valid, "203.0.113.5", "203.0.113.6", 1), "ip_mismatch"},
		{"invalid_ip", strings.Replace(valid, "203.0.113.5", "invalid", 1), "invalid_response"},
		{"bogon", strings.Replace(valid, `"ip":`, `"bogon":true,"ip":`, 1), "invalid_response"},
		{"provider_error", strings.Replace(valid, `"ip":`, `"error":{"message":"sensitive upstream error"},"ip":`, 1), "invalid_response"},
		{"invalid_json", `secret proxy credential`, "invalid_response"},
		{"null", `null`, "invalid_response"},
		{"array", `[]`, "invalid_response"},
		{"unknown_schema", `{"location":{"city":"Seattle"}}`, "invalid_response"},
		{"mixed_schema", strings.Replace(valid, `"ip":`, `"status":"success","query":"203.0.113.5","ip":`, 1), "invalid_response"},
		{"field_wrong_type", strings.Replace(valid, `"city":"Seattle"`, `"city":3`, 1), "invalid_response"},
		{"lite_country_only", `{"ip":"203.0.113.5","country":"US"}`, "incomplete_location"},
		{"missing_city", strings.Replace(valid, `"city":"Seattle"`, `"city":""`, 1), "incomplete_location"},
		{"missing_region", strings.Replace(valid, `"region":"Washington"`, `"region":""`, 1), "incomplete_location"},
		{"country_name_instead_of_code", strings.Replace(valid, `"country":"US"`, `"country":"United States"`, 1), "incomplete_location"},
		{"unknown_country", strings.Replace(valid, `"country":"US"`, `"country":"ZZ"`, 1), "incomplete_location"},
		{"invalid_timezone", strings.Replace(valid, "America/Los_Angeles", "Mars/Olympus", 1), "invalid_timezone"},
		{"missing_timezone", strings.Replace(valid, "America/Los_Angeles", "", 1), "invalid_timezone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer geo.Close()
			probe := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}/json"}
			info := &service.ProxyExitInfo{
				IP: "203.0.113.5", Country: "United States", CountryCode: "US", Region: "Oregon", City: "Portland", Timezone: "America/Los_Angeles",
			}
			probe.enrichExitInfo(context.Background(), info)
			require.Equal(t, "203.0.113.5", info.IP)
			require.Equal(t, "failed", info.GeoStatus)
			require.Equal(t, tc.reason, info.GeoReason)
			require.Empty(t, info.Country)
			require.Empty(t, info.CountryCode)
			require.Empty(t, info.Region)
			require.Empty(t, info.City)
			require.Empty(t, info.Timezone)
			require.False(t, info.GeoCheckedAt.IsZero())
		})
	}
}

func TestProxyProbeIPinfoRateLimitPreservesConnectivity(t *testing.T) {
	var lookupCalls, discoveryCalls atomic.Int32
	geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lookupCalls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"title":"Rate limit exceeded"}}`)
	}))
	defer geo.Close()
	discovery := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		discoveryCalls.Add(1)
		_, _ = io.WriteString(w, `{"ip":"203.0.113.5"}`)
	}))
	defer discovery.Close()
	probe := &proxyProbeService{
		allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}/json",
		configuredProbeURLs: []configuredProbeTarget{{discovery.URL, "ipify"}},
	}
	started := time.Now()
	for range 2 {
		info, _, err := probe.ProbeProxy(context.Background(), "")
		require.NoError(t, err, "IPinfo failure must not mark a reachable proxy as disconnected")
		require.Equal(t, "203.0.113.5", info.IP)
		require.Equal(t, "failed", info.GeoStatus)
		require.Equal(t, "rate_limited", info.GeoReason)
	}
	require.Equal(t, int32(2), discoveryCalls.Load())
	require.Equal(t, int32(1), lookupCalls.Load(), "shared provider cooldown suppresses the second lookup")
	require.False(t, probe.geoNextAllowed.Before(started.Add(120*time.Second)))
}
