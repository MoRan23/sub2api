package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var officialStatusTestComponents = func() string {
	type component struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	envelope := struct {
		Components []component `json:"components"`
	}{}
	for id := range officialStatusComponentGroups {
		status := "operational"
		if id == "01JMXBNJXGV1T5GT2M9XA83XNG" {
			status = "degraded_performance"
		}
		envelope.Components = append(envelope.Components, component{ID: id, Status: status})
	}
	encoded, _ := json.Marshal(envelope)
	return string(encoded)
}()

func officialStatusTestEntry(id, at, content string) string {
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(content))
	return fmt.Sprintf(`<entry><id>https://status.openai.com//incidents/%s</id><title>Official &amp; notice</title><updated>%s</updated><content type="html">%s</content></entry>`, id, at, escaped.String())
}

func officialStatusTestFeed(entries string) string {
	return `<feed xmlns="http://www.w3.org/2005/Atom">` + entries + `</feed>`
}

func TestOfficialStatusComponents(t *testing.T) {
	now := time.Now().UTC()
	groups, err := parseOfficialStatusComponents([]byte(officialStatusTestComponents), now)
	require.NoError(t, err)
	require.Equal(t, []string{"apis", "chatgpt", "codex"}, []string{groups[0].ID, groups[1].ID, groups[2].ID})
	require.Equal(t, "operational", groups[0].Status)
	require.Equal(t, "degraded_performance", groups[1].Status)
	require.Equal(t, &now, groups[2].FetchedAt)
	require.False(t, groups[2].Stale)
	for _, raw := range []string{`{}`, `{"components":[]}`, `{"components":`} {
		_, err = parseOfficialStatusComponents([]byte(raw), now)
		require.Error(t, err)
	}
	unknown := strings.Replace(officialStatusTestComponents, `]}`, `,{"id":"new-component","status":"operational"}]}`, 1)
	groups, err = parseOfficialStatusComponents([]byte(unknown), now)
	require.NoError(t, err)
	require.Equal(t, "unknown", groups[0].Status)
	require.Equal(t, "degraded_performance", groups[1].Status)
	require.Equal(t, "unknown", groups[2].Status)
	groups, err = parseOfficialStatusComponents([]byte(`{"components":[{"id":"01JP8CD9JR3HR6Y7G4Q75N4DVW","status":"new-status"}]}`), now)
	require.NoError(t, err)
	for _, group := range groups {
		require.Equal(t, "unknown", group.Status)
	}
	groups, err = parseOfficialStatusComponents([]byte(`{"components":[{"id":"01JP8CD9JR3HR6Y7G4Q75N4DVW","status":"operational"},{"id":"01JP8CD9JR3HR6Y7G4Q75N4DVW","status":"operational"}]}`), now)
	require.NoError(t, err)
	for _, group := range groups {
		require.Equal(t, "unknown", group.Status)
	}
	partial := strings.Replace(officialStatusTestComponents, `"01JP8CD9JR3HR6Y7G4Q75N4DVW"`, `"01JMXBRMFE6N2NNT7DG6XZQ6PW"`, 1)
	groups, err = parseOfficialStatusComponents([]byte(partial), now)
	require.NoError(t, err)
	require.Equal(t, "unknown", groups[0].Status)
	require.Equal(t, "degraded_performance", groups[1].Status)
	require.Equal(t, "operational", groups[2].Status)
}

func TestOfficialStatusFeedClassificationAndSanitization(t *testing.T) {
	content := `<b>Status: Monitoring</b><br>Checking &amp; recovering.<script>secret()</script><b>Affected components</b><ul><li>Login (Degraded)</li><li>Codex API (Operational)</li></ul>`
	old := officialStatusTestEntry("shared", "2026-11-19T12:00:00Z", `<b>Status: Investigating</b>`)
	newer := officialStatusTestEntry("shared", "2026-11-19T13:00:00Z", content)
	excluded := officialStatusTestEntry("ads", "2026-11-19T14:00:00Z", `<b>Status: Resolved</b><ul><li>Ads API (Operational)</li></ul>`)
	unknown := officialStatusTestEntry("unknown", "2026-11-19T11:00:00Z", `<b>Status: Unexpected</b><ul><li>New component (Operational)</li></ul>`)
	items, err := parseOfficialStatusFeed([]byte(officialStatusTestFeed(old + newer + excluded + unknown)))
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "shared", items[0].ID)
	require.Equal(t, "https://status.openai.com/incidents/shared", items[0].URL)
	require.Equal(t, []string{"codex"}, items[0].Groups)
	require.Equal(t, []string{"Login", "Codex API"}, items[0].Components)
	require.Equal(t, "Official & notice", items[0].Title)
	require.Equal(t, "monitoring", items[0].Status)
	require.Equal(t, "Checking & recovering.", items[0].Summary)
	require.Empty(t, items[1].Groups)
	require.Equal(t, "unknown", items[1].Status)
	items, err = parseOfficialStatusFeed([]byte(officialStatusTestFeed(officialStatusTestEntry("login", "2026-11-19T13:00:00Z", `<ul><li>Login (Degraded)</li></ul>`))))
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, []string{"Login"}, items[0].Components)
	require.Empty(t, items[0].Groups)
	for _, raw := range []string{`<rss/>`, `<feed`, officialStatusTestFeed(`<entry><id>https://evil.example/incidents/x</id><updated>2026-11-19T13:00:00Z</updated></entry>`)} {
		_, err = parseOfficialStatusFeed([]byte(raw))
		require.Error(t, err)
	}
	items, err = parseOfficialStatusFeed([]byte(officialStatusTestFeed("")))
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestOfficialStatusFeedBoundedAndOrdered(t *testing.T) {
	var entries strings.Builder
	for i := range 120 {
		entries.WriteString(officialStatusTestEntry(fmt.Sprint(i), time.Unix(int64(i), 0).UTC().Format(time.RFC3339), strings.Repeat("测", 1200)))
	}
	items, err := parseOfficialStatusFeed([]byte(officialStatusTestFeed(entries.String())))
	require.NoError(t, err)
	require.Len(t, items, 100)
	require.Equal(t, "119", items[0].ID)
	require.Len(t, []rune(items[0].Summary), 1000)
	require.Equal(t, "20", items[99].ID)
}

type officialStatusMemoryStore struct {
	mu    sync.Mutex
	value *OfficialStatusSnapshot
	held  bool
}

func (s *officialStatusMemoryStore) Load(context.Context) (*OfficialStatusSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, nil
}
func (s *officialStatusMemoryStore) Save(_ context.Context, value *OfficialStatusSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}
func (s *officialStatusMemoryStore) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held {
		return false, nil
	}
	s.held = true
	return true, nil
}
func (*officialStatusMemoryStore) ReleaseLeaderLock(context.Context, string, string) error {
	panic("refresh lease must expire naturally")
}

func TestOfficialStatusRefreshIndependentFailuresAndLease(t *testing.T) {
	var calls atomic.Int32
	var failFeed, failComponents atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if (r.URL.Path == "/feed" && failFeed.Load()) || (r.URL.Path == "/components" && failComponents.Load()) {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/components" {
			_, _ = fmt.Fprint(w, officialStatusTestComponents)
			return
		}
		_, _ = fmt.Fprint(w, officialStatusTestFeed(officialStatusTestEntry("incident", "2026-11-19T13:00:00Z", `<b>Status: Resolved</b>`)))
	}))
	defer server.Close()
	store := &officialStatusMemoryStore{}
	s := &OfficialStatusService{client: server.Client(), store: store, locks: store, componentsURL: server.URL + "/components", feedURL: server.URL + "/feed"}
	s.refresh(context.Background())
	first := s.Get()
	require.False(t, first.IncidentsStale)
	require.Len(t, first.Incidents, 1)
	require.EqualValues(t, 2, calls.Load())
	peer := &OfficialStatusService{client: server.Client(), store: store, locks: store}
	peer.refresh(context.Background())
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, first, peer.Get())
	failFeed.Store(true)
	store.held = false
	s.refresh(context.Background())
	second := s.Get()
	require.True(t, second.IncidentsStale)
	require.Equal(t, first.IncidentsFetchedAt, second.IncidentsFetchedAt)
	require.False(t, second.Groups[0].Stale)
	failFeed.Store(false)
	failComponents.Store(true)
	store.held = false
	s.refresh(context.Background())
	third := s.Get()
	require.False(t, third.IncidentsStale)
	require.True(t, third.Groups[0].Stale)
	require.Equal(t, second.Groups[0].FetchedAt, third.Groups[0].FetchedAt)
}

func TestOfficialStatusReadSnapshotStalenessAndLimit(t *testing.T) {
	s := &OfficialStatusService{}
	result := s.Get()
	require.True(t, result.IncidentsStale)
	require.Len(t, result.Groups, 3)
	require.NotNil(t, result.Incidents)
	snapshot := emptyOfficialStatus()
	old := time.Now().Add(-6 * time.Minute)
	for i := range snapshot.Groups {
		snapshot.Groups[i].Stale = false
		snapshot.Groups[i].FetchedAt = &old
	}
	snapshot.IncidentsStale, snapshot.IncidentsFetchedAt = false, &old
	snapshot.Incidents = make([]OfficialStatusIncident, 100)
	s.snapshot.Store(snapshot)
	for range 20 {
		go s.Get()
	}
	result = s.Get()
	require.Len(t, result.Incidents, 20)
	require.True(t, result.IncidentsStale)
	require.True(t, result.Groups[0].Stale)
	require.False(t, snapshot.Groups[0].Stale)
}

type officialStatusBlockingClient struct{ started chan struct{} }

func (c officialStatusBlockingClient) Do(r *http.Request) (*http.Response, error) {
	c.started <- struct{}{}
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestOfficialStatusStopCancelsRequests(t *testing.T) {
	store := &officialStatusMemoryStore{}
	client := officialStatusBlockingClient{started: make(chan struct{}, 2)}
	s := NewOfficialStatusService(client, store, store, func(context.Context) bool { return true })
	for range 2 {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("poll did not start")
		}
	}
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel requests")
	}
	require.Nil(t, s.snapshot.Load())
}

func TestOfficialStatusFetchRejectsOversizeAndHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.WriteHeader(404)
			return
		}
		_, _ = fmt.Fprint(w, strings.Repeat("x", officialStatusBodyLimit+1))
	}))
	defer server.Close()
	s := &OfficialStatusService{client: server.Client()}
	for _, path := range []string{"/error", "/large"} {
		_, err := s.fetch(context.Background(), server.URL+path)
		require.Error(t, err)
	}
}

func BenchmarkOfficialStatusGet(b *testing.B) {
	s := &OfficialStatusService{}
	snapshot := emptyOfficialStatus()
	now := time.Now()
	for i := range snapshot.Groups {
		snapshot.Groups[i].FetchedAt = &now
	}
	snapshot.IncidentsFetchedAt = &now
	snapshot.Incidents = make([]OfficialStatusIncident, 100)
	s.snapshot.Store(snapshot)
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Get()
	}
}
