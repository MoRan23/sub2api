package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const publicCatalogFixture = `{"models":[{"slug":"future-model","shell_type":"shell_command","future":{"enabled":null},"model_messages":{"instructions_template":"keep me"}}]}`

type publicCatalogClientFunc func(*http.Request) (*http.Response, error)

func (f publicCatalogClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

type memoryPublicCatalogStore struct {
	mu       sync.Mutex
	snapshot *PublicCodexCatalogSnapshot
	fail     bool
	saves    int
}

func (s *memoryPublicCatalogStore) Load(context.Context) (*PublicCodexCatalogSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return nil, errors.New("cache unavailable")
	}
	return s.snapshot, nil
}
func (s *memoryPublicCatalogStore) Save(_ context.Context, snapshot *PublicCodexCatalogSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("cache unavailable")
	}
	if s.snapshot == nil || !snapshot.CheckedAt.Before(s.snapshot.CheckedAt) {
		copy := *snapshot
		copy.Body = bytes.Clone(snapshot.Body)
		s.snapshot = &copy
		s.saves++
	}
	return nil
}
func publicCatalogResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestPublicCodexCatalogFetchAndFreshCache(t *testing.T) {
	var calls atomic.Int32
	store := &memoryPublicCatalogStore{}
	svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		require.Equal(t, publicCodexCatalogURL, req.URL.String())
		require.Empty(t, req.Header.Get("Authorization"))
		require.Empty(t, req.Header.Get("Cookie"))
		require.Empty(t, req.Header.Get("Chatgpt-Account-Id"))
		require.Equal(t, "application/json", req.Header.Get("Accept"))
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), publicCodexCatalogTimeout)
		resp := publicCatalogResponse(200, publicCatalogFixture)
		resp.Header.Set("ETag", `"upstream-v1"`)
		return resp, nil
	}), store)
	defer svc.Stop()
	for range 3 {
		got, err := svc.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, publicCatalogFixture, string(got.Body))
		require.NotEmpty(t, got.ETag)
		require.True(t, got.observedAt.IsZero(), "public metadata must not become authorization evidence")
		require.Nil(t, got.upstreamSourceBody)
	}
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, 1, store.saves)
}

func TestPublicCodexCatalogRestartUsesPersistedSnapshot(t *testing.T) {
	now := time.Now()
	store := &memoryPublicCatalogStore{snapshot: &PublicCodexCatalogSnapshot{Body: []byte(publicCatalogFixture), CheckedAt: now.Add(-time.Hour)}}
	svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(*http.Request) (*http.Response, error) {
		t.Error("fresh Redis cache must avoid network")
		return nil, errors.New("unexpected")
	}), store)
	defer svc.Stop()
	got, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, publicCatalogFixture, string(got.Body))
	// Redis/client buffers can be reused without changing the published snapshot.
	store.snapshot.Body[0] = 'x'
	again, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, publicCatalogFixture, string(again.Body))
}

func TestPublicCodexCatalogConditional304(t *testing.T) {
	now := time.Now()
	store := &memoryPublicCatalogStore{snapshot: &PublicCodexCatalogSnapshot{Body: []byte(publicCatalogFixture), CheckedAt: now.Add(-7 * time.Hour), ETag: `"v1"`, LastModified: "Tue, 10 Nov 2026 00:00:00 GMT"}}
	svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, `"v1"`, req.Header.Get("If-None-Match"))
		require.Equal(t, store.snapshot.LastModified, req.Header.Get("If-Modified-Since"))
		return publicCatalogResponse(304, ""), nil
	}), store)
	defer svc.Stop()
	svc.now = func() time.Time { return now }
	got, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, publicCatalogFixture, string(got.Body))
	require.Equal(t, now, store.snapshot.CheckedAt)
	require.Equal(t, `"v1"`, store.snapshot.ETag)
}

func TestPublicCodexCatalogFailureKeepsLastGood(t *testing.T) {
	for _, status := range []int{404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			now := time.Now()
			store := &memoryPublicCatalogStore{snapshot: &PublicCodexCatalogSnapshot{Body: []byte(publicCatalogFixture), CheckedAt: now.Add(-7 * time.Hour)}}
			var calls atomic.Int32
			svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return publicCatalogResponse(status, "private error body"), nil
			}), store)
			defer svc.Stop()
			svc.now = func() time.Time { return now }
			for range 3 {
				got, err := svc.Get(context.Background())
				require.NoError(t, err)
				require.Equal(t, publicCatalogFixture, string(got.Body))
			}
			require.EqualValues(t, 1, calls.Load())
			require.Zero(t, store.saves)
		})
	}
}

func TestPublicCodexCatalogRejectsInvalidPayloads(t *testing.T) {
	cases := map[string]string{
		"duplicate slug": `{"models":[{"slug":"a"},{"slug":"a"}]}`,
		"empty":          `{"models":[]}`, "null": `null`, "no slug": `{"models":[{}]}`,
		"bad slug": `{"models":[{"slug":" a"}]}`, "wrong type": `{"models":[{"slug":2}]}`,
		"trailing": publicCatalogFixture + ` {}`, "too large": strings.Repeat(" ", publicCodexCatalogMaxBytes) + publicCatalogFixture,
		"wrong envelope key":               `{"Models":[{"slug":"a"}]}`,
		"case variant masks invalid array": `{"models":[null],"Models":[{"slug":"ok"}]}`,
		"wrong slug key":                   `{"models":[{"Slug":"a"}]}`,
		"wildcard slug":                    `{"models":[{"slug":"gpt-*"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := &memoryPublicCatalogStore{}
			svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(*http.Request) (*http.Response, error) { return publicCatalogResponse(200, body), nil }), store)
			defer svc.Stop()
			got, err := svc.Get(context.Background())
			require.NoError(t, err)
			require.Equal(t, bundledOfficialCodexCatalogJSON, got.Body)
			require.Nil(t, store.snapshot)
		})
	}
}

func TestPublicCodexCatalogInitialUnavailableUsesBundled(t *testing.T) {
	for _, status := range []int{0, http.StatusNotModified} {
		svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(*http.Request) (*http.Response, error) {
			if status == 0 {
				return nil, errors.New("network unavailable")
			}
			return publicCatalogResponse(status, ""), nil
		}), nil)
		got, err := svc.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, bundledOfficialCodexCatalogJSON, got.Body)
		snapshot, _ := svc.current()
		require.Nil(t, snapshot, "fallback must not be persisted as a successful remote fetch")
		svc.Stop()
	}
}

func TestPublicCodexCatalogSingleflightAndCallerCancellation(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return publicCatalogResponse(200, publicCatalogFixture), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}), nil)
	defer svc.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := svc.Get(ctx); first <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.Get(context.Background())
			require.NoError(t, err)
			if got != nil {
				require.Equal(t, publicCatalogFixture, string(got.Body))
			}
		}()
	}
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestPublicCodexCatalogStaleReturnsImmediatelyAndRefreshes(t *testing.T) {
	now := time.Now()
	started := make(chan struct{})
	release := make(chan struct{})
	svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return publicCatalogResponse(200, strings.Replace(publicCatalogFixture, "future-model", "new-model", 1)), nil
	}), nil)
	defer svc.Stop()
	svc.now = func() time.Time { return now }
	svc.publish(&PublicCodexCatalogSnapshot{Body: []byte(publicCatalogFixture), CheckedAt: now.Add(-7 * time.Hour)})
	got, err := svc.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, publicCatalogFixture, string(got.Body))
	<-started
	close(release)
	require.Eventually(t, func() bool { s, _ := svc.current(); return bytes.Contains(s.Body, []byte("new-model")) }, time.Second, time.Millisecond)
}

func TestPublicCodexCatalogCacheOutageAndInvalidTimestamp(t *testing.T) {
	for _, store := range []*memoryPublicCatalogStore{{fail: true}, {snapshot: &PublicCodexCatalogSnapshot{Body: []byte(publicCatalogFixture), CheckedAt: time.Now().Add(time.Hour)}}} {
		var calls atomic.Int32
		svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return publicCatalogResponse(200, publicCatalogFixture), nil
		}), store)
		got, err := svc.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, publicCatalogFixture, string(got.Body))
		require.EqualValues(t, 1, calls.Load())
		svc.Stop()
	}
}

func TestPublicCodexCatalogLegacyGatewayUsesBundledWithoutNetwork(t *testing.T) {
	svc := &OpenAIGatewayService{}
	got, err := svc.getPublicOfficialCodexModels(context.Background())
	require.NoError(t, err)
	require.Equal(t, bundledOfficialCodexCatalogJSON, got.Body)
	require.NoError(t, validatePublicCodexCatalog(got.Body))
}

func TestPublicCodexCatalogStopCancelsRefresh(t *testing.T) {
	started := make(chan struct{})
	svc := NewPublicCodexCatalogService(publicCatalogClientFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}), nil)
	svc.Start()
	<-started
	done := make(chan struct{})
	go func() { svc.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel fetch")
	}
	svc.Stop()
}
