package service

import "time"

func proxyEgressRoute(proxy *Proxy) OpenAIEgressRoute {
	return OpenAIEgressRoute{ProxyID: proxy.ID, ProxyURL: proxy.URL()}
}

func proxyGeoRouteKey(proxy *Proxy) string {
	return openAIEgressRouteKey(proxyEgressRoute(proxy), "")
}

func (s *adminServiceImpl) observeProxyExit(proxy *Proxy, info *ProxyExitInfo, err error, startedAt time.Time) {
	if s.egressLocationService != nil {
		// Even a connectivity failure must retain its attempt time. Otherwise a
		// late old failure looks newer than a successful manual probe to resolver.
		if info == nil {
			info = &ProxyExitInfo{GeoCheckedAt: startedAt}
		} else if info.GeoCheckedAt.IsZero() {
			copy := *info
			copy.GeoCheckedAt = startedAt
			info = &copy
		}
		s.egressLocationService.ObserveResult(proxyEgressRoute(proxy), info, err)
	}
}

func proxyLatencyFromExit(proxy *Proxy, exit *ProxyExitInfo, latencyMs int64, err error, startedAt time.Time) *ProxyLatencyInfo {
	info := &ProxyLatencyInfo{Success: err == nil, RouteKey: proxyGeoRouteKey(proxy), GeoResultUnixMs: startedAt.UnixMilli(), UpdatedAt: time.Now()}
	if err != nil {
		info.Message = err.Error()
		info.GeoStatus, info.GeoReason = "failed", "probe_failed"
		return info
	}
	info.LatencyMs, info.Message = &latencyMs, "Proxy is accessible"
	copyProxyExitGeo(info, exit)
	return info
}

func copyProxyExitGeo(info *ProxyLatencyInfo, exit *ProxyExitInfo) {
	if exit == nil {
		info.GeoStatus, info.GeoReason = "failed", "not_checked"
		return
	}
	info.IPAddress, info.Country, info.CountryCode = exit.IP, exit.Country, exit.CountryCode
	info.Region, info.City, info.Timezone = exit.Region, exit.City, exit.Timezone
	info.GeoStatus, info.GeoReason = exit.GeoStatus, exit.GeoReason
	if info.GeoStatus == "" || (info.GeoStatus == "success" && !validOpenAIEgressGeo(exit)) {
		info.GeoStatus, info.GeoReason = "failed", "incomplete_location"
	}
	if !exit.GeoCheckedAt.IsZero() {
		checked := exit.GeoCheckedAt
		info.GeoCheckedAt = &checked
	}
}

func usableProxyGeo(info *ProxyLatencyInfo, now time.Time) bool {
	return info != nil && info.IPAddress != "" && info.Country != "" && info.CountryCode != "" && info.Region != "" &&
		info.City != "" && info.Timezone != "" && info.GeoCheckedAt != nil &&
		!info.GeoCheckedAt.After(now) && now.Sub(*info.GeoCheckedAt) < openAIEgressLocationMaxAge
}

// Preserve a recent success only for the same configured route and same known
// exit. In particular, a new IP with failed geolocation cannot inherit old geo.
func mergeProxyGeo(info, existing *ProxyLatencyInfo, now time.Time) {
	if existing == nil || info.RouteKey == "" || info.RouteKey != existing.RouteKey {
		return
	}
	if existing.GeoResultUnixMs > info.GeoResultUnixMs {
		info.IPAddress, info.Country, info.CountryCode = existing.IPAddress, existing.Country, existing.CountryCode
		info.Region, info.City, info.Timezone = existing.Region, existing.City, existing.Timezone
		info.GeoStatus, info.GeoReason, info.GeoCheckedAt = existing.GeoStatus, existing.GeoReason, existing.GeoCheckedAt
		info.GeoResultUnixMs = existing.GeoResultUnixMs
		expireProxyGeo(info, now)
		return
	}
	if info.GeoStatus == "success" {
		return
	}
	if info.IPAddress != "" && info.IPAddress != existing.IPAddress || !usableProxyGeo(existing, now) {
		return
	}
	info.IPAddress, info.Country, info.CountryCode = existing.IPAddress, existing.Country, existing.CountryCode
	info.Region, info.City, info.Timezone = existing.Region, existing.City, existing.Timezone
	checked := *existing.GeoCheckedAt
	info.GeoCheckedAt = &checked
}

func expireProxyGeo(info *ProxyLatencyInfo, now time.Time) {
	if info.RouteKey != "" && usableProxyGeo(info, now) {
		return
	}
	info.Country, info.CountryCode, info.Region, info.City, info.Timezone = "", "", "", "", ""
	if info.GeoCheckedAt != nil && !info.GeoCheckedAt.After(now) && now.Sub(*info.GeoCheckedAt) >= openAIEgressLocationMaxAge {
		info.GeoStatus, info.GeoReason = "failed", "last_good_expired"
		return
	}
	// A failed fresh lookup has a useful reason even though it has no complete
	// location. Expiry must not turn rate limits/network failures into expiry.
	if info.GeoStatus == "failed" && info.GeoReason != "" {
		return
	}
	info.GeoStatus, info.GeoReason = "failed", "incomplete_location"
	switch {
	case info.GeoCheckedAt == nil || info.RouteKey == "":
		info.GeoReason = "not_checked"
	case info.GeoCheckedAt.After(now):
		info.GeoReason = "invalid_checked_at"
	case now.Sub(*info.GeoCheckedAt) >= openAIEgressLocationMaxAge:
		info.GeoReason = "last_good_expired"
	}
}

func proxyTestResultFromLatency(info *ProxyLatencyInfo) *ProxyTestResult {
	result := &ProxyTestResult{
		Success: info.Success, Message: info.Message, IPAddress: info.IPAddress,
		Country: info.Country, CountryCode: info.CountryCode, Region: info.Region, City: info.City,
		Timezone: info.Timezone, GeoStatus: info.GeoStatus, GeoReason: info.GeoReason, GeoCheckedAt: info.GeoCheckedAt,
	}
	if info.LatencyMs != nil {
		result.LatencyMs = *info.LatencyMs
	}
	return result
}

func applyProxyQualityGeo(result *ProxyQualityCheckResult, info *ProxyLatencyInfo) {
	result.ExitIP, result.Country, result.CountryCode = info.IPAddress, info.Country, info.CountryCode
	result.Region, result.City, result.Timezone = info.Region, info.City, info.Timezone
	result.GeoStatus, result.GeoReason, result.GeoCheckedAt = info.GeoStatus, info.GeoReason, info.GeoCheckedAt
}
