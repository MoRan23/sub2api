package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetGroupOverviewMetrics returns only counters used by public cards and their
// whole-window TTFT percentiles. Detailed model/time-bucket distributions stay in
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
		SUM(m.input_tokens),SUM(m.cache_creation_tokens),SUM(m.cache_read_tokens),
		SUM(m.error_requests),SUM(m.output_tokens),SUM(m.ttft_sum_ms),SUM(m.ttft_count)
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
		if err := rows.Scan(&at, &id, &count.success, &count.availabilityErrors, &count.ready, &count.input, &count.creation, &count.read,
			&count.errors, &count.output, &count.ttftSum, &count.ttftCount); err != nil {
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
		out.Items[index].Buckets = append(out.Items[index].Buckets, service.ChannelMonitorV2TrendPoint{BucketStart: at, Metrics: count.metric(filter.Bucket.Minutes())})
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return nil, readErr
	}
	for i := range totals {
		out.Items[i].Metrics = totals[i].metric(effective.End.Sub(effective.Start).Minutes())
	}
	// Avoid a second query only when neither a group nor any counters exist.
	if len(out.Items) == 0 {
		return out, nil
	}
	where, args, _ = channelMonitorV2WhereWithRollup(effective, cfg, "h")
	rows, err = r.db.QueryContext(ctx, channelMonitorV2OverviewPercentilesSQL(channelMonitorV2HistogramTable(effective), where), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, median, p90 int64
		if err := rows.Scan(&id, &median, &p90); err != nil {
			return nil, err
		}
		if index, exists := byGroup[id]; exists {
			out.Items[index].Metrics.TTFT.P50Ms = &median
			out.Items[index].Metrics.TTFT.P90Ms = &p90
		}
	}
	return out, rows.Err()
}

// Merge platforms, models and times before the shared window scan. Only two
// percentile scalars per group cross the database boundary, even for 30-day views.
func channelMonitorV2OverviewPercentilesSQL(table, where string) string {
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
		SELECT group_id,MIN(upper_bound_ms),
		MIN(upper_bound_ms) FILTER (WHERE cumulative>=CEIL(total*0.9)) FROM ranked
	WHERE total>0 AND cumulative>=CEIL(total*0.5) GROUP BY group_id`
}

type channelMonitorV2OverviewCounter struct {
	success, errors, availabilityErrors, input, output, creation, read int64
	ttftSum, ttftCount                                                 int64
	ready                                                              bool
}

func (c *channelMonitorV2OverviewCounter) add(other channelMonitorV2OverviewCounter) {
	c.success += other.success
	c.errors += other.errors
	c.availabilityErrors += other.availabilityErrors
	c.input += other.input
	c.output += other.output
	c.creation += other.creation
	c.read += other.read
	c.ttftSum += other.ttftSum
	c.ttftCount += other.ttftCount
	c.ready = c.ready && other.ready
}

func (c channelMonitorV2OverviewCounter) metric(minutes float64) service.ChannelMonitorV2Metric {
	m := service.ChannelMonitorV2Metric{
		SuccessRequests: c.success, ErrorRequests: c.errors, RequestCount: c.success + c.errors,
		InputTokens: c.input, OutputTokens: c.output, CacheCreationTokens: c.creation, CacheReadTokens: c.read,
		TokenCount:                c.input + c.output + c.creation + c.read,
		AvailabilityErrorRequests: c.availabilityErrors, AvailabilitySampleCount: c.success + c.availabilityErrors,
		CacheRateNumerator: c.read, CacheRateDenominator: c.input + c.creation + c.read,
		TTFT: service.ChannelMonitorV2Latency{SampleCount: c.ttftCount},
	}
	if minutes > 0 {
		m.TPM = float64(m.TokenCount) / minutes
	}
	if c.ttftCount > 0 {
		avg := float64(c.ttftSum) / float64(c.ttftCount)
		m.TTFT.AvgMs = &avg
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
