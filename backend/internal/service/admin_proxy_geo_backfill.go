package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	proxyGeoBackfillInterval = time.Minute
	proxyGeoBackfillTimeout  = 15 * time.Second
	proxyGeoBackfillPageSize = 100
)

func (s *adminServiceImpl) scheduleProxyGeo(proxy *Proxy) {
	if proxy == nil || s.egressLocationService == nil {
		return
	}
	s.egressLocationService.Resolve(proxyEgressRoute(proxy))
}

// StopProxyGeoBackfill joins maintenance before the shared resolver and its
// storage dependencies are shut down. Business enabled/disabled state is untouched.
func (s *adminServiceImpl) StopProxyGeoBackfill() {
	if s.proxyGeoStop != nil {
		s.proxyGeoStop()
	}
}

// startProxyGeoBackfill restores persistent observations before serving traffic.
// Missing rows are queued in the resolver's existing bounded worker pool. The
// periodic scan also covers import paths that insert proxies without AdminService.
func (s *adminServiceImpl) startProxyGeoBackfill() func() {
	if s.proxyRepo == nil || s.proxyLatencyCache == nil || s.egressLocationService == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.backfillProxyGeo(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(proxyGeoBackfillInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.backfillProxyGeo(ctx)
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}

func (s *adminServiceImpl) backfillProxyGeo(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, proxyGeoBackfillTimeout)
	defer cancel()
	for page := 1; ctx.Err() == nil; page++ {
		proxies, _, err := s.proxyRepo.ListWithFilters(ctx, pagination.PaginationParams{
			Page: page, PageSize: proxyGeoBackfillPageSize, SortBy: "id", SortOrder: "asc",
		}, "", "", "")
		if err != nil {
			logger.LegacyPrintf("service.admin", "Warning: list proxies for geo backfill failed: %v", err)
			return
		}
		if len(proxies) == 0 {
			return
		}
		ids := make([]int64, len(proxies))
		for i := range proxies {
			ids[i] = proxies[i].ID
		}
		snapshots, err := s.proxyLatencyCache.GetProxyLatencies(ctx, ids)
		if err != nil {
			// A storage outage must not turn every saved proxy into a fresh probe.
			logger.LegacyPrintf("service.admin", "Warning: load proxies for geo backfill failed: %v", err)
			return
		}
		for i := range proxies {
			proxy := &proxies[i]
			info := snapshots[proxy.ID]
			if info != nil && info.RouteKey == proxyGeoRouteKey(proxy) &&
				s.egressLocationService.ObserveProxySnapshot(proxyEgressRoute(proxy), info) {
				continue
			}
			s.scheduleProxyGeo(proxy)
		}
		if len(proxies) < proxyGeoBackfillPageSize {
			return
		}
	}
}
