package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetGroupOverviewMetrics returns only counters used by public cards and their
// whole-window TTFT P50. Detailed model/time-bucket distributions stay in
// GetMatrix; neither their rows nor their percentile maps enter this path.
func (r *channelMonitorV2Repository) GetGroupOverviewMetrics(ctx context.Context, filter service.ChannelMonitorV2Filter, cfg service.ChannelMonitorV2Config) (*service.ChannelMonitorV2Matrix, error) {
	filter.IncludeUnroutedComposite = true
	filter.Models = nil // Group overview always includes every model.
	out := &service.ChannelMonitorV2Matrix{GroupBy: service.ChannelMonitorV2GroupByGroup, Items: []service.ChannelMonitorV2MatrixRow{}}
	if channelMonitorV2RestrictedGroupScopeEmpty(filter, cfg) {
		return out, nil
	}
	coverage, err := r.loadCoverage(ctx, filter)
	if err != nil {
		return nil, err
	}
	out.Coverage = *coverage
	effective := channelMonitorV2CommonCoverageFilter(filter, *coverage)
	if !effective.Start.Before(effective.End) {
		return out, nil
	}
	where, args, seconds := channelMonitorV2WhereWithRollup(effective, cfg, "m")
	bucket := "m.bucket_start"
	if seconds == 0 {
		args = append([]any{fmt.Sprintf("%d seconds", int(filter.Bucket.Seconds()))}, args...)
		where = shiftSQLPlaceholders(where, 1)
		bucket = channelMonitorV2DateBinExpr("m.bucket_start")
	}
	query := `SELECT ` + bucket + `,m.group_id,SUM(m.success_requests),
		COALESCE(SUM(m.availability_error_requests),0),
		BOOL_AND(m.aggregation_version >= 2 AND m.availability_error_requests IS NOT NULL),
		SUM(m.input_tokens),SUM(m.cache_creation_tokens),SUM(m.cache_read_tokens)
		FROM ` + channelMonitorV2MetricsTable(effective) + ` m ` + where + ` AND m.group_id > 0
		GROUP BY 1,2 ORDER BY 2,1`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	byGroup := make(map[int64]int)
	totals := make([]channelMonitorV2OverviewCounter, 0)
	// Seed the already authorized scope without loading group metadata again.
	// Retained histograms can still exist while a counter interval is empty.
	for _, id := range configuredChannelMonitorV2GroupIDs(filter, cfg) {
		if _, exists := byGroup[id]; exists {
			continue
		}
		byGroup[id] = len(out.Items)
		groupID := id
		out.Items = append(out.Items, service.ChannelMonitorV2MatrixRow{GroupID: &groupID, Buckets: []service.ChannelMonitorV2TrendPoint{}})
		totals = append(totals, channelMonitorV2OverviewCounter{ready: true})
	}
	for rows.Next() {
		var at time.Time
		var id int64
		var count channelMonitorV2OverviewCounter
		if err := rows.Scan(&at, &id, &count.success, &count.errors, &count.ready, &count.input, &count.creation, &count.read); err != nil {
			_ = rows.Close()
			return nil, err
		}
		index, exists := byGroup[id]
		if !exists {
			index = len(out.Items)
			byGroup[id] = index
			groupID := id
			out.Items = append(out.Items, service.ChannelMonitorV2MatrixRow{GroupID: &groupID, Buckets: []service.ChannelMonitorV2TrendPoint{}})
			totals = append(totals, channelMonitorV2OverviewCounter{ready: true})
		}
		totals[index].add(count)
		out.Items[index].Buckets = append(out.Items[index].Buckets, service.ChannelMonitorV2TrendPoint{BucketStart: at, Metrics: count.metric()})
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	for i := range totals {
		out.Items[i].Metrics = totals[i].metric()
	}
	// Avoid a second query only when neither a group nor any counters exist.
	if len(out.Items) == 0 {
		return out, nil
	}
	where, args, _ = channelMonitorV2WhereWithRollup(effective, cfg, "h")
	rows, err = r.db.QueryContext(ctx, channelMonitorV2OverviewP50SQL(channelMonitorV2HistogramTable(effective), where), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, median int64
		if err := rows.Scan(&id, &median); err != nil {
			return nil, err
		}
		if index, exists := byGroup[id]; exists {
			value := median
			out.Items[index].Metrics.TTFT.P50Ms = &value
		}
	}
	return out, rows.Err()
}

// Merge platforms, models and times before the window scan. Only a single
// scalar per group crosses the database boundary, including for 30-day views.
func channelMonitorV2OverviewP50SQL(table, where string) string {
	return `WITH bins AS (
		SELECT h.group_id,h.upper_bound_ms,SUM(h.sample_count) AS samples
		FROM ` + table + ` h ` + where + ` AND h.user_id=0 AND h.metric='ttft' AND h.group_id>0
		GROUP BY h.group_id,h.upper_bound_ms
	), ranked AS (
		SELECT group_id,upper_bound_ms,
		SUM(samples) OVER (PARTITION BY group_id ORDER BY upper_bound_ms ROWS UNBOUNDED PRECEDING) AS cumulative,
		SUM(samples) OVER (PARTITION BY group_id) AS total
		FROM bins
	)
	SELECT group_id,MIN(upper_bound_ms) FROM ranked
	WHERE total>0 AND cumulative>=CEIL(total*0.5) GROUP BY group_id`
}

type channelMonitorV2OverviewCounter struct {
	success, errors, input, creation, read int64
	ready                                  bool
}

func (c *channelMonitorV2OverviewCounter) add(other channelMonitorV2OverviewCounter) {
	c.success += other.success
	c.errors += other.errors
	c.input += other.input
	c.creation += other.creation
	c.read += other.read
	c.ready = c.ready && other.ready
}

func (c channelMonitorV2OverviewCounter) metric() service.ChannelMonitorV2Metric {
	m := service.ChannelMonitorV2Metric{
		AvailabilityErrorRequests: c.errors, AvailabilitySampleCount: c.success + c.errors,
		CacheRateNumerator: c.read, CacheRateDenominator: c.input + c.creation + c.read,
	}
	if c.ready && m.AvailabilitySampleCount > 0 {
		rate := float64(c.success) / float64(m.AvailabilitySampleCount)
		m.AvailabilityRate = &rate
	}
	if m.CacheRateDenominator > 0 {
		m.CacheRate = float64(c.read) / float64(m.CacheRateDenominator)
	}
	return m
}
