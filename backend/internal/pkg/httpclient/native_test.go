package httpclient

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codexnative"
	"github.com/stretchr/testify/require"
)

func TestNativeSharedClientScopeAndWrapperCleanup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "unchanged", r.Header.Get("X-Test"))
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	client, err := GetClient(Options{OpenAINative: true})
	require.NoError(t, err)
	ordinary, err := GetClient(Options{})
	require.NoError(t, err)
	require.NotSame(t, ordinary, client)
	for _, ctx := range []context.Context{context.Background(), codexnative.WithScope(context.Background(), codexnative.Scope{AccountID: 7, AccountUserAgent: "Mac OS"})} {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		require.NoError(t, err)
		r.Header.Set("User-Agent", "sdk/1.0")
		r.Header.Set("X-Test", "unchanged")
		response, err := client.Do(r)
		require.NoError(t, err)
		_, err = io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, "sdk/1.0", r.UserAgent())
	}
	// The outer timing wrapper must expose this method to reach real native pools.
	require.Implements(t, (*interface{ CloseIdleConnections() })(nil), client.Transport)
	client.CloseIdleConnections()
	ordinary.CloseIdleConnections()
}
