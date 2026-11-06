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

var schedulerSharingModesSink any

// BenchmarkSchedulerCacheSharingModes fixes the number of unique accounts at
// 2,000 and buckets at 100. Shared accounts occur in every bucket; remaining
// accounts are distributed disjointly. Thus per-bucket sizes are 20, 1,010 and
// 2,000 at 0%, 50% and 100%. Each measured operation reads all 100 buckets.
// Cold means an empty process-local cache, with Redis already populated.
// The same fixture compiles on the old GetSnapshot-only implementation.
func BenchmarkSchedulerCacheSharingModes(b *testing.B) {
	for _, sharedPercent := range []int{0, 50, 100} {
		for _, mode := range []string{"warm", "cold"} {
			b.Run(fmt.Sprintf("accounts_2000_groups_100_shared_%d_%s", sharedPercent, mode), func(b *testing.B) {
				const accountCount, groupCount = 2000, 100
				sharedCount := accountCount * sharedPercent / 100
				membersPerGroup := sharedCount + (accountCount-sharedCount)/groupCount
				mr, err := miniredis.Run()
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(mr.Close)
				rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
				b.Cleanup(func() { _ = rdb.Close() })
				ctx := context.Background()
				fixedNow := time.Now()
				newCache := func() *schedulerCache {
					cache := newSchedulerCacheWithChunkSizes(rdb, 128, 256).(*schedulerCache)
					cache.now = func() time.Time { return fixedNow }
					return cache
				}
				accounts := schedulerCacheBenchmarkAccounts(accountCount)
				members := make([][]int64, groupCount)
				for i := range accounts {
					account := &accounts[i]
					account.Status, account.Schedulable = service.StatusActive, true
					account.GroupIDs, account.AccountGroups = nil, nil
					for group := 0; group < groupCount; group++ {
						if i >= sharedCount && (i-sharedCount)%groupCount != group {
							continue
						}
						groupID := int64(group + 1)
						members[group] = append(members[group], account.ID)
						account.GroupIDs = append(account.GroupIDs, groupID)
						account.AccountGroups = append(account.AccountGroups, service.AccountGroup{AccountID: account.ID, GroupID: groupID, Priority: group % 3})
					}
				}
				writer := newCache()
				if _, err := writer.writeAccountIDs(ctx, accounts); err != nil {
					b.Fatal(err)
				}
				buckets := make([]service.SchedulerBucket, groupCount)
				for group := range buckets {
					bucket := service.SchedulerBucket{GroupID: int64(group + 1), Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
					buckets[group] = bucket
					token, err := writer.CaptureBucketWriteToken(ctx, bucket)
					if err != nil {
						b.Fatal(err)
					}
					if err := writer.SetSnapshotByAccountIDs(ctx, bucket, token, members[group]); err != nil {
						b.Fatal(err)
					}
				}
				accounts, members, writer = nil, nil, nil
				readAll := func(cache *schedulerCache) {
					method := reflect.ValueOf(cache).MethodByName("GetCandidateSnapshot")
					if !method.IsValid() {
						method = reflect.ValueOf(cache).MethodByName("GetSnapshot")
					}
					for _, bucket := range buckets {
						result := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(bucket)})
						if !result[2].IsNil() {
							b.Fatal(result[2].Interface())
						}
						if !result[1].Bool() {
							b.Fatal("unexpected snapshot miss")
						}
						if result[0].Len() != membersPerGroup {
							b.Fatalf("membership changed: got %d want %d", result[0].Len(), membersPerGroup)
						}
						schedulerSharingModesSink = result[0].Interface()
					}
				}
				var warmCache *schedulerCache
				if mode == "warm" {
					warmCache = newCache()
					readAll(warmCache)
				}
				schedulerSharingModesSink = nil
				runtime.GC()
				var latencies [1024]int64
				samples := 0
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					cache := warmCache
					if mode == "cold" {
						cache = newCache()
					}
					started := time.Now()
					readAll(cache)
					if samples < len(latencies) {
						latencies[samples] = time.Since(started).Nanoseconds()
						samples++
					}
					if mode == "cold" {
						b.StopTimer()
						schedulerSharingModesSink, cache = nil, nil
						runtime.GC()
						b.StartTimer()
					}
				}
				b.StopTimer()
				slices.Sort(latencies[:samples])
				if samples > 0 {
					b.ReportMetric(float64(latencies[(samples*95+99)/100-1]), "p95-sweep-ns")
				}
				b.ReportMetric(groupCount, "bucket-reads/op")
				b.ReportMetric(float64(groupCount*membersPerGroup), "account-views/op")
				runtime.KeepAlive(warmCache)
			})
		}
	}
}
