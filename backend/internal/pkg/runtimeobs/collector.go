package runtimeobs

import (
	"context"
	"os"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/process"
)

const (
	SampleInterval = 10 * time.Second
	HistoryLimit   = 360
	StaleAfter     = 30 * time.Second
)

var processID = uuid.NewString()
var processStarted = time.Now().UTC()

// InstanceID is shared by all runtime collectors in this process.
func InstanceID() string { return processID }

type QueueStats struct {
	Capacity            int       `json:"capacity"`
	Waiting             uint64    `json:"waiting"`
	Running             int64     `json:"running"`
	Submitted           uint64    `json:"submitted"`
	Completed           uint64    `json:"completed"`
	Failed              uint64    `json:"failed"`
	Dropped             uint64    `json:"dropped"`
	SyncFallback        uint64    `json:"sync_fallback"`
	SettlementSucceeded uint64    `json:"settlement_succeeded"`
	SettlementFailed    uint64    `json:"settlement_failed"`
	WaitMS              Histogram `json:"wait_ms"`
	ExecutionMS         Histogram `json:"execution_ms"`
}
type PauseHistogram struct {
	Bounds [6]float64 `json:"bounds"`
	Counts [7]uint64  `json:"counts"`
}
type Sample struct {
	At                time.Time      `json:"at"`
	SysBytes          uint64         `json:"sys_bytes"`
	RSSBytes          uint64         `json:"rss_bytes"`
	RSSAvailable      bool           `json:"rss_available"`
	HeapAllocBytes    uint64         `json:"heap_alloc_bytes"`
	HeapInuseBytes    uint64         `json:"heap_inuse_bytes"`
	HeapIdleBytes     uint64         `json:"heap_idle_bytes"`
	HeapReleasedBytes uint64         `json:"heap_released_bytes"`
	HeapObjects       uint64         `json:"heap_objects"`
	StackInuseBytes   uint64         `json:"stack_inuse_bytes"`
	GCCycles          uint64         `json:"gc_cycles"`
	AllocBytesTotal   uint64         `json:"alloc_bytes_total"`
	FreeBytesTotal    uint64         `json:"free_bytes_total"`
	Goroutines        uint64         `json:"goroutines"`
	GCPauseSeconds    PauseHistogram `json:"gc_pause_seconds"`
	Activity          Activity       `json:"activity"`
	Caches            []CacheStats   `json:"caches"`
	UsageQueue        QueueStats     `json:"usage_queue"`
}
type Instance struct {
	InstanceID string    `json:"instance_id"`
	StartedAt  time.Time `json:"started_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	Stale      bool      `json:"stale"`
}
type View struct {
	CurrentInstanceID     string       `json:"current_instance_id"`
	SelectedInstanceID    string       `json:"selected_instance_id"`
	SampleIntervalSeconds int          `json:"sample_interval_seconds"`
	HistoryLimit          int          `json:"history_limit"`
	StaleAfterSeconds     int          `json:"stale_after_seconds"`
	RedisAvailable        bool         `json:"redis_available"`
	Truncated             bool         `json:"truncated"`
	Instances             []Instance   `json:"instances"`
	History               []Sample     `json:"history"`
	Profile               ProfileState `json:"profile"`
}
type Store interface {
	Save(context.Context, Instance, Sample) error
	Read(context.Context, string) ([]Instance, []Sample, error)
}
type Collector struct {
	store               Store
	enabled             func() bool
	cacheProviders      []CacheStatsProvider
	queue               func() QueueStats
	mu                  sync.RWMutex
	ring                [HistoryLimit]Sample
	next, count         int
	startOnce, stopOnce sync.Once
	stop                chan struct{}
	done                chan struct{}
	profile             ProfileGate
}

func NewCollector(store Store, enabled func() bool, queue func() QueueStats, caches ...CacheStatsProvider) *Collector {
	return &Collector{store: store, enabled: enabled, queue: queue, cacheProviders: caches, stop: make(chan struct{}), done: make(chan struct{})}
}
func (c *Collector) Start() {
	c.startOnce.Do(func() {
		go func() {
			defer close(c.done)
			select {
			case <-c.stop:
				return
			default:
			}
			c.collect()
			t := time.NewTicker(SampleInterval)
			defer t.Stop()
			for {
				select {
				case <-c.stop:
					return
				case <-t.C:
					c.collect()
				}
			}
		}()
	})
}
func (c *Collector) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() { close(c.stop) })
	c.Start()
	<-c.done
}
func (c *Collector) collect() {
	if c.enabled != nil && !c.enabled() {
		c.profile.Disable()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := ReadSample(ctx)
	if c.queue != nil {
		s.UsageQueue = c.queue()
	}
	for _, provider := range c.cacheProviders {
		if provider != nil {
			s.Caches = append(s.Caches, provider.RuntimeCacheStats()...)
		}
	}
	c.mu.Lock()
	c.ring[c.next] = s
	c.next = (c.next + 1) % HistoryLimit
	c.count = min(c.count+1, HistoryLimit)
	c.mu.Unlock()
	if c.store != nil {
		_ = c.store.Save(ctx, Instance{InstanceID: processID, StartedAt: processStarted, LastSeenAt: s.At}, s)
	}
}
func (c *Collector) localHistory() []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Sample, 0, c.count)
	for i := 0; i < c.count; i++ {
		out = append(out, c.ring[(c.next-c.count+i+HistoryLimit)%HistoryLimit])
	}
	return out
}
func (c *Collector) View(ctx context.Context, selected string) View {
	if selected == "" {
		selected = processID
	}
	v := View{
		CurrentInstanceID: processID, SelectedInstanceID: selected,
		SampleIntervalSeconds: int(SampleInterval.Seconds()), HistoryLimit: HistoryLimit,
		StaleAfterSeconds: int(StaleAfter.Seconds()),
		Instances:         []Instance{}, History: []Sample{}, Profile: c.profile.State(),
	}
	if c.store != nil {
		instances, history, err := c.store.Read(ctx, selected)
		if err == nil {
			v.RedisAvailable = true
			v.Instances = instances
			v.History = history
			if bounded, ok := c.store.(interface {
				Truncated(context.Context) (bool, error)
			}); ok {
				v.Truncated, err = bounded.Truncated(ctx)
				if err != nil {
					v.RedisAvailable = false
				}
			}
		}
	}
	local := c.localHistory()
	self := Instance{InstanceID: processID, StartedAt: processStarted}
	if len(local) > 0 {
		self.LastSeenAt = local[len(local)-1].At
	}
	self.Stale = time.Since(self.LastSeenAt) > StaleAfter
	filtered := v.Instances[:0]
	for _, inst := range v.Instances {
		if inst.InstanceID != processID {
			inst.Stale = time.Since(inst.LastSeenAt) > StaleAfter
			filtered = append(filtered, inst)
		}
	}
	v.Instances = append(filtered, self)
	if selected == processID {
		v.History = local
	}
	return v
}
func (c *Collector) Profile() *ProfileGate { return &c.profile }

var metricNames = []string{
	"/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/free:bytes", "/memory/classes/heap/released:bytes",
	"/gc/heap/objects:objects", "/memory/classes/heap/stacks:bytes",
	"/gc/cycles/total:gc-cycles", "/gc/heap/allocs:bytes", "/gc/heap/frees:bytes",
	"/sched/goroutines:goroutines", "/gc/pauses:seconds", "/memory/classes/total:bytes",
}

// ReadSample uses runtime/metrics and never forces GC or changes MemProfileRate.
func ReadSample(ctx context.Context) Sample {
	values := make([]metrics.Sample, len(metricNames))
	for i, n := range metricNames {
		values[i].Name = n
	}
	metrics.Read(values)
	u := func(i int) uint64 {
		if values[i].Value.Kind() == metrics.KindUint64 {
			return values[i].Value.Uint64()
		}
		return 0
	}
	s := Sample{
		At: time.Now().UTC(), SysBytes: u(11),
		HeapAllocBytes: u(0), HeapInuseBytes: u(0) + u(1), HeapIdleBytes: u(2) + u(3),
		HeapReleasedBytes: u(3), HeapObjects: u(4), StackInuseBytes: u(5),
		GCCycles: u(6), AllocBytesTotal: u(7), FreeBytesTotal: u(8), Goroutines: u(9),
		Activity: Default.Snapshot(), Caches: []CacheStats{},
	}
	s.GCPauseSeconds.Bounds = [6]float64{.001, .005, .01, .025, .05, .1}
	if values[10].Value.Kind() == metrics.KindFloat64Histogram {
		h := values[10].Value.Float64Histogram()
		for i, n := range h.Counts {
			bucket := 6
			for j, b := range s.GCPauseSeconds.Bounds {
				if h.Buckets[i+1] <= b {
					bucket = j
					break
				}
			}
			s.GCPauseSeconds.Counts[bucket] += n
		}
	}
	if p, err := process.NewProcessWithContext(ctx, int32(os.Getpid())); err == nil {
		if mem, err := p.MemoryInfoWithContext(ctx); err == nil {
			s.RSSBytes = mem.RSS
			s.RSSAvailable = true
		}
	}
	return s
}
