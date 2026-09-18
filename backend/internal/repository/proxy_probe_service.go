package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

func NewProxyExitInfoProber(cfg *config.Config) service.ProxyExitInfoProber {
	insecure := false
	allowPrivate := false
	validateResolvedIP := true
	maxResponseBytes := defaultProxyProbeResponseMaxBytes
	geoLookupURL := config.DefaultProxyProbeGeoLookupURL
	if cfg != nil {
		insecure = cfg.Security.ProxyProbe.InsecureSkipVerify
		allowPrivate = cfg.Security.URLAllowlist.AllowPrivateHosts
		validateResolvedIP = cfg.Security.URLAllowlist.Enabled
		if value := strings.TrimSpace(cfg.Security.ProxyProbe.GeoLookupURL); value != "" {
			geoLookupURL = value
		}
		if cfg.Gateway.ProxyProbeResponseReadMaxBytes > 0 {
			maxResponseBytes = cfg.Gateway.ProxyProbeResponseReadMaxBytes
		}
	}
	if insecure {
		log.Printf("[ProxyProbe] Warning: insecure_skip_verify is not allowed and will cause probe failure.")
	}
	// 构建探测 URL 列表：配置存在时覆盖内置默认列表。
	var configuredTargets []configuredProbeTarget
	if cfg != nil && len(cfg.Security.ProxyProbe.URLs) > 0 {
		configuredTargets = make([]configuredProbeTarget, 0, len(cfg.Security.ProxyProbe.URLs))
		for _, u := range cfg.Security.ProxyProbe.URLs {
			configuredTargets = append(configuredTargets, configuredProbeTarget{
				url:    u.URL,
				parser: u.Parser,
			})
		}
	}

	return &proxyProbeService{
		insecureSkipVerify:  insecure,
		allowPrivateHosts:   allowPrivate,
		validateResolvedIP:  validateResolvedIP,
		maxResponseBytes:    maxResponseBytes,
		configuredProbeURLs: configuredTargets,
		geoLookupURL:        geoLookupURL,
	}
}

const (
	defaultProxyProbeTimeout          = 10 * time.Second
	defaultProxyGeoTimeout            = 5 * time.Second
	defaultProxyProbeResponseMaxBytes = int64(1024 * 1024)
)

// probeURLs 按优先级排列的内置探测 URL 列表。
// 某些 AI API 专用代理只允许访问特定域名，因此需要多个备选。
var probeURLs = []struct {
	url    string
	parser string
}{
	{"http://ip-api.com/json/?lang=en", "ip-api"},
	{"http://api64.ipify.org?format=json", "ipify"},
}

type configuredProbeTarget struct {
	url    string
	parser string
}

type proxyProbeService struct {
	insecureSkipVerify  bool
	allowPrivateHosts   bool
	validateResolvedIP  bool
	maxResponseBytes    int64
	configuredProbeURLs []configuredProbeTarget
	geoLookupURL        string
	geoMu               sync.Mutex
	geoNextAllowed      time.Time // guarded by geoMu, shared by manual/background probes
}

func (s *proxyProbeService) ProbeProxy(ctx context.Context, proxyURL string) (*service.ProxyExitInfo, int64, error) {
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:           proxyURL,
		Timeout:            defaultProxyProbeTimeout,
		InsecureSkipVerify: s.insecureSkipVerify,
		ValidateResolvedIP: s.validateResolvedIP,
		AllowPrivateHosts:  s.allowPrivateHosts,
	})
	if err != nil {
		return nil, 0, errors.New("failed to create proxy client")
	}
	client = proxyProbeNoRedirectClient(client)

	var lastErr error
	if len(s.configuredProbeURLs) > 0 {
		for _, probe := range s.configuredProbeURLs {
			exitInfo, latencyMs, err := s.probeWithURL(ctx, client, probe.url, probe.parser)
			if err == nil {
				s.enrichExitInfo(ctx, exitInfo)
				return exitInfo, latencyMs, nil
			}
			lastErr = err
		}
		return nil, 0, fmt.Errorf("all probe URLs failed, last error: %w", lastErr)
	}

	for _, probe := range probeURLs {
		exitInfo, latencyMs, err := s.probeWithURL(ctx, client, probe.url, probe.parser)
		if err == nil {
			s.enrichExitInfo(ctx, exitInfo)
			return exitInfo, latencyMs, nil
		}
		lastErr = err
	}

	return nil, 0, fmt.Errorf("all probe URLs failed, last error: %w", lastErr)
}

func (s *proxyProbeService) probeWithURL(ctx context.Context, client *http.Client, url string, parser string) (*service.ProxyExitInfo, int64, error) {
	startTime := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, errors.New("failed to create probe request")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("proxy connection failed")
	}
	defer func() { _ = resp.Body.Close() }()

	latencyMs := time.Since(startTime).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		return nil, latencyMs, fmt.Errorf("request failed with status: %d", resp.StatusCode)
	}

	maxResponseBytes := s.maxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultProxyProbeResponseMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, latencyMs, errors.New("failed to read probe response")
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, latencyMs, fmt.Errorf("proxy probe response exceeds limit: %d", maxResponseBytes)
	}

	switch parser {
	case "ip-api":
		return s.parseIPAPI(body, latencyMs)
	case "ipify":
		return s.parseIPify(body, latencyMs)
	case "chatgpt-trace":
		return s.parseChatGPTTrace(body, latencyMs)
	default:
		return nil, latencyMs, fmt.Errorf("unknown parser: %s", parser)
	}
}

func (s *proxyProbeService) parseIPAPI(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var ipInfo struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Query       string `json:"query"`
		City        string `json:"city"`
		RegionName  string `json:"regionName"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		Timezone    string `json:"timezone"`
	}

	if err := json.Unmarshal(body, &ipInfo); err != nil {
		return nil, latencyMs, errors.New("failed to parse ip-api response")
	}
	if strings.ToLower(ipInfo.Status) != "success" {
		return nil, latencyMs, errors.New("ip-api request failed")
	}
	ip, err := canonicalProxyExitIP(ipInfo.Query)
	if err != nil {
		return nil, latencyMs, err
	}

	return &service.ProxyExitInfo{
		IP:          ip,
		City:        ipInfo.City,
		Region:      ipInfo.RegionName,
		Country:     ipInfo.Country,
		CountryCode: ipInfo.CountryCode,
		Timezone:    ipInfo.Timezone,
	}, latencyMs, nil
}

func (s *proxyProbeService) parseIPify(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var result struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, latencyMs, errors.New("failed to parse ipify response")
	}
	if result.IP == "" {
		return nil, latencyMs, fmt.Errorf("ipify: no IP found in response")
	}
	ip, err := canonicalProxyExitIP(result.IP)
	if err != nil {
		return nil, latencyMs, err
	}
	return &service.ProxyExitInfo{
		IP: ip,
	}, latencyMs, nil
}

// parseGeoResponse accepts the legacy IPinfo schema and preserves explicitly
// configured ip-api-compatible endpoints. Never merge fields between providers.
func (s *proxyProbeService) parseGeoResponse(body []byte) (*service.ProxyExitInfo, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return nil, errors.New("invalid geo response")
	}
	_, hasIP := envelope["ip"]
	_, hasQuery := envelope["query"]
	_, hasStatus := envelope["status"]
	if hasIP && !hasQuery && !hasStatus {
		return parseIPinfoGeo(body)
	}
	if !hasIP && (hasQuery || hasStatus) {
		info, _, err := s.parseIPAPI(body, 0)
		return info, err
	}
	return nil, errors.New("unrecognized geo response")
}

func parseIPinfoGeo(body []byte) (*service.ProxyExitInfo, error) {
	var result struct {
		IP       string          `json:"ip"`
		Country  string          `json:"country"`
		Region   string          `json:"region"`
		City     string          `json:"city"`
		Timezone string          `json:"timezone"`
		Bogon    bool            `json:"bogon"`
		Error    json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Bogon || (len(result.Error) != 0 && string(result.Error) != "null") {
		return nil, errors.New("invalid ipinfo response")
	}
	ip, err := canonicalProxyExitIP(result.IP)
	if err != nil {
		return nil, err
	}
	code := strings.ToUpper(strings.TrimSpace(result.Country))
	var country string
	if len(code) == 2 {
		if region, err := language.ParseRegion(code); err == nil && region.IsCountry() {
			country = display.English.Regions().Name(region)
		}
	}
	return &service.ProxyExitInfo{
		IP: ip, Country: country, CountryCode: code,
		Region: result.Region, City: result.City, Timezone: result.Timezone,
	}, nil
}

// parseChatGPTTrace 解析 Cloudflare trace 端点（如 chatgpt.com/cdn-cgi/trace）的纯文本响应。
// 响应按行给出键值对，其中 ip= 为出口 IP，loc= 为国家代码。
func (s *proxyProbeService) parseChatGPTTrace(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var ip, loc string
	for _, line := range strings.Split(string(body), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "ip":
			ip = strings.TrimSpace(value)
		case "loc":
			loc = strings.TrimSpace(value)
		}
	}
	if ip == "" {
		return nil, latencyMs, errors.New("chatgpt-trace: no ip= found in response")
	}
	ip, err := canonicalProxyExitIP(ip)
	if err != nil {
		return nil, latencyMs, err
	}
	info := &service.ProxyExitInfo{
		IP: ip,
	}
	if loc != "" {
		info.CountryCode = loc
	}
	return info, latencyMs, nil
}

func canonicalProxyExitIP(value string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return "", errors.New("invalid proxy exit IP")
	}
	return ip.Unmap().String(), nil
}

func proxyProbeNoRedirectClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

// Connectivity and geolocation have separate outcomes. A lookup never uses the
// tested proxy, and never changes the discovered IP or triggers another probe.
func (s *proxyProbeService) enrichExitInfo(ctx context.Context, info *service.ProxyExitInfo) {
	info.City, info.Region, info.Country, info.CountryCode, info.Timezone = "", "", "", "", ""
	info.GeoStatus, info.GeoReason, info.GeoCheckedAt = "failed", "", time.Now().UTC()
	ctx, cancel := context.WithTimeout(ctx, defaultProxyGeoTimeout)
	defer cancel()
	// Only the provider cooldown is shared. Never hold its lock during I/O: a
	// slow lookup for one exit must not consume every other exit's timeout.
	s.geoMu.Lock()
	rateLimited := time.Now().Before(s.geoNextAllowed)
	s.geoMu.Unlock()
	if rateLimited {
		info.GeoReason = "rate_limited"
		return
	}
	template := strings.TrimSpace(s.geoLookupURL)
	if template == "" {
		template = config.DefaultProxyProbeGeoLookupURL
	}
	if strings.Count(template, "{ip}") != 1 {
		info.GeoReason = "invalid_lookup_url"
		return
	}
	parsedTemplate, err := url.Parse(template)
	if err != nil || (!strings.Contains(parsedTemplate.Path, "{ip}") && !strings.Contains(parsedTemplate.RawQuery, "{ip}")) || strings.Contains(template, "#") {
		info.GeoReason = "invalid_lookup_url"
		return
	}
	endpoint, err := url.Parse(strings.Replace(template, "{ip}", url.QueryEscape(info.IP), 1))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		info.GeoReason = "invalid_lookup_url"
		return
	}
	// The legacy IPinfo endpoint returns English location names without a
	// language parameter. Keep the existing English override for custom/ip-api
	// endpoints, including deployments that still explicitly configure ip-api.
	if !strings.EqualFold(endpoint.Hostname(), "ipinfo.io") {
		query := endpoint.Query()
		query.Set("lang", "en")
		endpoint.RawQuery = query.Encode()
	}
	client, err := httpclient.GetClient(httpclient.Options{
		Timeout: defaultProxyGeoTimeout, ValidateResolvedIP: s.validateResolvedIP, AllowPrivateHosts: s.allowPrivateHosts,
	})
	if err != nil {
		info.GeoReason = "client_error"
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		info.GeoReason = "invalid_lookup_url"
		return
	}
	resp, err := proxyProbeNoRedirectClient(client).Do(req)
	if err != nil {
		info.GeoReason = proxyGeoNetworkReason(err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	s.observeGeoRateLimit(resp)
	if resp.StatusCode != http.StatusOK {
		info.GeoReason = "http_error"
		if resp.StatusCode == http.StatusTooManyRequests {
			info.GeoReason = "rate_limited"
		}
		return
	}
	limit := s.maxResponseBytes
	if limit <= 0 {
		limit = defaultProxyProbeResponseMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		info.GeoReason = proxyGeoNetworkReason(err)
		return
	}
	if int64(len(body)) > limit {
		info.GeoReason = "response_too_large"
		return
	}
	geo, err := s.parseGeoResponse(body)
	if err != nil {
		info.GeoReason = "invalid_response"
		return
	}
	if geo.IP != info.IP {
		info.GeoReason = "ip_mismatch"
		return
	}
	geo.City, geo.Region, geo.Country = strings.TrimSpace(geo.City), strings.TrimSpace(geo.Region), strings.TrimSpace(geo.Country)
	geo.CountryCode = strings.ToUpper(strings.TrimSpace(geo.CountryCode))
	if !validProxyGeoName(geo.City) || !validProxyGeoName(geo.Region) || !validProxyGeoName(geo.Country) || len(geo.CountryCode) != 2 || geo.CountryCode[0] < 'A' || geo.CountryCode[0] > 'Z' || geo.CountryCode[1] < 'A' || geo.CountryCode[1] > 'Z' {
		info.GeoReason = "incomplete_location"
		return
	}
	geo.Timezone = strings.TrimSpace(geo.Timezone)
	if !validProxyGeoTimezone(geo.Timezone) {
		info.GeoReason = "invalid_timezone"
		return
	}
	info.City, info.Region, info.Country, info.CountryCode, info.Timezone = geo.City, geo.Region, geo.Country, geo.CountryCode, geo.Timezone
	info.GeoStatus = "success"
}

func validProxyGeoName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	hasLetter := false
	for _, char := range value {
		if unicode.IsControl(char) || (unicode.IsLetter(char) && !unicode.In(char, unicode.Latin)) {
			return false
		}
		hasLetter = hasLetter || unicode.IsLetter(char)
	}
	return hasLetter
}

func validProxyGeoTimezone(value string) bool {
	if value == "" || len(value) > 128 || value == "Local" || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	_, err := time.LoadLocation(value)
	return err == nil
}

func proxyGeoNetworkReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "network_error"
}

func (s *proxyProbeService) observeGeoRateLimit(resp *http.Response) {
	remaining, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("X-Rl")))
	if resp.StatusCode != http.StatusTooManyRequests && (err != nil || remaining > 0) {
		return
	}
	now := time.Now()
	const maxDelay = 24 * time.Hour
	var delay time.Duration
	seconds, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-Ttl")), 10, 64)
	if err == nil && seconds > 0 {
		if seconds > int64(maxDelay/time.Second) {
			seconds = int64(maxDelay / time.Second)
		}
		delay = time.Duration(seconds) * time.Second
	}
	// Configurable providers need not emit ip-api's X-Ttl. Honor standard
	// Retry-After as well, without shortening a longer explicit provider delay.
	retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if seconds, parseErr := strconv.ParseInt(retryAfter, 10, 64); parseErr == nil && seconds > 0 {
		if seconds > int64(maxDelay/time.Second) {
			seconds = int64(maxDelay / time.Second)
		}
		if candidate := time.Duration(seconds) * time.Second; candidate > delay {
			delay = candidate
		}
	} else if retryAt, parseErr := http.ParseTime(retryAfter); parseErr == nil && retryAt.After(now) {
		if candidate := retryAt.Sub(now); candidate > delay {
			delay = candidate
		}
	}
	if delay <= 0 {
		delay = time.Minute
	} else if delay > maxDelay {
		delay = maxDelay
	}
	nextAllowed := now.Add(delay)
	s.geoMu.Lock()
	if nextAllowed.After(s.geoNextAllowed) {
		s.geoNextAllowed = nextAllowed
	}
	s.geoMu.Unlock()
}
