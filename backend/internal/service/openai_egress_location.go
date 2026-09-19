package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	openAIEgressLocationQueueSize = 256
	openAIEgressLocationWorkers   = 2
	openAIEgressLocationCapacity  = 2048
	openAIEgressLocationFreshTTL  = time.Hour
	openAIEgressLocationMaxAge    = 24 * time.Hour
	openAIEgressLocationRetry     = 5 * time.Minute
	openAIEgressLocationTimeout   = 20 * time.Second
)

// OpenAIEgressRoute is a copy of the route selected for an outbound attempt.
// ProxyURL is transport-only and must never be included in observations or logs.
type OpenAIEgressRoute struct {
	ProxyID  int64
	ProxyURL string
}

// OpenAIEgressLocationSnapshot is a value, so a later probe cannot change a
// request's chosen location. RouteKey contains only a digest and is not exposed.
type OpenAIEgressLocationSnapshot struct {
	RouteKey    string    `json:"-"`
	RouteType   string    `json:"route_type"`
	ProxyID     int64     `json:"proxy_id,omitempty"`
	IPAddress   string    `json:"ip_address,omitempty"`
	Country     string    `json:"country"`
	CountryCode string    `json:"country_code"`
	Region      string    `json:"region"`
	City        string    `json:"city"`
	Timezone    string    `json:"timezone"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	Reason      string    `json:"reason,omitempty"`
	CheckedAt   time.Time `json:"checked_at,omitempty,omitzero"`
}

var defaultOpenAIEgressInstance = uuid.NewString()

func openAIEgressRouteKey(route OpenAIEgressRoute, instance string) string {
	// The full URL includes authentication, so changing any connection credential
	// gives a different cache entry without retaining a secret in the key.
	input := "proxy\x00" + route.ProxyURL
	if route.ProxyURL == "" {
		input = "direct\x00" + instance
	}
	// IDs do not identify a connection by themselves; retain them as an additional
	// namespace so two independently managed proxy records do not share state.
	var id [8]byte
	for i := range id {
		id[i] = byte(uint64(route.ProxyID) >> (8 * i))
	}
	sum := sha256.Sum256(append([]byte(input+"\x00"), id[:]...))
	return hex.EncodeToString(sum[:])
}

func defaultOpenAIEgressLocation(route OpenAIEgressRoute, instance string) OpenAIEgressLocationSnapshot {
	routeType := "proxy"
	if route.ProxyURL == "" {
		routeType = "direct"
		route.ProxyID = 0
	}
	return OpenAIEgressLocationSnapshot{
		RouteKey: openAIEgressRouteKey(route, instance), RouteType: routeType, ProxyID: route.ProxyID,
		Country: "United States", CountryCode: "US", Region: "Washington", City: "Seattle",
		Timezone: "America/Los_Angeles", Status: "fallback", Source: "fallback", Reason: "cache_miss",
	}
}

// DefaultOpenAIEgressLocationSnapshot also supports gateways created without a
// resolver, including tests. Its direct-route namespace is local to this process.
func DefaultOpenAIEgressLocationSnapshot(route OpenAIEgressRoute) OpenAIEgressLocationSnapshot {
	return defaultOpenAIEgressLocation(route, defaultOpenAIEgressInstance)
}

type openAIEgressLocationEntry struct {
	good       OpenAIEgressLocationSnapshot
	goodAt     time.Time
	checkedAt  time.Time
	lastUsed   time.Time
	retryAt    time.Time
	ip         string
	reason     string
	pending    bool
	generation uint64
	persist    *ProxyLatencyInfo
	stored     *openAIEgressStoredObservation
}

// Millisecond attempt timestamps can tie. Retain the committed geographic
// payload so an equal-time new winner is distinguishable from a repeated scan.
type openAIEgressStoredObservation struct {
	routeKey, ip, country, countryCode, region, city, timezone, status, reason string
	attemptedAt                                                                int64
	checkedAt, updatedAt                                                       time.Time
}

func proxySnapshotMarker(info *ProxyLatencyInfo) openAIEgressStoredObservation {
	marker := openAIEgressStoredObservation{
		routeKey: info.RouteKey, ip: info.IPAddress, country: info.Country, countryCode: info.CountryCode,
		region: info.Region, city: info.City, timezone: info.Timezone, status: info.GeoStatus, reason: info.GeoReason,
		attemptedAt: info.GeoResultUnixMs, updatedAt: info.UpdatedAt.Round(0).UTC(),
	}
	if info.GeoCheckedAt != nil {
		marker.checkedAt = info.GeoCheckedAt.Round(0).UTC()
	}
	return marker
}

type openAIEgressLocationTask struct {
	route      OpenAIEgressRoute
	key        string
	generation uint64
}

// OpenAIEgressLocationService is a bounded resolver. Resolve never performs
// storage or network I/O. Managed proxies reuse their durable last good result;
// direct routes retain a process-local, time-limited snapshot.
type OpenAIEgressLocationService struct {
	mu          sync.Mutex
	entries     map[string]*openAIEgressLocationEntry
	queue       chan openAIEgressLocationTask
	prober      ProxyExitInfoProber
	cache       ProxyLatencyCache
	instance    string
	now         func() time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	stopped     bool
	sequence    uint64
	starts      []time.Time
	limit       int
	limitWindow time.Duration
}

func NewOpenAIEgressLocationService(prober ProxyExitInfoProber) *OpenAIEgressLocationService {
	return newOpenAIEgressLocationService(prober, time.Now, 30, time.Minute)
}

// SetProxyLatencyCache connects the same durable snapshots used by proxy
// administration. It must be installed before exposing the service to traffic.
func (s *OpenAIEgressLocationService) SetProxyLatencyCache(cache ProxyLatencyCache) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
}

func newOpenAIEgressLocationService(prober ProxyExitInfoProber, now func() time.Time, limit int, limitWindow time.Duration) *OpenAIEgressLocationService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &OpenAIEgressLocationService{
		entries: make(map[string]*openAIEgressLocationEntry), queue: make(chan openAIEgressLocationTask, openAIEgressLocationQueueSize),
		prober: prober, instance: uuid.NewString(), now: now, ctx: ctx, cancel: cancel,
		limit: limit, limitWindow: limitWindow,
	}
	for range openAIEgressLocationWorkers {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

// Resolve returns a complete location immediately and schedules at most one
// lookup for a route. A configured proxy keeps a successful result until an
// explicit observation replaces it; direct routes expire after 24 hours.
func (s *OpenAIEgressLocationService) Resolve(route OpenAIEgressRoute) OpenAIEgressLocationSnapshot {
	if s == nil {
		return DefaultOpenAIEgressLocationSnapshot(route)
	}
	fallback := defaultOpenAIEgressLocation(route, s.instance)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		fallback.Reason = "service_stopped"
		return fallback
	}
	entry := s.entryLocked(fallback.RouteKey, now)
	if entry == nil {
		fallback.Reason = "cache_full"
		return fallback
	}
	entry.lastUsed = now
	managed := route.ProxyID > 0 && route.ProxyURL != ""
	if entry.goodAt.IsZero() || entry.persist != nil || (!managed && now.Sub(entry.goodAt) >= openAIEgressLocationFreshTTL) {
		s.enqueueLocked(route, fallback.RouteKey, entry, now)
	}
	if !entry.goodAt.IsZero() && (managed || now.Sub(entry.goodAt) < openAIEgressLocationMaxAge) {
		result := entry.good
		if !managed && now.Sub(entry.goodAt) >= openAIEgressLocationFreshTTL {
			result.Status = "stale"
			result.Reason = entry.reason
			if result.Reason == "" {
				result.Reason = "refresh_pending"
			}
		}
		return result
	}
	fallback.IPAddress, fallback.CheckedAt = entry.ip, entry.checkedAt
	if entry.reason != "" {
		fallback.Reason = entry.reason
	} else if !entry.goodAt.IsZero() {
		fallback.Reason = "last_good_expired"
	}
	return fallback
}

func (s *OpenAIEgressLocationService) entryLocked(key string, now time.Time) *openAIEgressLocationEntry {
	if entry := s.entries[key]; entry != nil {
		return entry
	}
	if len(s.entries) >= openAIEgressLocationCapacity {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.entries {
			if !entry.pending && (oldestKey == "" || entry.lastUsed.Before(oldest)) {
				oldestKey, oldest = key, entry.lastUsed
			}
		}
		if oldestKey == "" {
			return nil
		}
		delete(s.entries, oldestKey)
	}
	entry := &openAIEgressLocationEntry{lastUsed: now}
	s.entries[key] = entry
	return entry
}

func (s *OpenAIEgressLocationService) enqueueLocked(route OpenAIEgressRoute, key string, entry *openAIEgressLocationEntry, now time.Time) {
	if entry.pending || now.Before(entry.retryAt) {
		return
	}
	if s.prober == nil && s.cache == nil {
		entry.reason = "prober_unavailable"
		entry.retryAt = now.Add(openAIEgressLocationRetry)
		return
	}
	s.sequence++
	select {
	case s.queue <- openAIEgressLocationTask{route: route, key: key, generation: s.sequence}:
		entry.pending, entry.generation = true, s.sequence
	default:
		entry.reason = "queue_full"
		entry.retryAt = now.Add(openAIEgressLocationRetry)
	}
}

// ObserveResult imports a manual probe into exactly the same route cache. Manual
// results supersede older background work, but never initiate a network request.
// Errors are converted to fixed reason codes and are not stored or logged.
func (s *OpenAIEgressLocationService) ObserveResult(route OpenAIEgressRoute, info *ProxyExitInfo, probeErr error) {
	if s == nil {
		return
	}
	observedAt := s.now()
	if info != nil && !info.GeoCheckedAt.IsZero() {
		observedAt = info.GeoCheckedAt
	}
	s.ObserveResultAt(route, info, probeErr, observedAt)
}

// ObserveResultAt orders manual observations by physical attempt start. Geo
// enrichment may happen much later and must not make an older attempt newer.
func (s *OpenAIEgressLocationService) ObserveResultAt(route OpenAIEgressRoute, info *ProxyExitInfo, probeErr error, observedAt time.Time) {
	if s == nil {
		return
	}
	now := s.now()
	key := defaultOpenAIEgressLocation(route, s.instance).RouteKey
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && route.ProxyID > 0 && route.ProxyURL != "" {
		observedAt = observedAt.Truncate(time.Millisecond)
	}
	if s.stopped {
		return
	}
	entry := s.entryLocked(key, now)
	if entry == nil {
		return
	}
	if observedAt.Before(entry.checkedAt) {
		return
	}
	s.sequence++
	entry.generation, entry.pending = s.sequence, false
	entry.persist = nil
	entry.stored = nil
	s.applyResultLocked(route, entry, info, probeErr, now, now)
	entry.checkedAt = observedAt
}

// ObserveProxySnapshot restores a durable observation without scheduling I/O.
// Failures preserve a known same-IP success, but a newly observed IP invalidates
// the previous exit's location. Its boolean result reports a usable last good.
func (s *OpenAIEgressLocationService) ObserveProxySnapshot(route OpenAIEgressRoute, info *ProxyLatencyInfo) bool {
	if s == nil || route.ProxyID <= 0 || route.ProxyURL == "" {
		return false
	}
	now := s.now()
	key := openAIEgressRouteKey(route, s.instance)
	if info == nil || info.RouteKey != key || proxySnapshotObservationTime(info).After(now) ||
		(info.GeoCheckedAt != nil && info.GeoCheckedAt.After(now)) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	entry := s.entryLocked(key, now)
	if entry == nil {
		return false
	}
	observedAt := proxySnapshotObservationTime(info)
	if observedAt.Before(entry.checkedAt) || (observedAt.Equal(entry.checkedAt) && entry.stored != nil && *entry.stored == proxySnapshotMarker(info)) {
		return !entry.goodAt.IsZero()
	}
	s.sequence++
	entry.generation, entry.pending, entry.persist = s.sequence, false, nil
	s.applyStoredSnapshotLocked(route, entry, info, now)
	return !entry.goodAt.IsZero()
}

func proxySnapshotObservationTime(info *ProxyLatencyInfo) time.Time {
	if info.GeoResultUnixMs > 0 {
		return time.UnixMilli(info.GeoResultUnixMs)
	}
	if info.GeoCheckedAt != nil {
		return info.GeoCheckedAt.Truncate(time.Millisecond)
	}
	return info.UpdatedAt.Truncate(time.Millisecond)
}

func storedOpenAIEgressExit(info *ProxyLatencyInfo, key string, now time.Time) *ProxyExitInfo {
	if info == nil || info.RouteKey != key || !usableProxyGeo(info, now) {
		return nil
	}
	exit := &ProxyExitInfo{IP: info.IPAddress, Country: info.Country, CountryCode: info.CountryCode,
		Region: info.Region, City: info.City, Timezone: info.Timezone, GeoStatus: "success", GeoCheckedAt: *info.GeoCheckedAt}
	if _, err := netip.ParseAddr(strings.TrimSpace(exit.IP)); err != nil || !validOpenAIEgressGeo(exit) || proxySnapshotObservationTime(info).After(now) {
		return nil
	}
	return exit
}

func (s *OpenAIEgressLocationService) applyStoredSnapshotLocked(route OpenAIEgressRoute, entry *openAIEgressLocationEntry, info *ProxyLatencyInfo, now time.Time) {
	marker := proxySnapshotMarker(info)
	entry.stored = &marker
	exit := storedOpenAIEgressExit(info, openAIEgressRouteKey(route, s.instance), now)
	if exit == nil {
		exit = &ProxyExitInfo{IP: info.IPAddress, GeoStatus: "failed"}
		if info.GeoCheckedAt != nil && !info.GeoCheckedAt.After(now) {
			exit.GeoCheckedAt = *info.GeoCheckedAt
		}
	}
	observedAt := proxySnapshotObservationTime(info)
	s.applyResultLocked(route, entry, exit, nil, observedAt, now)
	entry.checkedAt = observedAt
	if entry.goodAt.IsZero() {
		// Re-reading an unchanged failed snapshot must not extend its cooldown.
		retryFrom := observedAt
		if !info.UpdatedAt.IsZero() && !info.UpdatedAt.After(now) {
			retryFrom = info.UpdatedAt
		}
		entry.retryAt = retryFrom.Add(openAIEgressLocationRetry)
	}
}

func (s *OpenAIEgressLocationService) applyResultLocked(route OpenAIEgressRoute, entry *openAIEgressLocationEntry, info *ProxyExitInfo, probeErr error, attemptAt, now time.Time) {
	checkedAt := attemptAt
	if info != nil && !info.GeoCheckedAt.IsZero() && !info.GeoCheckedAt.After(now) {
		checkedAt = info.GeoCheckedAt
	}
	entry.checkedAt, entry.lastUsed = checkedAt, now
	entry.retryAt = now.Add(openAIEgressLocationRetry)
	entry.reason = "probe_failed"
	if probeErr != nil || info == nil {
		return
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(info.IP))
	if err != nil {
		entry.reason = "invalid_exit_ip"
		return
	}
	ipString := ip.Unmap().String()
	changedIP := entry.ip != "" && entry.ip != ipString
	entry.ip = ipString
	if changedIP {
		// A newly observed exit must never be paired with the prior IP's location.
		entry.good, entry.goodAt = OpenAIEgressLocationSnapshot{}, time.Time{}
	}
	entry.reason = "geo_unavailable"
	if changedIP {
		entry.reason = "ip_changed_geo_unavailable"
	}
	if info.GeoStatus != "success" {
		return
	}
	if !validOpenAIEgressGeo(info) {
		entry.reason = "geo_invalid"
		return
	}
	result := defaultOpenAIEgressLocation(route, s.instance)
	result.IPAddress = ipString
	result.Country = strings.TrimSpace(info.Country)
	result.CountryCode = strings.ToUpper(strings.TrimSpace(info.CountryCode))
	result.Region, result.City, result.Timezone = strings.TrimSpace(info.Region), strings.TrimSpace(info.City), strings.TrimSpace(info.Timezone)
	result.Status, result.Source, result.Reason = "fresh", result.RouteType+"_exit", ""
	result.CheckedAt = checkedAt
	entry.good, entry.goodAt, entry.checkedAt = result, checkedAt, checkedAt
	entry.retryAt, entry.reason = time.Time{}, ""
}

func validOpenAIEgressGeo(info *ProxyExitInfo) bool {
	if info == nil {
		return false
	}
	for _, value := range []string{info.Country, info.Region, info.City, info.Timezone} {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
			return false
		}
	}
	code := strings.ToUpper(strings.TrimSpace(info.CountryCode))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return false
	}
	zone := strings.TrimSpace(info.Timezone)
	if zone == "Local" {
		return false
	}
	_, err := time.LoadLocation(zone)
	return err == nil
}

func (s *OpenAIEgressLocationService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case task := <-s.queue:
			s.runTask(task)
		}
	}
}

func (s *OpenAIEgressLocationService) taskEntryLocked(task openAIEgressLocationTask) *openAIEgressLocationEntry {
	entry := s.entries[task.key]
	if s.stopped || entry == nil || !entry.pending || entry.generation != task.generation {
		return nil
	}
	return entry
}

func (s *OpenAIEgressLocationService) finishTask(task openAIEgressLocationTask, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.taskEntryLocked(task); entry != nil {
		entry.pending = false
		entry.reason, entry.retryAt = reason, s.now().Add(openAIEgressLocationRetry)
	}
}

// restoreProxySnapshot returns true when durable data resolves this attempt,
// including its persisted failure cooldown. A storage outage must not turn a
// restart into a fresh network probe for every already configured proxy.
func (s *OpenAIEgressLocationService) restoreProxySnapshot(ctx context.Context, task openAIEgressLocationTask, cache ProxyLatencyCache) bool {
	values, err := cache.GetProxyLatencies(ctx, []int64{task.route.ProxyID})
	if err != nil {
		s.finishTask(task, "snapshot_store_unavailable")
		return true
	}
	info := values[task.route.ProxyID]
	if info == nil || info.RouteKey != task.key {
		return false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.taskEntryLocked(task)
	if entry == nil {
		return true
	}
	observedAt := proxySnapshotObservationTime(info)
	if observedAt.After(now) {
		return false
	}
	if observedAt.After(entry.checkedAt) || entry.checkedAt.IsZero() ||
		(observedAt.Equal(entry.checkedAt) && (entry.stored == nil || *entry.stored != proxySnapshotMarker(info))) {
		s.applyStoredSnapshotLocked(task.route, entry, info, now)
	}
	if !entry.goodAt.IsZero() {
		entry.pending = false
		return true
	}
	if now.Before(entry.retryAt) {
		entry.pending = false
		entry.reason = "probe_retry_pending"
		return true
	}
	return false
}

func (s *OpenAIEgressLocationService) runTask(task openAIEgressLocationTask) {
	s.mu.Lock()
	entry := s.taskEntryLocked(task)
	if entry == nil {
		s.mu.Unlock()
		return
	}
	cache, pendingWrite := s.cache, entry.persist
	s.mu.Unlock()
	managed := task.route.ProxyID > 0 && task.route.ProxyURL != "" && cache != nil
	ctx, cancel := context.WithTimeout(s.ctx, openAIEgressLocationTimeout)
	defer cancel()
	if managed && pendingWrite != nil {
		s.persistTaskSnapshot(ctx, task, cache, pendingWrite)
		return
	}
	if managed && s.restoreProxySnapshot(ctx, task, cache) {
		return
	}
	if s.prober == nil {
		s.finishTask(task, "prober_unavailable")
		return
	}
	if !s.waitForBudget() {
		return
	}
	// Queue/budget waiting does not consume the actual probe timeout or lock TTL.
	cancel()
	ctx, cancel = context.WithTimeout(s.ctx, openAIEgressLocationTimeout)
	defer cancel()
	if managed {
		if leases, ok := cache.(ProxyProbeLeaseCache); ok {
			release, acquired, err := leases.AcquireProxyProbe(ctx, task.route.ProxyID, task.key, 2*openAIEgressLocationTimeout)
			if err != nil {
				s.finishTask(task, "snapshot_store_unavailable")
				return
			}
			if !acquired {
				s.finishTask(task, "probe_in_progress")
				return
			}
			defer release()
			// A different instance may have finished after our initial lookup.
			if s.restoreProxySnapshot(ctx, task, cache) {
				return
			}
		}
	}
	s.mu.Lock()
	current := s.taskEntryLocked(task) != nil
	s.mu.Unlock()
	if !current {
		return
	}
	attemptAt := s.now()
	info, latency, probeErr := s.prober.ProbeProxy(ctx, task.route.ProxyURL)
	var snapshot *ProxyLatencyInfo
	if managed {
		snapshot = &ProxyLatencyInfo{Success: probeErr == nil, RouteKey: task.key, GeoResultUnixMs: attemptAt.UnixMilli(), UpdatedAt: s.now()}
		if probeErr == nil {
			snapshot.LatencyMs, snapshot.Message = &latency, "Proxy is accessible"
			copyProxyExitGeo(snapshot, info)
		} else {
			snapshot.Message, snapshot.GeoStatus, snapshot.GeoReason = "Proxy probe failed", "failed", "probe_failed"
		}
	}
	s.mu.Lock()
	entry = s.taskEntryLocked(task)
	if entry == nil {
		s.mu.Unlock()
		return
	}
	if snapshot == nil {
		s.applyResultLocked(task.route, entry, info, probeErr, attemptAt, s.now())
		entry.pending = false
		s.mu.Unlock()
		return
	}
	entry.persist = snapshot
	s.mu.Unlock()
	s.persistTaskSnapshot(ctx, task, cache, snapshot)
}

func (s *OpenAIEgressLocationService) persistTaskSnapshot(ctx context.Context, task openAIEgressLocationTask, cache ProxyLatencyCache, candidate *ProxyLatencyInfo) {
	if err := cache.SetProxyLatency(ctx, task.route.ProxyID, candidate); err != nil {
		s.finishTask(task, "snapshot_store_unavailable")
		return
	}
	// A successful write call may have rejected our candidate by route or attempt
	// CAS. Only the committed winner can enter the permanent in-memory snapshot.
	values, err := cache.GetProxyLatencies(ctx, []int64{task.route.ProxyID})
	if err != nil {
		s.finishTask(task, "snapshot_store_unavailable")
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.taskEntryLocked(task)
	if entry == nil {
		return
	}
	entry.persist, entry.pending = nil, false
	info := values[task.route.ProxyID]
	if info == nil || info.RouteKey != task.key {
		entry.reason, entry.retryAt = "snapshot_unavailable", now.Add(openAIEgressLocationRetry)
		return
	}
	observedAt := proxySnapshotObservationTime(info)
	if observedAt.Before(entry.checkedAt) || observedAt.After(now) {
		return
	}
	s.applyStoredSnapshotLocked(task.route, entry, info, now)
}

// The singleton resolver's workers share one rolling-minute budget. Waiting only
// occupies a background worker; request threads never wait on this limiter.
func (s *OpenAIEgressLocationService) waitForBudget() bool {
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return false
		}
		now := s.now()
		first := 0
		for first < len(s.starts) && !s.starts[first].Add(s.limitWindow).After(now) {
			first++
		}
		s.starts = s.starts[first:]
		if len(s.starts) < s.limit {
			s.starts = append(s.starts, now)
			s.mu.Unlock()
			return true
		}
		delay := s.starts[0].Add(s.limitWindow).Sub(now)
		s.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// Stop cancels probes and budget waits, discards queued work, and joins workers.
func (s *OpenAIEgressLocationService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	clear(s.entries)
	for len(s.queue) > 0 {
		<-s.queue
	}
	s.mu.Unlock()
}
