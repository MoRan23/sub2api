//go:build unit

package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// The serial branch matches e6de1c8eb's mgetChunked. No simulated latency is
// injected. Set TEST_REDIS_URL to compare against the isolated Redis instance.
func BenchmarkSchedulerMGetComparison(b *testing.B) {
	var opts *redis.Options
	if raw := os.Getenv("TEST_REDIS_URL"); raw != "" {
		var err error
		opts, err = redis.ParseURL(raw)
		if err != nil {
			b.Fatal(err)
		}
	} else {
		mr, err := miniredis.Run()
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(mr.Close)
		opts = &redis.Options{Addr: mr.Addr()}
	}
	rdb := redis.NewClient(opts)
	b.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	prefix := fmt.Sprintf("scheduler-perf:%d:", time.Now().UnixNano())
	keys := make([]string, 2000)
	pipe := rdb.Pipeline()
	for i := range keys {
		keys[i] = prefix + fmt.Sprint(i)
		pipe.Set(ctx, keys[i], fmt.Sprint(i), time.Minute*10)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = rdb.Del(ctx, keys...).Err() })
	cache := newSchedulerCacheWithChunkSizes(rdb, 128, 256).(*schedulerCache)
	b.Run("baseline_serial_16_commands", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out := make([]any, 0, len(keys))
			for start := 0; start < len(keys); start += 128 {
				end := min(start+128, len(keys))
				v, err := rdb.MGet(ctx, keys[start:end]...).Result()
				if err != nil {
					b.Fatal(err)
				}
				out = append(out, v...)
			}
			schedulerReadBenchmarkSink = out
		}
	})
	b.Run("pipeline_2_waves", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v, err := cache.mgetChunked(ctx, keys)
			if err != nil {
				b.Fatal(err)
			}
			schedulerReadBenchmarkSink = v
		}
	})
}
