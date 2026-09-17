package codexnative

import (
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

type dispatchTestTransport struct {
	trip   func(*http.Request) (*http.Response, error)
	closed atomic.Int32
}

func (t *dispatchTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t.trip(r) }
func (t *dispatchTestTransport) CloseIdleConnections()                             { t.closed.Add(1) }

func dispatchRequest(t *testing.T, account int64, ua string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(WithScope(context.Background(), Scope{AccountID: account, Purpose: "test"}), http.MethodGet, "https://example.test/", nil)
	require.NoError(t, err)
	r.Header.Set("User-Agent", ua)
	return r
}

func dispatchOK(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func TestNativeDispatcherIsolationAndFinalUA(t *testing.T) {
	var made []Platform
	fallback := &dispatchTestTransport{trip: dispatchOK}
	d := NewDispatcher(fallback, func(p Platform) (http.RoundTripper, error) {
		made = append(made, p)
		return &dispatchTestTransport{trip: func(r *http.Request) (*http.Response, error) {
			require.NotEmpty(t, UserAgent(r.Header))
			return dispatchOK(r)
		}}, nil
	})
	for _, c := range []struct {
		id int64
		ua string
	}{{1, "Codex (Windows 11)"}, {1, "Codex (Windows 11)"}, {1, "Codex (Mac OS 26; arm64)"}, {2, "Codex (Mac OS 26; arm64)"}} {
		r := dispatchRequest(t, c.id, c.ua)
		resp, err := d.RoundTrip(r)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, c.ua, r.Header.Get("User-Agent"))
	}
	aliasRequest := dispatchRequest(t, 1, "")
	delete(aliasRequest.Header, "User-Agent")
	aliasRequest.Header["user-agent"] = []string{"Codex (Windows 11)"}
	aliasResponse, err := d.RoundTrip(aliasRequest)
	require.NoError(t, err)
	require.NoError(t, aliasResponse.Body.Close())
	require.Equal(t, []Platform{Windows, MacOS, MacOS}, made)
	r, _ := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	resp, err := d.RoundTrip(r)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Len(t, made, 3, "unmarked API key requests must use fallback")
}

func TestNativeDispatcherCapacityKeepsActiveStreams(t *testing.T) {
	var created []*dispatchTestTransport
	d := NewDispatcher(nil, func(Platform) (http.RoundTripper, error) {
		tr := &dispatchTestTransport{trip: dispatchOK}
		created = append(created, tr)
		return tr, nil
	})
	d.capacity = 1
	first, err := d.RoundTrip(dispatchRequest(t, 1, "Windows"))
	require.NoError(t, err)
	_, err = d.RoundTrip(dispatchRequest(t, 2, "Linux"))
	require.ErrorContains(t, err, "cache limit")
	require.Zero(t, created[0].closed.Load())
	require.NoError(t, first.Body.Close())
	second, err := d.RoundTrip(dispatchRequest(t, 2, "Linux"))
	require.NoError(t, err)
	require.EqualValues(t, 1, created[0].closed.Load())
	require.NoError(t, second.Body.Close())
	d.CloseIdleConnections()
	require.Empty(t, d.entries)
	require.EqualValues(t, 1, created[1].closed.Load())
}

func TestNativeDispatcherTTLAndFailedFactory(t *testing.T) {
	now := time.Unix(100, 0)
	created := 0
	d := NewDispatcher(nil, func(Platform) (http.RoundTripper, error) {
		created++
		if created == 2 {
			return nil, errors.New("factory failed")
		}
		return &dispatchTestTransport{trip: dispatchOK}, nil
	})
	d.now = func() time.Time { return now }
	resp, err := d.RoundTrip(dispatchRequest(t, 1, "Linux"))
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	for _, e := range d.entries {
		require.Zero(t, e.active, "EOF and close release only once")
	}
	now = now.Add(dispatcherIdleTTL)
	_, err = d.RoundTrip(dispatchRequest(t, 1, "Linux"))
	require.ErrorContains(t, err, "factory failed")
	require.Empty(t, d.entries)
	resp, err = d.RoundTrip(dispatchRequest(t, 1, "Linux"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
}

func TestNativeDispatcherConcurrentSelection(t *testing.T) {
	var made atomic.Int32
	d := NewDispatcher(nil, func(Platform) (http.RoundTripper, error) {
		made.Add(1)
		return &dispatchTestTransport{trip: dispatchOK}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		r := dispatchRequest(t, int64(i%3), []string{"Windows", "Mac OS", "Linux"}[i%3])
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := d.RoundTrip(r)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = io.ReadAll(resp.Body)
			if err != nil {
				t.Error(err)
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	require.EqualValues(t, 3, made.Load())
	d.CloseIdleConnections()
}
