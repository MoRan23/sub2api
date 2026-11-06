package repository

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"sort"
	"time"
	"unsafe"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/sync/singleflight"
)

const defaultSchedulerCacheMaxBytes int64 = 64 * 1024 * 1024
const schedulerCacheCleanupInterval = 10 * time.Second

type schedulerAccountFlights = singleflight.Group

type schedulerAccountPoolKey struct {
	id             int64
	digest         [32]byte
	runtimeVersion uint64
}

type schedulerAccountPoolEntry struct {
	account        *service.Account
	lastAccess     time.Time
	estimatedBytes int64
}

type schedulerInternedAccount struct {
	account  *service.Account
	retained bool
}

// GetSnapshot keeps the legacy ownership contract for non-candidate callers.
// Scheduling uses GetCandidateSnapshot and does not allocate this Account slab.
func (c *schedulerCache) GetSnapshot(ctx context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	views, hit, err := c.GetCandidateSnapshot(ctx, bucket)
	if err != nil || !hit {
		return nil, hit, err
	}
	slab := make([]service.Account, len(views))
	accounts := make([]*service.Account, len(views))
	for i, view := range views {
		slab[i] = *view.Ref()
		slab[i].LastUsedAt = view.LastUsedAt
		accounts[i] = &slab[i]
	}
	return accounts, true, nil
}

func (c *schedulerCache) internSchedulerAccount(id int64, value any, version uint64, now time.Time) (*service.Account, schedulerAccountPoolKey, bool, error) {
	raw, err := schedulerCacheOptionalString(value)
	if err != nil {
		return nil, schedulerAccountPoolKey{}, false, err
	}
	// One conversion serves both the content identity and JSON decoder. Keeping
	// raw as a string through decodeCachedAccount would copy the payload twice.
	payload := []byte(raw)
	key := schedulerAccountPoolKey{id: id, digest: sha256.Sum256(payload), runtimeVersion: version}
	if account := c.lookupPooledAccount(key, now); account != nil {
		c.poolHits.Add(1)
		return account, key, true, nil
	}
	c.poolMisses.Add(1)
	// The full fixed-width key is losslessly encoded; singleflight still merges
	// exactly the same account/content/model-version tuple, without fmt/hex work.
	var flightKey [48]byte
	binary.LittleEndian.PutUint64(flightKey[:8], uint64(id))
	copy(flightKey[8:40], key.digest[:])
	binary.LittleEndian.PutUint64(flightKey[40:], version)
	result, err, _ := c.accountFlights.Do(string(flightKey[:]), func() (any, error) {
		if account := c.lookupPooledAccount(key, now); account != nil {
			return schedulerInternedAccount{account, true}, nil
		}
		account := new(service.Account)
		if err := json.Unmarshal(payload, account); err != nil {
			return nil, err
		}
		account.PrepareSharedSnapshot()
		size := schedulerAccountEstimatedBytes(account)
		retained := false
		c.snapshotMu.Lock()
		if size <= c.cacheMaxBytes {
			c.evictSchedulerCacheLocked(now, size)
			c.accountEntries[key] = &schedulerAccountPoolEntry{account: account, lastAccess: now, estimatedBytes: size}
			c.accountBytes += size
			retained = true
		}
		c.snapshotMu.Unlock()
		return schedulerInternedAccount{account, retained}, nil
	})
	if err != nil {
		return nil, key, false, err
	}
	decoded := result.(schedulerInternedAccount)
	return decoded.account, key, decoded.retained, nil
}

func (c *schedulerCache) lookupPooledAccount(key schedulerAccountPoolKey, now time.Time) *service.Account {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	entry := c.accountEntries[key]
	if entry == nil {
		return nil
	}
	if now.Sub(entry.lastAccess) > schedulerSnapshotCacheEvictAge {
		c.removePooledAccountLocked(key)
		return nil
	}
	entry.lastAccess = now
	return entry.account
}

func (c *schedulerCache) resolveSchedulerCandidates(keys []schedulerAccountPoolKey, now time.Time) service.CandidateSet {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	accounts := make(service.CandidateSet, len(keys))
	for i, key := range keys {
		entry := c.accountEntries[key]
		if entry == nil || now.Sub(entry.lastAccess) > schedulerSnapshotCacheEvictAge {
			c.poolMisses.Add(1)
			return nil
		}
		entry.lastAccess = now
		accounts[i] = service.NewCandidateView(entry.account, entry.account.LastUsedAt)
	}
	c.poolHits.Add(uint64(len(keys)))
	return accounts
}

func (c *schedulerCache) removePooledAccountLocked(key schedulerAccountPoolKey) {
	if entry := c.accountEntries[key]; entry != nil {
		c.accountBytes -= entry.estimatedBytes
		delete(c.accountEntries, key)
		c.poolEvictions.Add(1)
	}
}

func (c *schedulerCache) removeBucketLocked(key string) {
	if entry := c.snapshotEntries[key]; entry != nil {
		c.bucketBytes -= entry.estimatedBytes
		delete(c.snapshotEntries, key)
		c.bucketEvictions.Add(1)
	}
}

// A bucket only holds content keys, so eviction really releases its ownership
// of a master. Readers already using the old master finish through Go references.
func (c *schedulerCache) evictSchedulerCacheLocked(now time.Time, incoming int64) {
	// Throttle full idle scans; inserting N cold accounts must not scan the
	// growing pool N times. Budget enforcement below remains immediate.
	if c.lastCleanup.IsZero() || now.Before(c.lastCleanup) || now.Sub(c.lastCleanup) >= schedulerCacheCleanupInterval {
		c.lastCleanup = now
		for key, entry := range c.accountEntries {
			if now.Sub(entry.lastAccess) > schedulerSnapshotCacheEvictAge {
				c.removePooledAccountLocked(key)
			}
		}
		for key, entry := range c.snapshotEntries {
			if now.Sub(entry.lastAccess) > schedulerSnapshotCacheEvictAge {
				c.removeBucketLocked(key)
			}
		}
	}
	if c.accountBytes+c.bucketBytes+incoming <= c.cacheMaxBytes {
		return
	}
	type victim struct {
		at      time.Time
		account schedulerAccountPoolKey
		bucket  string
	}
	victims := make([]victim, 0, len(c.accountEntries)+len(c.snapshotEntries))
	for key, entry := range c.accountEntries {
		victims = append(victims, victim{at: entry.lastAccess, account: key})
	}
	for key, entry := range c.snapshotEntries {
		victims = append(victims, victim{at: entry.lastAccess, bucket: key})
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].at.Before(victims[j].at) })
	// Trim a little headroom to avoid sorting on every subsequent insertion.
	target := c.cacheMaxBytes * 9 / 10
	for _, victim := range victims {
		if c.accountBytes+c.bucketBytes+incoming <= target {
			break
		}
		if victim.bucket != "" {
			c.removeBucketLocked(victim.bucket)
		} else {
			c.removePooledAccountLocked(victim.account)
		}
	}
}

func schedulerBucketEstimatedBytes(key string, entry *schedulerSnapshotEntry) int64 {
	size := int64(unsafe.Sizeof(*entry)) + int64(len(key)+len(entry.active))
	size += int64(len(entry.accountKeys)) * int64(unsafe.Sizeof(schedulerAccountPoolKey{}))
	size += int64(len(entry.lastUsedKeys)+len(entry.revisions)) * 16
	for _, value := range entry.lastUsedKeys {
		size += int64(len(value))
	}
	for _, value := range entry.revisions {
		size += int64(len(value))
	}
	return size
}

func schedulerAccountEstimatedBytes(account *service.Account) int64 {
	size := int64(unsafe.Sizeof(*account)) + int64(unsafe.Sizeof(schedulerAccountPoolKey{})) + int64(unsafe.Sizeof(schedulerAccountPoolEntry{}))
	size += int64(len(account.AccountGroups))*int64(unsafe.Sizeof(service.AccountGroup{})) + int64(len(account.GroupIDs))*8
	size += int64(len(account.Name) + len(account.Platform) + len(account.Type) + len(account.Status) + len(account.TempUnschedulableReason) + len(account.SessionWindowStatus))
	size += schedulerJSONEstimatedBytes(account.Credentials) + schedulerJSONEstimatedBytes(account.Extra)
	for key, value := range account.GetModelMapping() {
		size += int64(64 + len(key) + len(value))
	}
	for key, value := range account.GetCompactModelMapping() {
		size += int64(64 + len(key) + len(value))
	}
	// Time/scalar pointer allocations and map bookkeeping are deliberately
	// conservative; this is an admission estimate, not a process RSS limit.
	return size + 512
}

func schedulerJSONEstimatedBytes(value any) int64 {
	switch typed := value.(type) {
	case map[string]any:
		var total int64 = 48
		for key, item := range typed {
			total += int64(64+len(key)) + schedulerJSONEstimatedBytes(item)
		}
		return total
	case map[string]string:
		var total int64 = 48
		for key, item := range typed {
			total += int64(64 + len(key) + len(item))
		}
		return total
	case []any:
		total := int64(len(typed)) * 16
		for _, item := range typed {
			total += schedulerJSONEstimatedBytes(item)
		}
		return total
	case string:
		return int64(len(typed))
	default:
		return 16
	}
}

func (c *schedulerCache) StartCacheCleanup() {
	c.cleanupStart.Do(func() {
		go func() {
			defer close(c.cleanupDone)
			ticker := time.NewTicker(schedulerCacheCleanupInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					c.snapshotMu.Lock()
					c.evictSchedulerCacheLocked(c.now(), 0)
					c.snapshotMu.Unlock()
				case <-c.cleanupCancel:
					return
				}
			}
		}()
	})
}

func (c *schedulerCache) StopCacheCleanup() {
	c.cleanupStop.Do(func() { close(c.cleanupCancel) })
	// Starting an already-stopped cleaner makes Stop safe before Start as well.
	c.StartCacheCleanup()
	<-c.cleanupDone
}

func (c *schedulerCache) RuntimeCacheStats() []runtimeobs.CacheStats {
	c.snapshotMu.RLock()
	defer c.snapshotMu.RUnlock()
	uniqueAccounts := make(map[int64]struct{}, len(c.accountEntries))
	for key := range c.accountEntries {
		uniqueAccounts[key.id] = struct{}{}
	}
	return []runtimeobs.CacheStats{
		{Name: "scheduler_unique_accounts", Entries: int64(len(uniqueAccounts)), TTLSeconds: 100},
		{Name: "scheduler_accounts", Entries: int64(len(c.accountEntries)), EstimatedBytes: c.accountBytes, Hits: c.poolHits.Load(), Misses: c.poolMisses.Load(), Evictions: c.poolEvictions.Load(), TTLSeconds: 100},
		{Name: "scheduler_buckets", Entries: int64(len(c.snapshotEntries)), EstimatedBytes: c.bucketBytes, Hits: c.bucketHits.Load(), Misses: c.bucketMisses.Load(), Evictions: c.bucketEvictions.Load(), TTLSeconds: 100},
		{Name: "scheduler_cache_total", Entries: int64(len(c.accountEntries) + len(c.snapshotEntries)), EstimatedBytes: c.accountBytes + c.bucketBytes, MaxBytes: c.cacheMaxBytes, TTLSeconds: 100},
	}
}
