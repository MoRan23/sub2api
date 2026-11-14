package service

import (
	"context"
	"errors"
	"sort"
	"time"
)

// Overview responses deliberately contain no request volumes, account identity,
// upstream diagnostics or attribution snapshots. Permission filtering is applied
// before repository reads, and the resulting projection is never shared by users.
type ChannelMonitorV2GroupOverview struct {
	Range                  string                              `json:"range"`
	RefreshIntervalSeconds int                                 `json:"refresh_interval_seconds"`
	Coverage               ChannelMonitorV2Coverage            `json:"coverage"`
	Items                  []ChannelMonitorV2GroupOverviewItem `json:"items"`
}

type ChannelMonitorV2GroupOverviewItem struct {
	GroupID           int64                               `json:"group_id"`
	GroupName         string                              `json:"group_name"`
	GroupI18n         GroupI18n                           `json:"-"`
	Platform          string                              `json:"platform"`
	RateMultiplier    float64                             `json:"rate_multiplier"`
	MonitoringEnabled bool                                `json:"monitoring_enabled"`
	State             string                              `json:"state"`
	Metrics           ChannelMonitorV2OverviewMetrics     `json:"metrics"`
	Buckets           []ChannelMonitorV2OverviewBucket    `json:"buckets"`
	Attribution       ChannelMonitorV2OverviewAttribution `json:"attribution"`
}

type ChannelMonitorV2OverviewMetrics struct {
	AvailabilityRate *float64 `json:"availability_rate"`
	CacheRate        *float64 `json:"cache_rate"`
	TTFTP50Ms        *int64   `json:"ttft_p50_ms"`
}

type ChannelMonitorV2OverviewBucket struct {
	BucketStart      time.Time `json:"bucket_start"`
	State            string    `json:"state"`
	AvailabilityRate *float64  `json:"availability_rate"`
}

type ChannelMonitorV2AttributionEntry struct {
	FinishedAt time.Time `json:"finished_at"`
	ProbeModel string    `json:"probe_model"`
	Status     string    `json:"status"`
	ReasonCode string    `json:"reason_code"`
}

type ChannelMonitorV2OverviewAttribution struct {
	Latest       *ChannelMonitorV2AttributionEntry   `json:"latest"`
	Recent       []ChannelMonitorV2AttributionEntry  `json:"recent"`
	Enabled      bool                                `json:"enabled"`
	ProbeModel   string                              `json:"probe_model"`
	AsOf         time.Time                           `json:"as_of"`
	SummaryState string                              `json:"summary_state"`
	History      []ChannelMonitorV2AttributionBucket `json:"history"`
}

type ChannelMonitorV2AttributionBucket struct {
	BucketStart time.Time `json:"bucket_start"`
	BucketEnd   time.Time `json:"bucket_end"`
	State       string    `json:"state"`
}

// NewChannelMonitorV2OverviewAttribution also supplies the complete empty-group
// timeline. Its last sample is the current summary's exact evaluation instant.
func NewChannelMonitorV2OverviewAttribution(asOf time.Time) ChannelMonitorV2OverviewAttribution {
	out := ChannelMonitorV2OverviewAttribution{
		AsOf: asOf, SummaryState: "no_data", Recent: []ChannelMonitorV2AttributionEntry{},
		History: make([]ChannelMonitorV2AttributionBucket, 20),
	}
	for i := range out.History {
		end := asOf.Add(time.Duration(i-19) * 10 * time.Minute)
		out.History[i] = ChannelMonitorV2AttributionBucket{BucketStart: end.Add(-10 * time.Minute), BucketEnd: end, State: "no_data"}
	}
	return out
}

// Optional extension keeps the existing detailed monitoring repository contract
// independent of attribution storage.
type channelMonitorV2GroupAttributionReader interface {
	GetGroupAttributionOverview(context.Context, []int64, time.Time) (map[int64]ChannelMonitorV2OverviewAttribution, error)
}

func (s *ChannelMonitorV2Service) GetGroupOverview(ctx context.Context, filter ChannelMonitorV2Filter, groups []Group, userRates map[int64]float64) (*ChannelMonitorV2GroupOverview, error) {
	cfg, err := s.getEnabledConfig(ctx)
	if err != nil {
		return nil, err
	}
	// Ignore any caller-supplied group/model/platform filter: only the trusted
	// authorization result intersected with monitoring configuration defines scope.
	filter.GroupIDs, filter.Platforms, filter.Models = nil, nil, nil
	filter.RestrictGroups = true
	filter.AllowedGroupIDs = make([]int64, 0, len(groups))
	allowed := make([]Group, 0, len(groups))
	seen := make(map[int64]struct{}, len(groups))
	for _, group := range groups {
		if group.ID <= 0 || group.Status != StatusActive || !channelMonitorV2GroupMonitored(group, *cfg) {
			continue
		}
		if _, exists := seen[group.ID]; exists {
			continue
		}
		seen[group.ID] = struct{}{}
		allowed = append(allowed, group)
		filter.AllowedGroupIDs = append(filter.AllowedGroupIDs, group.ID)
	}
	out := &ChannelMonitorV2GroupOverview{Range: filter.Range, RefreshIntervalSeconds: cfg.RefreshIntervalSeconds, Items: make([]ChannelMonitorV2GroupOverviewItem, 0, len(allowed))}
	out.Coverage = ChannelMonitorV2Coverage{RequestedStart: filter.Start, RequestedEnd: filter.End, BucketSeconds: int(filter.Bucket.Seconds())}
	if len(allowed) == 0 {
		return out, nil
	}
	// Internal group dimension merges raw counts and histogram bins across actual
	// upstream platforms, including Composite; it never averages rates or P50s.
	matrix, err := s.repo.GetMatrix(ctx, filter, *cfg, ChannelMonitorV2GroupBy("group"), false)
	if err != nil {
		return nil, err
	}
	if matrix == nil {
		return nil, errors.New("group monitoring data unavailable")
	}
	out.Coverage = matrix.Coverage
	reader, ok := s.repo.(channelMonitorV2GroupAttributionReader)
	if !ok {
		return nil, errors.New("group attribution history unavailable")
	}
	// The monitor filter ends at an aligned bucket boundary, which can be hours
	// old. Attribution always evaluates against a single current clock reading.
	asOf := s.now().UTC()
	attributions, err := reader.GetGroupAttributionOverview(ctx, filter.AllowedGroupIDs, asOf)
	if err != nil {
		return nil, err
	}
	rows := make(map[int64]*ChannelMonitorV2MatrixRow, len(matrix.Items))
	for i := range matrix.Items {
		if id := matrix.Items[i].GroupID; id != nil {
			rows[*id] = &matrix.Items[i]
		}
	}
	sort.Slice(allowed, func(i, j int) bool {
		if allowed[i].Platform != allowed[j].Platform {
			return allowed[i].Platform < allowed[j].Platform
		}
		if allowed[i].SortOrder != allowed[j].SortOrder {
			return allowed[i].SortOrder < allowed[j].SortOrder
		}
		return allowed[i].ID < allowed[j].ID
	})
	for _, group := range allowed {
		monitored := true // Already filtered before any statistics or attribution reads.
		item := ChannelMonitorV2GroupOverviewItem{GroupID: group.ID, GroupName: group.Name, GroupI18n: group.I18n, Platform: group.Platform, RateMultiplier: group.RateMultiplier, MonitoringEnabled: monitored}
		if rate, exists := userRates[group.ID]; exists {
			item.RateMultiplier = rate
		}
		metric := ChannelMonitorV2Metric{}
		bucketMetrics := map[int64]ChannelMonitorV2Metric{}
		if row := rows[group.ID]; row != nil {
			metric = row.Metrics
			for _, bucket := range row.Buckets {
				bucketMetrics[bucket.BucketStart.Unix()] = bucket.Metrics
			}
		}
		item.State = channelMonitorV2OverviewState(metric, monitored, out.Coverage.AvailabilityReady, cfg.HealthThresholds)
		if monitored && out.Coverage.AvailabilityReady {
			item.Metrics.AvailabilityRate = metric.AvailabilityRate
		}
		if monitored {
			if metric.CacheRateDenominator > 0 {
				rate := metric.CacheRate
				item.Metrics.CacheRate = &rate
			}
			item.Metrics.TTFTP50Ms = metric.TTFT.P50Ms
		}
		item.Buckets = make([]ChannelMonitorV2OverviewBucket, 0, int(filter.End.Sub(filter.Start)/filter.Bucket))
		for at := filter.Start; at.Before(filter.End); at = at.Add(filter.Bucket) {
			m := bucketMetrics[at.Unix()]
			ready := out.Coverage.AvailabilityReady || (out.Coverage.AvailabilityVersion >= ChannelMonitorV2AggregationVersion && !out.Coverage.AvailabilityStart.IsZero() && !at.Before(out.Coverage.AvailabilityStart))
			bucket := ChannelMonitorV2OverviewBucket{BucketStart: at, State: channelMonitorV2OverviewState(m, monitored, ready, cfg.HealthThresholds)}
			if monitored && ready {
				bucket.AvailabilityRate = m.AvailabilityRate
			}
			item.Buckets = append(item.Buckets, bucket)
		}
		item.Attribution = attributions[group.ID]
		if item.Attribution.History == nil {
			item.Attribution = NewChannelMonitorV2OverviewAttribution(asOf)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func channelMonitorV2GroupMonitored(group Group, cfg ChannelMonitorV2Config) bool {
	if len(cfg.GroupIDs) > 0 {
		found := false
		for _, id := range cfg.GroupIDs {
			found = found || id == group.ID
		}
		if !found {
			return false
		}
	}
	for _, platform := range cfg.Platforms {
		if platform.Enabled && (platform.Platform == group.Platform || group.Platform == PlatformComposite) {
			return true
		}
	}
	return false
}

func channelMonitorV2OverviewState(metric ChannelMonitorV2Metric, monitored, ready bool, thresholds ChannelMonitorV2HealthThresholds) string {
	if !monitored {
		return "unmonitored"
	}
	if !ready {
		return "rebuilding"
	}
	if metric.AvailabilitySampleCount <= 0 || metric.AvailabilityRate == nil {
		return "no_data"
	}
	if metric.AvailabilitySampleCount < thresholds.MinimumSample {
		return "insufficient"
	}
	errorRate := float64(metric.AvailabilityErrorRequests) / float64(metric.AvailabilitySampleCount)
	if errorRate >= thresholds.CriticalErrorRate {
		return "critical"
	}
	if errorRate >= thresholds.WarningErrorRate {
		return "warning"
	}
	return "healthy"
}
