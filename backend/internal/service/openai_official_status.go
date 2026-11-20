package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	officialStatusInterval  = 120 * time.Second
	officialStatusTimeout   = 10 * time.Second
	officialStatusBodyLimit = 1 << 20
	officialStatusLeaseKey  = "openai:official-status:refresh:v2"
)

type OfficialStatusGroup struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Status    string                `json:"status"`
	FetchedAt *time.Time            `json:"fetched_at"`
	Stale     bool                  `json:"stale"`
	History   OfficialStatusHistory `json:"history"`
}

type OfficialStatusIncident struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Status     string    `json:"status"`
	Groups     []string  `json:"groups"`
	Components []string  `json:"components"`
	UpdatedAt  time.Time `json:"updated_at"`
	Summary    string    `json:"summary"`
}

// Snapshots are published once and never mutated by readers.
type OfficialStatusSnapshot struct {
	Version            int                      `json:"version"`
	CheckedAt          time.Time                `json:"checked_at"`
	Groups             []OfficialStatusGroup    `json:"groups"`
	Incidents          []OfficialStatusIncident `json:"incidents"`
	IncidentsFetchedAt *time.Time               `json:"incidents_fetched_at"`
	IncidentsStale     bool                     `json:"incidents_stale"`
}

type OfficialStatusResponse struct {
	SourceURL              string                   `json:"source_url"`
	RefreshIntervalSeconds int                      `json:"refresh_interval_seconds"`
	Groups                 []OfficialStatusGroup    `json:"groups"`
	Incidents              []OfficialStatusIncident `json:"incidents"`
	IncidentsFetchedAt     *time.Time               `json:"incidents_fetched_at"`
	IncidentsStale         bool                     `json:"incidents_stale"`
}

type OfficialStatusStore interface {
	Load(context.Context) (*OfficialStatusSnapshot, error)
	Save(context.Context, *OfficialStatusSnapshot) error
}

type OfficialStatusService struct {
	client        publicCodexHTTPClient
	store         OfficialStatusStore
	locks         LeaderLockCache
	enabled       func(context.Context) bool
	componentsURL string
	feedURL       string
	summaryURL    string
	historyURL    string
	owner         string
	snapshot      atomic.Pointer[OfficialStatusSnapshot]
	cancel        context.CancelFunc
	done          chan struct{}
}

func NewOfficialStatusService(client publicCodexHTTPClient, store OfficialStatusStore, locks LeaderLockCache, enabled func(context.Context) bool) *OfficialStatusService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &OfficialStatusService{client: client, store: store, locks: locks, enabled: enabled,
		componentsURL: "https://status.openai.com/api/v2/components.json", feedURL: "https://status.openai.com/feed.atom",
		summaryURL: "https://app.incident.io/api/status_page_content/status.openai.com",
		historyURL: "https://app.incident.io/api/status_page_content/status.openai.com/component_impacts",
		owner:      uuid.NewString(), cancel: cancel, done: make(chan struct{})}
	go s.run(ctx)
	return s
}

func (s *OfficialStatusService) Stop() {
	s.cancel()
	<-s.done
}

func (s *OfficialStatusService) run(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if s.enabled(ctx) {
			s.refresh(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func emptyOfficialStatus() *OfficialStatusSnapshot {
	return &OfficialStatusSnapshot{Version: 2, Groups: []OfficialStatusGroup{
		{ID: "apis", Name: "APIs", Status: "unknown", Stale: true, History: OfficialStatusHistory{Days: []OfficialStatusDay{}, Stale: true}},
		{ID: "chatgpt", Name: "ChatGPT", Status: "unknown", Stale: true, History: OfficialStatusHistory{Days: []OfficialStatusDay{}, Stale: true}},
		{ID: "codex", Name: "Codex", Status: "unknown", Stale: true, History: OfficialStatusHistory{Days: []OfficialStatusDay{}, Stale: true}},
	}, Incidents: []OfficialStatusIncident{}, IncidentsStale: true}
}

func (s *OfficialStatusService) Get() OfficialStatusResponse {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		snapshot = emptyOfficialStatus()
	}
	groups := append([]OfficialStatusGroup(nil), snapshot.Groups...)
	now := time.Now()
	for i := range groups {
		groups[i].Stale = groups[i].Stale || officialStatusExpired(groups[i].FetchedAt, now)
		groups[i].History.Stale = groups[i].History.Stale || officialStatusExpired(groups[i].History.FetchedAt, now)
	}
	incidents := snapshot.Incidents
	if len(incidents) > 20 {
		incidents = incidents[:20]
	}
	return OfficialStatusResponse{SourceURL: "https://status.openai.com/", RefreshIntervalSeconds: 120,
		Groups: groups, Incidents: incidents, IncidentsFetchedAt: snapshot.IncidentsFetchedAt,
		IncidentsStale: snapshot.IncidentsStale || officialStatusExpired(snapshot.IncidentsFetchedAt, now)}
}

func officialStatusExpired(at *time.Time, now time.Time) bool {
	return at == nil || now.Sub(*at) > 5*time.Minute
}

func (s *OfficialStatusService) refresh(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	previous, err := s.store.Load(ctx)
	if err != nil {
		return
	}
	if previous != nil {
		current := s.snapshot.Load()
		if current == nil || previous.CheckedAt.After(current.CheckedAt) {
			s.snapshot.Store(previous)
		}
	}
	acquired, err := s.locks.TryAcquireLeaderLock(ctx, officialStatusLeaseKey, s.owner, officialStatusInterval)
	if err != nil || !acquired {
		return
	}
	// The lease expires naturally to throttle all instances, including failed polls.
	previous = s.snapshot.Load()
	if previous == nil {
		previous = emptyOfficialStatus()
	}
	next := *previous
	next.Groups = append([]OfficialStatusGroup(nil), previous.Groups...)
	for i := range next.Groups {
		next.Groups[i].Stale = true
		next.Groups[i].History.Stale = true
	}
	next.IncidentsStale = true
	var wg sync.WaitGroup
	var groups []OfficialStatusGroup
	var histories [3]OfficialStatusHistory
	var historyErr error
	now := time.Now().UTC()
	wg.Add(3)
	go func() {
		defer wg.Done()
		body, err := s.fetch(ctx, s.componentsURL)
		if err != nil {
			return
		}
		groups, _ = parseOfficialStatusComponents(body, time.Now().UTC())
	}()
	go func() {
		defer wg.Done()
		body, err := s.fetch(ctx, s.feedURL)
		if err != nil {
			return
		}
		incidents, err := parseOfficialStatusFeed(body)
		if err != nil {
			return
		}
		at := time.Now().UTC()
		next.Incidents, next.IncidentsFetchedAt, next.IncidentsStale = incidents, &at, false
	}()
	go func() {
		defer wg.Done()
		histories, historyErr = s.fetchHistory(ctx, now)
	}()
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	for i := range next.Groups {
		if groups != nil {
			next.Groups[i].Status = groups[i].Status
			next.Groups[i].FetchedAt = groups[i].FetchedAt
			next.Groups[i].Stale = groups[i].Stale
		}
		if historyErr == nil {
			next.Groups[i].History = histories[i]
		}
	}
	next.CheckedAt = time.Now().UTC()
	s.snapshot.Store(&next)
	_ = s.store.Save(ctx, &next)
}

func (s *OfficialStatusService) fetch(parent context.Context, endpoint string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, officialStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official status HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, officialStatusBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > officialStatusBodyLimit {
		return nil, errors.New("official status response too large")
	}
	return body, nil
}

func ValidateOfficialStatusSnapshot(snapshot *OfficialStatusSnapshot) error {
	if snapshot == nil || snapshot.Version != 2 || len(snapshot.Groups) != 3 || snapshot.Incidents == nil || len(snapshot.Incidents) > 100 || snapshot.CheckedAt.IsZero() || snapshot.CheckedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("invalid official status snapshot")
	}
	for i, id := range []string{"apis", "chatgpt", "codex"} {
		group := snapshot.Groups[i]
		if group.ID != id || officialStatusRank(group.Status) < 0 || len(group.Name) > 32 {
			return errors.New("invalid official status group")
		}
		if err := validateOfficialStatusHistory(group.History, snapshot.CheckedAt); err != nil {
			return err
		}
	}
	for _, incident := range snapshot.Incidents {
		if officialStatusIncidentURL(incident.URL) == "" || len(incident.ID) > 256 || len(incident.Title) > 2048 || len(incident.Summary) > 4096 || len(incident.Components) > 64 || len(incident.Groups) > 3 {
			return errors.New("invalid official status incident")
		}
	}
	return nil
}

func parseOfficialStatusComponents(body []byte, now time.Time) ([]OfficialStatusGroup, error) {
	var envelope struct {
		Components []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"components"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Components) == 0 {
		return nil, errors.New("missing official status components")
	}
	groups := emptyOfficialStatus().Groups
	seen := [3]bool{}
	covered := [3]int{}
	expected := [3]int{}
	componentIDs := make(map[string]struct{}, len(officialStatusComponentGroups))
	for _, index := range officialStatusComponentGroups {
		if index >= 0 {
			expected[index]++
		}
	}
	unknown := false
	for _, component := range envelope.Components {
		index, known := officialStatusComponentGroups[component.ID]
		if !known {
			unknown = true
			continue
		}
		if index < 0 {
			continue
		}
		if _, exists := componentIDs[component.ID]; !exists {
			componentIDs[component.ID] = struct{}{}
			covered[index]++
		}
		status := component.Status
		if officialStatusRank(status) < 0 {
			status = "unknown"
		}
		if !seen[index] || officialStatusRank(status) > officialStatusRank(groups[index].Status) {
			groups[index].Status = status
		}
		seen[index] = true
	}
	for i := range groups {
		if (unknown || covered[i] != expected[i]) && groups[i].Status == "operational" {
			groups[i].Status = "unknown"
		}
		groups[i].FetchedAt, groups[i].Stale = &now, false
	}
	return groups, nil
}

func officialStatusRank(status string) int {
	switch status {
	case "operational":
		return 0
	case "under_maintenance":
		return 1
	case "degraded_performance":
		return 2
	case "partial_outage":
		return 3
	case "major_outage":
		return 4
	case "unknown":
		return 5
	}
	return -1
}
