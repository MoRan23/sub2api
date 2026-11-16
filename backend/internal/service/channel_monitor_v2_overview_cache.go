package service

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type channelOverviewData struct {
	matrix       *ChannelMonitorV2Matrix
	attributions map[int64]ChannelMonitorV2OverviewAttribution
	asOf         time.Time
}
type channelOverviewEntry struct {
	key     string
	data    *channelOverviewData
	expires time.Time
	bytes   int
}
type channelOverviewCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	bytes   int
	flight  singleflight.Group
}

func (c *channelOverviewCache) get(key string, now time.Time) *channelOverviewData {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil {
		v := e.Value.(*channelOverviewEntry)
		if now.Before(v.expires) {
			c.lru.MoveToFront(e)
			return v.data
		}
		delete(c.entries, key)
		c.bytes -= v.bytes
		c.lru.Remove(e)
	}
	return nil
}
func (c *channelOverviewCache) put(key string, data *channelOverviewData, now time.Time) {
	// Conservative accounting of scalar metric rows and bounded legacy evidence.
	size := len(key) + 512
	for _, row := range data.matrix.Items {
		size += 1024 + len(row.Platform) + len(row.GroupName) + len(row.Model) + len(row.Buckets)*1024
	}
	for _, a := range data.attributions {
		size += 512 + len(a.ProbeModel) + len(a.History)*256 + len(a.Recent)*4096
		for _, entry := range a.Recent {
			size += len(entry.ProbeModel) + len(entry.ReasonCode) + len(entry.Status)
		}
	}
	if size > 32<<20 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if old := c.entries[key]; old != nil {
		c.bytes -= old.Value.(*channelOverviewEntry).bytes
		c.lru.Remove(old)
	}
	c.entries[key] = c.lru.PushFront(&channelOverviewEntry{key: key, data: data, expires: now.Add(10 * time.Second), bytes: size})
	c.bytes += size
	for len(c.entries) > 256 || c.bytes > 32<<20 {
		e := c.lru.Back()
		v := e.Value.(*channelOverviewEntry)
		delete(c.entries, v.key)
		c.bytes -= v.bytes
		c.lru.Remove(e)
	}
}

func (s *ChannelMonitorV2Service) loadOverviewCached(ctx context.Context, filter ChannelMonitorV2Filter, cfg ChannelMonitorV2Config, metrics channelMonitorV2GroupMetricsReader, legacy bool) (*channelOverviewData, error) {
	now := s.now().UTC()
	filter.AllowedGroupIDs = append([]int64(nil), filter.AllowedGroupIDs...)
	sort.Slice(filter.AllowedGroupIDs, func(i, j int) bool { return filter.AllowedGroupIDs[i] < filter.AllowedGroupIDs[j] })
	encoded, err := json.Marshal(struct {
		Filter  ChannelMonitorV2Filter
		Config  ChannelMonitorV2Config
		Legacy  bool
		Version int
	}{filter, cfg, legacy, ChannelMonitorV2AggregationVersion})
	if err != nil {
		return nil, err
	}
	key := string(encoded)
	if data := s.overviewCache.get(key, now); data != nil {
		return data, nil
	}
	ch := s.overviewCache.flight.DoChan(key, func() (any, error) {
		if data := s.overviewCache.get(key, now); data != nil {
			return data, nil
		}
		// This read contains no request identity or body. One cancelled browser must
		// not abort another viewer's shared query; the loader has its own deadline.
		loadCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		matrix, err := metrics.GetGroupOverviewMetrics(loadCtx, filter, cfg)
		if err != nil {
			return nil, err
		}
		if matrix == nil {
			return nil, errors.New("group monitoring data unavailable")
		}
		asOf := now
		var attrs map[int64]ChannelMonitorV2OverviewAttribution
		if reader, ok := s.repo.(interface {
			GetGroupAttributionOverviewWithLegacy(context.Context, []int64, time.Time, bool) (map[int64]ChannelMonitorV2OverviewAttribution, error)
		}); ok {
			attrs, err = reader.GetGroupAttributionOverviewWithLegacy(loadCtx, filter.AllowedGroupIDs, asOf, legacy)
		} else if reader, ok := s.repo.(channelMonitorV2GroupAttributionReader); ok {
			attrs, err = reader.GetGroupAttributionOverview(loadCtx, filter.AllowedGroupIDs, asOf)
		} else {
			return nil, errors.New("group attribution history unavailable")
		}
		if err != nil {
			return nil, err
		}
		data := &channelOverviewData{matrix: matrix, attributions: attrs, asOf: asOf}
		s.overviewCache.put(key, data, now)
		return data, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*channelOverviewData), nil
	}
}
