//go:build unit

package repository

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var schedulerReadBenchmarkSink any

// BenchmarkSchedulerCacheReadSharing exercises the same 2,000 accounts shared by
// 100 groups. Redis fixtures are prepared before measuring retained decoded heap.
// The optional candidate API is detected so this fixture also runs unchanged on
// the pre-candidate baseline. Reflection overhead is present on both revisions.
func BenchmarkSchedulerCacheReadSharing(b *testing.B) {
	for _, groups := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("accounts_2000_groups_%d", groups), func(b *testing.B) {
			mr, err := miniredis.Run()
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(mr.Close)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			b.Cleanup(func() { _ = rdb.Close() })
			cache := newSchedulerCacheWithChunkSizes(rdb, 128, 256).(*schedulerCache)
			fixedNow := time.Now()
			cache.now = func() time.Time { return fixedNow }
			ctx := context.Background()
			accounts := schedulerCacheBenchmarkAccounts(2000)
			for i := range accounts {
				accounts[i].Status = service.StatusActive
				accounts[i].Schedulable = true
				accounts[i].GroupIDs = make([]int64, groups)
				accounts[i].AccountGroups = make([]service.AccountGroup, groups)
				for g := 0; g < groups; g++ {
					accounts[i].GroupIDs[g] = int64(g + 1)
					accounts[i].AccountGroups[g] = service.AccountGroup{AccountID: accounts[i].ID, GroupID: int64(g + 1), Priority: g % 3}
				}
			}
			buckets := make([]service.SchedulerBucket, groups)
			var ids []int64
			for g := range buckets {
				bucket := service.SchedulerBucket{GroupID: int64(g + 1), Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
				buckets[g] = bucket
				token, err := cache.CaptureBucketWriteToken(ctx, bucket)
				if err != nil {
					b.Fatal(err)
				}
				if g == 0 {
					ids, err = cache.SetSnapshotAndReturnAccountIDs(ctx, bucket, token, accounts)
				} else {
					err = cache.SetSnapshotByAccountIDs(ctx, bucket, token, ids)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			accounts = nil
			ids = nil
			method := reflect.ValueOf(cache).MethodByName("GetCandidateSnapshot")
			if !method.IsValid() {
				method = reflect.ValueOf(cache).MethodByName("GetSnapshot")
			}
			read := func(bucket service.SchedulerBucket) any {
				result := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(bucket)})
				if !result[2].IsNil() {
					b.Fatal(result[2].Interface())
				}
				if !result[1].Bool() {
					b.Fatal("unexpected snapshot miss")
				}
				return result[0].Interface()
			}
			schedulerReadBenchmarkSink = nil
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			for _, bucket := range buckets {
				schedulerReadBenchmarkSink = read(bucket)
			}
			schedulerReadBenchmarkSink = nil
			runtime.GC()
			runtime.ReadMemStats(&after)
			retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
			var latencies [1024]int64
			samples := 0
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				started := time.Now()
				schedulerReadBenchmarkSink = read(buckets[i%groups])
				if samples < len(latencies) {
					latencies[samples] = time.Since(started).Nanoseconds()
					samples++
				}
			}
			b.StopTimer()
			slices.Sort(latencies[:samples])
			if samples > 0 {
				b.ReportMetric(float64(latencies[(samples*95+99)/100-1]), "p95-ns")
			}
			b.ReportMetric(float64(retained), "decoded-retained-B")
			runtime.KeepAlive(cache)
		})
	}
}
