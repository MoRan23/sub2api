package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ProxyProbeServiceSuite struct {
	suite.Suite
	ctx      context.Context
	proxySrv *httptest.Server
	geoSrv   *httptest.Server
	prober   *proxyProbeService
}

func (s *ProxyProbeServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.prober = &proxyProbeService{
		allowPrivateHosts: true,
	}
	s.geoSrv = newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "query": strings.TrimPrefix(r.URL.Path, "/"), "city": "c", "regionName": "r", "country": "cc", "countryCode": "CC", "timezone": "America/Los_Angeles"})
	}))
	s.prober.geoLookupURL = s.geoSrv.URL + "/{ip}"
}

func (s *ProxyProbeServiceSuite) TearDownTest() {
	s.geoSrv.Close()
	if s.proxySrv != nil {
		s.proxySrv.Close()
		s.proxySrv = nil
	}
}

func (s *ProxyProbeServiceSuite) setupProxyServer(handler http.HandlerFunc) {
	s.proxySrv = newLocalTestServer(s.T(), handler)
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidProxyURL() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "://bad")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_UnsupportedProxyScheme() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "ftp://127.0.0.1:1")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPAPI() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查是否是 ip-api 请求
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success","query":"1.2.3.4","city":"c","regionName":"r","country":"cc","countryCode":"CC"}`)
			return
		}
		// 其他请求返回错误
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "c", info.City)
	require.Equal(s.T(), "r", info.Region)
	require.Equal(s.T(), "cc", info.Country)
	require.Equal(s.T(), "CC", info.CountryCode)
	require.Equal(s.T(), "America/Los_Angeles", info.Timezone)
	require.Equal(s.T(), "success", info.GeoStatus)
	require.False(s.T(), info.GeoCheckedAt.IsZero())
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPifyFallback() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ip-api 失败
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// ipify 成功
		if strings.Contains(r.RequestURI, "api64.ipify.org") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ip": "5.6.7.8"}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy should fallback to ipify")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "5.6.7.8", info.IP)
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_AllFailed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidJSON() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		// ipify 也返回无效响应
		if strings.Contains(r.RequestURI, "api64.ipify.org") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_ProxyServerClosed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.proxySrv.Close()

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err, "expected error when proxy server is closed")
}

func (s *ProxyProbeServiceSuite) TestParseIPAPI_Success() {
	body := []byte(`{"status":"success","query":"1.2.3.4","city":"Beijing","regionName":"Beijing","country":"China","countryCode":"CN"}`)
	info, latencyMs, err := s.prober.parseIPAPI(body, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(100), latencyMs)
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "Beijing", info.City)
	require.Equal(s.T(), "Beijing", info.Region)
	require.Equal(s.T(), "China", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestParseIPAPI_Failure() {
	body := []byte(`{"status":"fail","message":"rate limited"}`)
	_, _, err := s.prober.parseIPAPI(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "ip-api request failed")
}

func (s *ProxyProbeServiceSuite) TestParseIPify_Success() {
	body := []byte(`{"ip": "2001:db8::1"}`)
	info, latencyMs, err := s.prober.parseIPify(body, 50)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(50), latencyMs)
	require.Equal(s.T(), "2001:db8::1", info.IP)
}

func (s *ProxyProbeServiceSuite) TestParseIPify_NoIP() {
	body := []byte(`{"ip": ""}`)
	_, _, err := s.prober.parseIPify(body, 50)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "no IP found")
}

func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_Success() {
	body := []byte("fl=abc\nh=chatgpt.com\nip=203.0.113.5\nts=1700000000\nloc=US\ntz=UTC\n")
	info, latencyMs, err := s.prober.parseChatGPTTrace(body, 320)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(320), latencyMs)
	require.Equal(s.T(), "203.0.113.5", info.IP)
	require.Equal(s.T(), "US", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_NoIP() {
	body := []byte("fl=abc\nh=chatgpt.com\nloc=US\n")
	_, _, err := s.prober.parseChatGPTTrace(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "chatgpt-trace: no ip= found")
}

func TestProxyProbeServiceSuite(t *testing.T) {
	suite.Run(t, new(ProxyProbeServiceSuite))
}

func TestProxyProbeLookupUsesDirectDiscoveredIPv6(t *testing.T) {
	const exitIP = "2001:db8:1:2:3:4:5:6"
	geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/json/"+exitIP, r.URL.Path)
		require.Equal(t, "en", r.URL.Query().Get("lang"))
		_, _ = io.WriteString(w, `{"status":"success","query":"2001:0db8:0001:0002:0003:0004:0005:0006","city":"Los Angeles","regionName":"California","country":"United States","countryCode":"US","timezone":"America/Los_Angeles"}`)
	}))
	defer geo.Close()
	var proxyRequests atomic.Int32
	proxy := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		require.Equal(t, "allowed.example", r.URL.Host)
		_, _ = io.WriteString(w, "ip="+exitIP+"\nloc=US\n")
	}))
	defer proxy.Close()
	prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/json/{ip}?lang=zh-CN", configuredProbeURLs: []configuredProbeTarget{{"http://allowed.example/trace", "chatgpt-trace"}}}
	info, _, err := prober.ProbeProxy(context.Background(), proxy.URL)
	require.NoError(t, err)
	require.Equal(t, int32(1), proxyRequests.Load(), "geo lookup must not use the tested proxy")
	require.Equal(t, exitIP, info.IP)
	require.Equal(t, "success", info.GeoStatus)
	require.Equal(t, "United States", info.Country)
	require.Equal(t, "California", info.Region)
	require.Equal(t, "Los Angeles", info.City)
}

func TestProxyProbeGeoFailurePreservesConnectivity(t *testing.T) {
	valid := `{"status":"success","query":"203.0.113.5","city":"Los Angeles","regionName":"California","country":"United States","countryCode":"US","timezone":"America/Los_Angeles"}`
	for _, tc := range []struct {
		name, body, reason string
		status             int
	}{
		{"wrong_ip", strings.Replace(valid, "203.0.113.5", "203.0.113.6", 1), "ip_mismatch", 200},
		{"invalid_timezone", strings.Replace(valid, "America/Los_Angeles", "Mars/Olympus", 1), "invalid_timezone", 200},
		{"local_timezone", strings.Replace(valid, "America/Los_Angeles", "Local", 1), "invalid_timezone", 200},
		{"missing_city", strings.Replace(valid, "Los Angeles", "", 1), "incomplete_location", 200},
		{"region_code_only", strings.Replace(valid, `"regionName":"California"`, `"region":"CA"`, 1), "incomplete_location", 200},
		{"non_english", strings.Replace(valid, "United States", "美国", 1), "incomplete_location", 200},
		{"oversized", strings.Repeat("x", 1025), "response_too_large", 200},
		{"malformed", `secret proxy-password`, "invalid_response", 200},
		{"denied", `secret proxy-password`, "http_error", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer geo.Close()
			discovery := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"ip":"203.0.113.5"}`)
			}))
			defer discovery.Close()
			prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}", maxResponseBytes: 1024, configuredProbeURLs: []configuredProbeTarget{{discovery.URL, "ipify"}}}
			info, latency, err := prober.ProbeProxy(context.Background(), "")
			require.NoError(t, err)
			require.GreaterOrEqual(t, latency, int64(0))
			require.Equal(t, "203.0.113.5", info.IP)
			require.Equal(t, "failed", info.GeoStatus)
			require.Equal(t, tc.reason, info.GeoReason)
			require.Empty(t, info.Country)
			require.Empty(t, info.Timezone)
		})
	}
}

func TestProxyProbeGeoRateLimit(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("X-Rl", "0")
				w.Header().Set("X-Ttl", "60")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"status":"success","query":"203.0.113.5","city":"Paris","regionName":"Ile-de-France","country":"France","countryCode":"FR","timezone":"Europe/Paris"}`)
			}))
			defer geo.Close()
			prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}"}
			first := &service.ProxyExitInfo{IP: "203.0.113.5"}
			prober.enrichExitInfo(context.Background(), first)
			if status == http.StatusOK {
				require.Equal(t, "success", first.GeoStatus)
			} else {
				require.Equal(t, "rate_limited", first.GeoReason)
			}
			second := &service.ProxyExitInfo{IP: "203.0.113.6"}
			prober.enrichExitInfo(context.Background(), second)
			require.Equal(t, "failed", second.GeoStatus)
			require.Equal(t, "rate_limited", second.GeoReason)
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestProxyProbeGeoRejectsRedirectAndPrivateHost(t *testing.T) {
	var redirected atomic.Bool
	target := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	redirect := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: redirect.URL + "/{ip}"}
	info := &service.ProxyExitInfo{IP: "203.0.113.5"}
	prober.enrichExitInfo(context.Background(), info)
	require.Equal(t, "http_error", info.GeoReason)
	require.False(t, redirected.Load())
	prober = &proxyProbeService{validateResolvedIP: true, geoLookupURL: target.URL + "/{ip}"}
	prober.enrichExitInfo(context.Background(), info)
	require.Equal(t, "network_error", info.GeoReason)
	require.False(t, redirected.Load())
}

func TestProxyProbeGeoTimeoutDoesNotFailConnectivity(t *testing.T) {
	geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer geo.Close()
	discovery := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ip":"203.0.113.5"}`) }))
	defer discovery.Close()
	prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}", configuredProbeURLs: []configuredProbeTarget{{discovery.URL, "ipify"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	info, _, err := prober.ProbeProxy(ctx, "")
	require.NoError(t, err)
	require.Equal(t, "203.0.113.5", info.IP)
	require.Equal(t, "failed", info.GeoStatus)
	require.Equal(t, "timeout", info.GeoReason)
}

func TestProxyProbeParsersRejectInvalidIP(t *testing.T) {
	prober := &proxyProbeService{}
	for _, value := range []string{"not-an-ip", "203.0.113.5:80", "[2001:db8::1]", "fe80::1%eth0", "0.0.0.0", "::"} {
		t.Run(value, func(t *testing.T) {
			_, _, err := prober.parseIPify([]byte(`{"ip":"`+value+`"}`), 0)
			require.Error(t, err)
			_, _, err = prober.parseIPAPI([]byte(`{"status":"success","query":"`+value+`"}`), 0)
			require.Error(t, err)
			_, _, err = prober.parseChatGPTTrace([]byte("ip="+value), 0)
			require.Error(t, err)
		})
	}
}

func TestProxyProbeGeoSlowExitDoesNotBlockOtherExit(t *testing.T) {
	started := make(chan struct{})
	geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/203.0.113.1" {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","query":"203.0.113.2","city":"Berlin","regionName":"Berlin","country":"Germany","countryCode":"DE","timezone":"CET"}`)
	}))
	defer geo.Close()
	prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/{ip}"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	first := &service.ProxyExitInfo{IP: "203.0.113.1"}
	go func() {
		defer close(done)
		prober.enrichExitInfo(ctx, first)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first lookup did not start")
	}
	second := &service.ProxyExitInfo{IP: "203.0.113.2"}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	prober.enrichExitInfo(secondCtx, second)
	require.Equal(t, "success", second.GeoStatus, "unrelated exit must complete while first lookup is stalled")
	require.Equal(t, "CET", second.Timezone, "valid IANA aliases are allowed")
	cancel()
	select {
	case <-done:
		require.Equal(t, "canceled", first.GeoReason)
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt in-flight lookup")
	}
}

func TestProxyProbeGeoQueryTemplate(t *testing.T) {
	const ip = "2001:db8::abcd"
	geo := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, ip, r.URL.Query().Get("ip"))
		require.Equal(t, "en", r.URL.Query().Get("lang"))
		_, _ = io.WriteString(w, `{"status":"success","query":"2001:db8::abcd","city":"Paris","regionName":"Ile-de-France","country":"France","countryCode":"FR","timezone":"Europe/Paris"}`)
	}))
	defer geo.Close()
	prober := &proxyProbeService{allowPrivateHosts: true, geoLookupURL: geo.URL + "/lookup?ip={ip}"}
	info := &service.ProxyExitInfo{IP: ip}
	prober.enrichExitInfo(context.Background(), info)
	require.Equal(t, "success", info.GeoStatus)
	for _, template := range []string{"http://{ip}/lookup", geo.URL + "/{ip}#", geo.URL + "/{ip}#{ip}"} {
		prober.geoLookupURL = template
		prober.enrichExitInfo(context.Background(), info)
		require.Equal(t, "invalid_lookup_url", info.GeoReason)
	}
}
