package repository

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProxyProbeGeoRateLimitHonorsRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		name       string
		status     int
		remaining  string
		ttl        string
		retryAfter string
		want       time.Duration
	}{
		{"standard seconds only", 429, "", "", "120", 120 * time.Second},
		{"short explicit seconds", 429, "", "invalid", "10", 10 * time.Second},
		{"retry longer than ttl", 429, "", "20", "120", 120 * time.Second},
		{"ttl longer than retry", 429, "", "180", "120", 180 * time.Second},
		{"exhausted ip api quota", 200, "0", "90", "120", 120 * time.Second},
		{"ip api ttl preserved", 200, "0", "90", "", 90 * time.Second},
		{"invalid uses fallback", 429, "", "invalid", "invalid", time.Minute},
		{"nonpositive uses fallback", 429, "", "-1", "0", time.Minute},
		{"seconds bounded", 429, "", "", "9223372036854775807", 24 * time.Hour},
		{"ttl bounded", 429, "", "9223372036854775807", "1", 24 * time.Hour},
		{"remaining quota ignores retry", 200, "1", "90", "120", 0},
		{"normal response ignores retry", 200, "", "", "120", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			probe := &proxyProbeService{}
			response := &http.Response{StatusCode: tt.status, Header: http.Header{}}
			response.Header.Set("X-Rl", tt.remaining)
			response.Header.Set("X-Ttl", tt.ttl)
			response.Header.Set("Retry-After", tt.retryAfter)
			before := time.Now()
			probe.observeGeoRateLimit(response)
			after := time.Now()
			if tt.want == 0 {
				require.True(t, probe.geoNextAllowed.IsZero())
				return
			}
			require.False(t, probe.geoNextAllowed.Before(before.Add(tt.want)))
			require.False(t, probe.geoNextAllowed.After(after.Add(tt.want)))
		})
	}
}

func TestProxyProbeGeoRateLimitHTTPDateAndMonotonicCooldown(t *testing.T) {
	probe := &proxyProbeService{}
	retryAt := time.Now().UTC().Truncate(time.Second).Add(3 * time.Minute)
	response := &http.Response{StatusCode: 429, Header: http.Header{}}
	response.Header.Set("Retry-After", retryAt.Format(http.TimeFormat))
	response.Header.Set("X-Ttl", "60")
	probe.observeGeoRateLimit(response)
	require.True(t, retryAt.Equal(probe.geoNextAllowed))
	response.Header.Set("Retry-After", "1")
	probe.observeGeoRateLimit(response)
	require.True(t, retryAt.Equal(probe.geoNextAllowed), "a later shorter response must not shorten an existing cooldown")

	probe = &proxyProbeService{}
	response.Header.Set("Retry-After", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))
	response.Header.Set("X-Ttl", "")
	before := time.Now()
	probe.observeGeoRateLimit(response)
	require.WithinDuration(t, before.Add(time.Minute), probe.geoNextAllowed, time.Second)

	probe = &proxyProbeService{}
	response.Header.Set("Retry-After", time.Now().Add(7*24*time.Hour).UTC().Format(http.TimeFormat))
	before = time.Now()
	probe.observeGeoRateLimit(response)
	require.WithinDuration(t, before.Add(24*time.Hour), probe.geoNextAllowed, time.Second)
}
