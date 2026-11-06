package runtimeobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCountersConcurrentAndBounded(t *testing.T) {
	var c Counters
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				release := c.BeginBody(LargeBodyThreshold)
				stream := c.BeginSSE()
				c.ObserveSSEBuffer(128 << 10)
				stream()
				release()
			}
		}()
	}
	wg.Wait()
	s := c.Snapshot()
	require.Zero(t, s.ActiveBodies)
	require.Zero(t, s.ActiveBodyBytes)
	require.Zero(t, s.ActiveLargeBodyBytes)
	require.Zero(t, s.ActiveSSE)
	require.EqualValues(t, 128<<10, s.SSEBufferMaxBytes)
	var total uint64
	for _, n := range s.BodySize.Counts {
		total += n
	}
	require.EqualValues(t, 3200, total)
}

func TestSettlementCountersKeepBusinessOutcomesSeparate(t *testing.T) {
	var c Counters
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				c.ObserveSettlement(nil)
				c.ObserveSettlement(errors.New("fixture settlement failure"))
			}
		})
	}
	wg.Wait()
	stats := c.SettlementSnapshot()
	require.EqualValues(t, 1600, stats.Succeeded)
	require.EqualValues(t, 1600, stats.Failed)
	require.Zero(t, c.Snapshot().ActiveBodies)
}

func TestSSEBufferGaugeTracksGrowthDropAndRelease(t *testing.T) {
	var c Counters
	a, b := c.TrackSSEBuffers(), c.TrackSSEBuffers()
	a.Update(4096, 8192)
	b.Update(1<<20, 0)
	s := c.Snapshot()
	require.EqualValues(t, 3, s.ActiveSSEBuffers)
	require.EqualValues(t, (1<<20)+12288, s.ActiveSSEBufferBytes)
	b.Update(0, 0)
	a.Update(65536, 4096)
	s = c.Snapshot()
	require.EqualValues(t, 2, s.ActiveSSEBuffers)
	require.EqualValues(t, 69632, s.ActiveSSEBufferBytes)
	a.Release()
	b.Release()
	require.Zero(t, c.Snapshot().ActiveSSEBuffers)
	require.Zero(t, c.Snapshot().ActiveSSEBufferBytes)
}
func TestReadSampleJSONAndRuntimeValues(t *testing.T) {
	s := ReadSample(context.Background())
	require.Positive(t, s.Goroutines)
	require.Positive(t, s.SysBytes)
	require.GreaterOrEqual(t, s.AllocBytesTotal, s.HeapAllocBytes)
	_, err := json.Marshal(s)
	require.NoError(t, err)
}

type brokenStore struct{}

func (brokenStore) Save(context.Context, Instance, Sample) error { return errors.New("offline") }
func (brokenStore) Read(context.Context, string) ([]Instance, []Sample, error) {
	return nil, nil, errors.New("offline")
}
func TestCollectorRingAndRedisFailure(t *testing.T) {
	c := NewCollector(brokenStore{}, func() bool { return true }, nil)
	// Populate the ring directly so the bound test does not make 365 OS reads.
	for i := 0; i < HistoryLimit+5; i++ {
		c.ring[c.next] = Sample{HeapObjects: uint64(i)}
		c.next = (c.next + 1) % HistoryLimit
		c.count = min(c.count+1, HistoryLimit)
	}
	h := c.localHistory()
	require.Len(t, h, HistoryLimit)
	require.EqualValues(t, 5, h[0].HeapObjects)
	c.collect()
	v := c.View(context.Background(), "")
	require.False(t, v.RedisAvailable)
	require.Len(t, v.History, HistoryLimit)
	require.Equal(t, processID, v.CurrentInstanceID)
	require.False(t, v.Instances[0].Stale)
	c.enabled = func() bool { return false }
	c.collect()
	require.Len(t, c.localHistory(), HistoryLimit)
}
func TestProfileGateTTLAndSingleDownload(t *testing.T) {
	var gate ProfileGate
	_, err := gate.Acquire(processID)
	require.ErrorIs(t, err, ErrProfileDisabled)
	state := gate.Enable()
	require.True(t, state.Enabled)
	require.WithinDuration(t, time.Now().Add(ProfileTTL), *state.ExpiresAt, time.Second)
	_, err = gate.Acquire(uuid.NewString())
	require.ErrorIs(t, err, ErrInstanceMismatch)
	release, err := gate.Acquire(processID)
	require.NoError(t, err)
	require.True(t, gate.State().InProgress)
	_, err = gate.Acquire(processID)
	require.ErrorIs(t, err, ErrProfileBusy)
	release()
	gate.mu.Lock()
	gate.expires = time.Now().Add(-time.Second)
	gate.mu.Unlock()
	_, err = gate.Acquire(processID)
	require.ErrorIs(t, err, ErrProfileDisabled)
	gate.Enable()
	gate.Disable()
	require.False(t, gate.State().Enabled)
	var out bytes.Buffer
	require.NoError(t, WriteHeap(&out))
	require.Positive(t, out.Len())
}
func TestRedisHistoryBoundTTLAndCrossInstance(t *testing.T) {
	m := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer client.Close()
	store := NewRedisStore(client)
	ctx := context.Background()
	id := uuid.NewString()
	now := time.Now().UTC()
	for i := 0; i < HistoryLimit+3; i++ {
		require.NoError(t, store.Save(ctx, Instance{InstanceID: id, StartedAt: now, LastSeenAt: now}, Sample{At: now, HeapObjects: uint64(i)}))
	}
	instances, history, err := store.Read(ctx, id)
	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Len(t, history, HistoryLimit)
	require.EqualValues(t, 3, history[0].HeapObjects)
	c := NewCollector(store, nil, nil)
	v := c.View(ctx, id)
	require.True(t, v.RedisAvailable)
	require.Len(t, v.History, HistoryLimit)
	require.Len(t, v.Instances, 2)
	m.FastForward(RedisTTL + time.Second)
	instances, history, err = store.Read(ctx, id)
	require.NoError(t, err)
	require.Empty(t, instances)
	require.Empty(t, history)
}
func TestRedisInstanceBoundAndStale(t *testing.T) {
	m := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer client.Close()
	store := NewRedisStore(client)
	ctx := context.Background()
	first := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, store.Save(ctx, Instance{InstanceID: first, LastSeenAt: now.Add(-time.Minute)}, Sample{At: now.Add(-time.Minute)}))
	c := NewCollector(store, nil, nil)
	view := c.View(ctx, first)
	require.True(t, view.Instances[0].Stale)
	for i := 0; i < MaxInstances+2; i++ {
		require.NoError(t, store.Save(ctx, Instance{InstanceID: uuid.NewString(), LastSeenAt: now}, Sample{At: now}))
	}
	instances, _, err := store.Read(ctx, first)
	require.NoError(t, err)
	require.Len(t, instances, MaxInstances)
	require.True(t, m.Exists(redisPrefix+first+":history"))
	require.LessOrEqual(t, len(m.Keys()), MaxInstances*2+2)
	truncated, err := store.Truncated(ctx)
	require.NoError(t, err)
	require.True(t, truncated)
}

func TestRedisUnavailableKeepsLocalSampling(t *testing.T) {
	m := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: -1, DialTimeout: 20 * time.Millisecond})
	defer client.Close()
	c := NewCollector(NewRedisStore(client), nil, nil)
	c.collect()
	m.Close()
	c.collect()
	view := c.View(context.Background(), "")
	require.False(t, view.RedisAvailable)
	require.Len(t, view.History, 2)
	require.False(t, view.Instances[0].Stale)
}

func BenchmarkNumericBodyGauge(b *testing.B) {
	var c Counters
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		release := c.BeginBody(2 << 20)
		release()
	}
}

func BenchmarkRuntimeMemorySample(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ReadSample(context.Background())
	}
}
