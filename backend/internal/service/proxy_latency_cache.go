package service

import (
	"context"
	"time"
)

type ProxyLatencyInfo struct {
	Success          bool       `json:"success"`
	LatencyMs        *int64     `json:"latency_ms,omitempty"`
	Message          string     `json:"message,omitempty"`
	IPAddress        string     `json:"ip_address,omitempty"`
	Country          string     `json:"country,omitempty"`
	CountryCode      string     `json:"country_code,omitempty"`
	Region           string     `json:"region,omitempty"`
	City             string     `json:"city,omitempty"`
	Timezone         string     `json:"timezone,omitempty"`
	GeoStatus        string     `json:"geo_status,omitempty"`
	GeoReason        string     `json:"geo_reason,omitempty"`
	GeoCheckedAt     *time.Time `json:"geo_checked_at,omitempty"`
	RouteKey         string     `json:"route_key,omitempty"`
	GeoResultUnixMs  int64      `json:"geo_result_unix_ms,omitempty"`
	QualityStatus    string     `json:"quality_status,omitempty"`
	QualityScore     *int       `json:"quality_score,omitempty"`
	QualityGrade     string     `json:"quality_grade,omitempty"`
	QualitySummary   string     `json:"quality_summary,omitempty"`
	QualityCheckedAt *int64     `json:"quality_checked_at,omitempty"`
	QualityCFRay     string     `json:"quality_cf_ray,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type ProxyLatencyCache interface {
	GetProxyLatencies(ctx context.Context, proxyIDs []int64) (map[int64]*ProxyLatencyInfo, error)
	SetProxyLatency(ctx context.Context, proxyID int64, info *ProxyLatencyInfo) error
}

// ProxyProbeLeaseCache optionally coordinates automatic probes across replicas.
// Release must only clear the lease owned by this caller.
type ProxyProbeLeaseCache interface {
	AcquireProxyProbe(ctx context.Context, proxyID int64, routeKey string, ttl time.Duration) (release func(), acquired bool, err error)
}
