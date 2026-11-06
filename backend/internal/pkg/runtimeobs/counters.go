// Package runtimeobs contains bounded, numeric-only process diagnostics.
package runtimeobs

import (
	"sync/atomic"
	"time"
)

const LargeBodyThreshold = 1 << 20

// FixedHistogram has six fixed upper bounds and an overflow bucket. Bucket
// counts are disjoint; callers choose and document the unit.
type FixedHistogram struct{ buckets [7]atomic.Uint64 }
type Histogram struct {
	Bounds [6]uint64 `json:"bounds"`
	Counts [7]uint64 `json:"counts"`
}

var DurationBounds = [6]uint64{1, 10, 100, 1000, 10000, 60000}
var SizeBounds = [6]uint64{1024, 16384, 65536, 262144, 1048576, 8388608}

func (h *FixedHistogram) Observe(value uint64, bounds [6]uint64) {
	for i, bound := range bounds {
		if value <= bound {
			h.buckets[i].Add(1)
			return
		}
	}
	h.buckets[6].Add(1)
}
func (h *FixedHistogram) ObserveDuration(d time.Duration) {
	h.Observe(uint64(max(d.Milliseconds(), 0)), DurationBounds)
}
func (h *FixedHistogram) Snapshot(bounds [6]uint64) Histogram {
	v := Histogram{Bounds: bounds}
	for i := range v.Counts {
		v.Counts[i] = h.buckets[i].Load()
	}
	return v
}

type CacheStats struct {
	Name           string `json:"name"`
	Entries        int64  `json:"entries"`
	EstimatedBytes int64  `json:"estimated_bytes"`
	MaxEntries     int64  `json:"max_entries"`
	MaxBytes       int64  `json:"max_bytes"`
	Hits           uint64 `json:"hits"`
	Misses         uint64 `json:"misses"`
	Evictions      uint64 `json:"evictions"`
	TTLSeconds     int64  `json:"ttl_seconds"`
}
type CacheStatsProvider interface{ RuntimeCacheStats() []CacheStats }

// Default only stores counters: no request, body, header, identity or cache key.
var Default Counters

type Counters struct {
	bodyCount           atomic.Int64
	bodyBytes           atomic.Int64
	largeCount          atomic.Int64
	largeBytes          atomic.Int64
	streams             atomic.Int64
	sseBuffers          atomic.Int64
	sseBytes            atomic.Int64
	sseMax              atomic.Uint64
	bodySizes           FixedHistogram
	sseSizes            FixedHistogram
	settlementSucceeded atomic.Uint64
	settlementFailed    atomic.Uint64
}

type SettlementStats struct {
	Succeeded uint64
	Failed    uint64
}

// ObserveSettlement records the returned outcome of one ordinary usage billing
// operation. It retains neither the error nor any billing/request data.
func (c *Counters) ObserveSettlement(err error) {
	if err != nil {
		c.settlementFailed.Add(1)
		return
	}
	c.settlementSucceeded.Add(1)
}

func (c *Counters) SettlementSnapshot() SettlementStats {
	return SettlementStats{Succeeded: c.settlementSucceeded.Load(), Failed: c.settlementFailed.Load()}
}

type Activity struct {
	ActiveBodies         int64     `json:"active_bodies"`
	ActiveBodyBytes      int64     `json:"active_body_bytes"`
	ActiveLargeBodies    int64     `json:"active_large_bodies"`
	ActiveLargeBodyBytes int64     `json:"active_large_body_bytes"`
	ActiveSSE            int64     `json:"active_sse"`
	ActiveSSEBuffers     int64     `json:"active_sse_buffers"`
	ActiveSSEBufferBytes int64     `json:"active_sse_buffer_bytes"`
	SSEBufferMaxBytes    uint64    `json:"sse_buffer_max_bytes"`
	BodySize             Histogram `json:"body_size_bytes"`
	SSEBufferSize        Histogram `json:"sse_buffer_size_bytes"`
}

// BeginBody tracks the lifetime of an already-buffered body; call the returned
// release exactly once when the handler stops owning it.
func (c *Counters) BeginBody(size int64) func() {
	size = max(size, 0)
	c.bodyCount.Add(1)
	c.bodyBytes.Add(size)
	c.bodySizes.Observe(uint64(size), SizeBounds)
	large := size >= LargeBodyThreshold
	if large {
		c.largeCount.Add(1)
		c.largeBytes.Add(size)
	}
	return func() {
		c.bodyCount.Add(-1)
		c.bodyBytes.Add(-size)
		if large {
			c.largeCount.Add(-1)
			c.largeBytes.Add(-size)
		}
	}
}
func (c *Counters) BeginSSE() func() { c.streams.Add(1); return func() { c.streams.Add(-1) } }

// SSEBufferGauge has one owner; ownership may transfer through synchronization.
// Update it after capacity changes and release at stream exit. It retains only
// capacities, never buffers, and concurrent calls on one gauge are unsupported.
type SSEBufferGauge struct {
	c     *Counters
	bytes int64
	count int64
}

func (c *Counters) TrackSSEBuffers() *SSEBufferGauge { return &SSEBufferGauge{c: c} }
func (g *SSEBufferGauge) Update(rawCapacity, scratchCapacity int) {
	rawCapacity = max(rawCapacity, 0)
	scratchCapacity = max(scratchCapacity, 0)
	n := int64(rawCapacity) + int64(scratchCapacity)
	var count int64
	if rawCapacity > 0 {
		count++
	}
	if scratchCapacity > 0 {
		count++
	}
	if n == g.bytes && count == g.count {
		return
	}
	g.c.sseBytes.Add(n - g.bytes)
	g.c.sseBuffers.Add(count - g.count)
	g.bytes = n
	g.count = count
}
func (g *SSEBufferGauge) Release() { g.Update(0, 0) }
func (c *Counters) ObserveSSEBuffer(size int64) {
	n := uint64(max(size, 0))
	c.sseSizes.Observe(n, SizeBounds)
	for old := c.sseMax.Load(); n > old; old = c.sseMax.Load() {
		if c.sseMax.CompareAndSwap(old, n) {
			break
		}
	}
}
func (c *Counters) Snapshot() Activity {
	return Activity{
		ActiveBodies: c.bodyCount.Load(), ActiveBodyBytes: c.bodyBytes.Load(),
		ActiveLargeBodies: c.largeCount.Load(), ActiveLargeBodyBytes: c.largeBytes.Load(),
		ActiveSSE: c.streams.Load(), ActiveSSEBuffers: c.sseBuffers.Load(), ActiveSSEBufferBytes: c.sseBytes.Load(),
		SSEBufferMaxBytes: c.sseMax.Load(), BodySize: c.bodySizes.Snapshot(SizeBounds), SSEBufferSize: c.sseSizes.Snapshot(SizeBounds),
	}
}
