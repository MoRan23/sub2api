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
}

type openAIEgressLocationTask struct {
	route      OpenAIEgressRoute
	key        string
	generation uint64
}

// OpenAIEgressLocationService is a process-local, bounded resolver. Resolve never
// probes synchronously and does not depend on the legacy proxy latency cache.
type OpenAIEgressLocationService struct {
	mu          sync.Mutex
	entries     map[string]*openAIEgressLocationEntry
	queue       chan openAIEgressLocationTask
	prober      ProxyExitInfoProber
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
// refresh for a route. An expired success remains usable for at most 24 hours.
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
	if entry.goodAt.IsZero() || now.Sub(entry.goodAt) >= openAIEgressLocationFreshTTL {
		s.enqueueLocked(route, fallback.RouteKey, entry, now)
	}
	if !entry.goodAt.IsZero() && now.Sub(entry.goodAt) < openAIEgressLocationMaxAge {
		result := entry.good
		if now.Sub(entry.goodAt) >= openAIEgressLocationFreshTTL {
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
	if s.prober == nil {
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
	now := s.now()
	key := defaultOpenAIEgressLocation(route, s.instance).RouteKey
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	entry := s.entryLocked(key, now)
	if entry == nil {
		return
	}
	if info != nil && !info.GeoCheckedAt.IsZero() && info.GeoCheckedAt.Before(entry.checkedAt) {
		return
	}
	s.sequence++
	entry.generation, entry.pending = s.sequence, false
	s.applyResultLocked(route, entry, info, probeErr, now, now)
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
			if !s.waitForBudget() {
				return
			}
			s.mu.Lock()
			entry := s.entries[task.key]
			current := !s.stopped && entry != nil && entry.pending && entry.generation == task.generation
			s.mu.Unlock()
			if !current {
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctx, openAIEgressLocationTimeout)
			attemptAt := s.now()
			info, _, err := s.prober.ProbeProxy(ctx, task.route.ProxyURL)
			cancel()
			s.mu.Lock()
			entry = s.entries[task.key]
			if !s.stopped && entry != nil && entry.generation == task.generation {
				entry.pending = false
				s.applyResultLocked(task.route, entry, info, err, attemptAt, s.now())
			}
			s.mu.Unlock()
		}
	}
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
