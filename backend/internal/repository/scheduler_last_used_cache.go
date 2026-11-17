package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const (
	schedulerLastUsedCacheMaxAge = 200 * time.Millisecond
	schedulerSharedLoadTimeout   = 10 * time.Second
)

// Published sidecars contain only timestamps, never account masters. Their
// maximum retained size is reserved by schedulerBucketEstimatedBytes.
type schedulerLastUsedValues struct {
	fetchedAt  time.Time
	generation uint64
	values     []*time.Time
}

func (v *schedulerLastUsedValues) freshAt(now time.Time, generation uint64) bool {
	return v != nil && v.generation == generation && !now.Before(v.fetchedAt) && now.Sub(v.fetchedAt) < schedulerLastUsedCacheMaxAge
}

func (c *schedulerCache) entryLastUsed(ctx context.Context, entry *schedulerSnapshotEntry) ([]*time.Time, error) {
	generation := c.lastUsedGeneration.Load()
	if current := entry.lastUsed.Load(); current.freshAt(c.now(), generation) {
		return current.values, nil
	}
	loaded, err := entry.lastUsedLoads.Do(ctx, generation, func(loadCtx context.Context) (*schedulerLastUsedValues, error) {
		fetchedAt := c.now()
		if current := entry.lastUsed.Load(); current.freshAt(fetchedAt, generation) {
			return current, nil
		}
		loadCtx, cancel := context.WithTimeout(loadCtx, schedulerSharedLoadTimeout)
		defer cancel()
		raw, err := c.mgetChunked(loadCtx, entry.lastUsedKeys)
		if err != nil {
			return nil, err
		}
		if len(raw) != len(entry.lastUsedKeys) {
			return nil, fmt.Errorf("unexpected scheduler last_used value count: got %d, want %d", len(raw), len(entry.lastUsedKeys))
		}
		values := make([]*time.Time, len(raw))
		var times []time.Time
		for i, value := range raw {
			if value == nil {
				continue
			}
			text, err := schedulerCacheOptionalString(value)
			if err != nil {
				return nil, err
			}
			millis, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid last_used cache value %q: %w", text, err)
			}
			if times == nil {
				times = make([]time.Time, len(raw))
			}
			times[i] = time.UnixMilli(millis).UTC()
			values[i] = &times[i]
		}
		loaded := &schedulerLastUsedValues{fetchedAt: fetchedAt, generation: generation, values: values}
		// A delayed read must not replace a result published after a local write
		// or a newer observation. In-flight readers may finish with their snapshot.
		for c.lastUsedGeneration.Load() == generation && loadCtx.Err() == nil {
			current := entry.lastUsed.Load()
			if current != nil && (current.generation > generation || current.fetchedAt.After(fetchedAt)) {
				break
			}
			if entry.lastUsed.CompareAndSwap(current, loaded) {
				break
			}
		}
		return loaded, nil
	})
	if err != nil {
		return nil, err
	}
	return loaded.values, nil
}
