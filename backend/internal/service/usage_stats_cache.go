package service

import (
	"container/list"
	"context"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"golang.org/x/sync/singleflight"
)

const usageStatsTTL = 10 * time.Second
const usageStatsMaxEntries = 4096
const usageStatsMaxBytes = 16 << 20

type usageStatsCacheEntry struct {
	key     string
	value   *usagestats.UserDashboardStats
	expires time.Time
	bytes   int
}
type usageStatsCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	bytes   int
	flights singleflight.Group
}

func cloneUsageDashboardStats(v *usagestats.UserDashboardStats) *usagestats.UserDashboardStats {
	if v == nil {
		return nil
	}
	copy := *v
	copy.ByPlatform = append([]usagestats.PlatformDashboardStats(nil), v.ByPlatform...)
	return &copy
}
func usageDashboardBytes(v *usagestats.UserDashboardStats) int {
	n := int(unsafe.Sizeof(*v)) + len(v.ByPlatform)*int(unsafe.Sizeof(usagestats.PlatformDashboardStats{}))
	for _, p := range v.ByPlatform {
		n += len(p.Platform)
	}
	return n
}
func (c *usageStatsCache) get(key string) *usagestats.UserDashboardStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el := c.entries[key]; el != nil {
		entry := el.Value.(*usageStatsCacheEntry)
		if time.Now().Before(entry.expires) {
			c.lru.MoveToFront(el)
			return entry.value
		}
		c.remove(el)
	}
	return nil
}
func (c *usageStatsCache) remove(el *list.Element) {
	e := el.Value.(*usageStatsCacheEntry)
	delete(c.entries, e.key)
	c.bytes -= e.bytes
	c.lru.Remove(el)
}
func (c *usageStatsCache) put(key string, value *usagestats.UserDashboardStats) {
	size := usageDashboardBytes(value) + len(key) + 128
	if size > usageStatsMaxBytes {
		return
	}
	owned := cloneUsageDashboardStats(value)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if el := c.entries[key]; el != nil {
		c.remove(el)
	}
	for len(c.entries) >= usageStatsMaxEntries || c.bytes+size > usageStatsMaxBytes {
		c.remove(c.lru.Back())
	}
	c.entries[key] = c.lru.PushFront(&usageStatsCacheEntry{key, owned, time.Now().Add(usageStatsTTL), size})
	c.bytes += size
}
func (s *UsageService) cachedDashboardStats(ctx context.Context, id int64, byKey bool) (*usagestats.UserDashboardStats, error) {
	key := strconv.FormatBool(byKey) + ":" + strconv.FormatInt(id, 10) + ":" + GroupUsageTimezoneName() + ":" + GroupUsageDate(time.Now()) + ":v1"
	if value := s.statsCache.get(key); value != nil {
		return cloneUsageDashboardStats(value), nil
	}
	ch := s.statsCache.flights.DoChan(key, func() (any, error) {
		if value := s.statsCache.get(key); value != nil {
			return value, nil
		}
		loadCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var value *usagestats.UserDashboardStats
		var err error
		if byKey {
			value, err = s.usageRepo.GetAPIKeyDashboardStats(loadCtx, id)
		} else {
			value, err = s.usageRepo.GetUserDashboardStats(loadCtx, id)
		}
		if err == nil && value != nil {
			s.statsCache.put(key, value)
		}
		return value, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		value, _ := result.Val.(*usagestats.UserDashboardStats)
		return cloneUsageDashboardStats(value), nil
	}
}
