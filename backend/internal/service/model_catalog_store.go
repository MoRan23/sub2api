package service

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/sharedflight"
)

const modelCatalogDefaultTTL = 15 * time.Second

// ModelCatalogStore shares one residency budget across projections, ownership
// indexes and final representations. Values are immutable after publication.
type ModelCatalogStore struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	lru        list.List
	bytes      int64
	maxBytes   int64
	maxEntries int
	revision   uint64
	loads      sharedflight.Group[string, any]
}
type modelCatalogEntry struct {
	key     string
	value   any
	bytes   int64
	expires time.Time
}
type modelCatalogStoreHolder struct {
	once  sync.Once
	store *ModelCatalogStore
}

func (h *modelCatalogStoreHolder) get() *ModelCatalogStore {
	h.once.Do(func() { h.store = NewModelCatalogStore(64<<20, 512) })
	return h.store
}
func NewModelCatalogStore(maxBytes int64, maxEntries int) *ModelCatalogStore {
	return &ModelCatalogStore{entries: make(map[string]*list.Element), maxBytes: maxBytes, maxEntries: maxEntries}
}
func (s *ModelCatalogStore) Revision() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.revision }
func (s *ModelCatalogStore) Get(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[key]
	if e == nil {
		return nil, false
	}
	entry := e.Value.(*modelCatalogEntry)
	if !time.Now().Before(entry.expires) {
		s.remove(e)
		return nil, false
	}
	s.lru.MoveToFront(e)
	return entry.value, true
}
func (s *ModelCatalogStore) remove(e *list.Element) {
	entry := e.Value.(*modelCatalogEntry)
	delete(s.entries, entry.key)
	s.bytes -= entry.bytes
	s.lru.Remove(e)
}
func (s *ModelCatalogStore) Put(key string, value any, size int64, revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	size += int64(len(key)) + 128
	if revision != s.revision || size > s.maxBytes || s.maxEntries <= 0 {
		return
	}
	if e := s.entries[key]; e != nil {
		s.remove(e)
	}
	now := time.Now()
	for e := s.lru.Back(); e != nil; {
		prev := e.Prev()
		if !now.Before(e.Value.(*modelCatalogEntry).expires) {
			s.remove(e)
		}
		e = prev
	}
	for s.bytes+size > s.maxBytes || len(s.entries) >= s.maxEntries {
		s.remove(s.lru.Back())
	}
	s.entries[key] = s.lru.PushFront(&modelCatalogEntry{key: key, value: value, bytes: size, expires: now.Add(modelCatalogDefaultTTL)})
	s.bytes += size
}

// Invalidate fences pending publishers. Authorization is still checked by the
// caller before looking up a representation; this store never grants access.
func (s *ModelCatalogStore) Invalidate() {
	s.mu.Lock()
	s.revision++
	s.entries = make(map[string]*list.Element)
	s.lru.Init()
	s.bytes = 0
	s.mu.Unlock()
}
func (s *ModelCatalogStore) Load(ctx context.Context, key string, load func(context.Context) (any, int64, error)) (any, error) {
	revision := s.Revision()
	if v, ok := s.Get(key); ok {
		return v, nil
	}
	return s.loads.Do(ctx, fmt.Sprintf("%d:%s", revision, key), func(ctx context.Context) (any, error) {
		if v, ok := s.Get(key); ok {
			return v, nil
		}
		v, size, err := load(ctx)
		if err == nil && v != nil && ctx.Err() == nil {
			s.Put(key, v, size, revision)
		}
		return v, err
	})
}
func (s *GatewayService) catalogStore() *ModelCatalogStore {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.catalogStore.get()
	}
	return s.localCatalogStore.get()
}
func (s *OpenAIGatewayService) catalogStore() *ModelCatalogStore {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.catalogStore.get()
	}
	return s.localCatalogStore.get()
}
