package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTransportClientsIsolateAccountTargetAndProxy(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer().(*coderOpenAIWSClientDialer)
	proxy := "http://private-user:private-password@127.0.0.1:8080"
	base := newOpenAIWSTransportScope(71, "wss://upstream.test/responses", proxy)
	first, err := dialer.proxyHTTPClient(proxy, base)
	require.NoError(t, err)
	again, err := dialer.proxyHTTPClient(proxy, base)
	require.NoError(t, err)
	require.Same(t, first, again)
	for _, test := range []struct {
		name    string
		account int64
		target  string
		proxy   string
	}{
		{"account", 72, "wss://upstream.test/responses", proxy},
		{"target path", 71, "wss://upstream.test/other", proxy},
		{"target host", 71, "wss://other.test/responses", proxy},
		{"proxy credential", 71, "wss://upstream.test/responses", "http://private-user:changed-password@127.0.0.1:8080"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := dialer.proxyHTTPClient(test.proxy, newOpenAIWSTransportScope(test.account, test.target, test.proxy))
			require.NoError(t, err)
			require.NotSame(t, first.Transport, client.Transport)
		})
	}
	for key := range dialer.proxyClients {
		require.NotContains(t, key, "private-user")
		require.NotContains(t, key, "password")
		require.NotContains(t, key, "127.0.0.1")
		require.Contains(t, key, "transport:standard")
	}
	_, err = dialer.proxyHTTPClient("http://private-user:private-password@[invalid")
	require.EqualError(t, err, "invalid proxy url")
}

func TestOpenAIWSTransportDirectClientsRetainStandardDefaults(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer().(*coderOpenAIWSClientDialer)
	client, err := dialer.proxyHTTPClient("", newOpenAIWSTransportScope(71, "wss://upstream.test/responses", ""))
	require.NoError(t, err)
	other, err := dialer.proxyHTTPClient("", newOpenAIWSTransportScope(72, "wss://upstream.test/responses", ""))
	require.NoError(t, err)
	require.NotSame(t, client.Transport, other.Transport)
	actual := client.Transport.(*http.Transport)
	defaults := http.DefaultTransport.(*http.Transport)
	require.NotSame(t, defaults, actual)
	require.NotNil(t, actual.Proxy, "preserve the environment proxy selector")
	require.Equal(t, defaults.ForceAttemptHTTP2, actual.ForceAttemptHTTP2)
	require.Equal(t, defaults.TLSHandshakeTimeout, actual.TLSHandshakeTimeout)
	require.Equal(t, defaults.IdleConnTimeout, actual.IdleConnTimeout)
	require.Equal(t, defaults.MaxIdleConns, actual.MaxIdleConns)
}

func TestOpenAIWSConnPoolTransportIsolationWithLocalProxies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			typ, body, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, typ, body); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	var proxyOneRequests, proxyTwoRequests atomic.Int64
	newProxy := func(count *atomic.Int64) *httptest.Server {
		proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			proxy.ServeHTTP(w, r)
		}))
	}
	proxyOne, proxyTwo := newProxy(&proxyOneRequests), newProxy(&proxyTwoRequests)
	defer proxyOne.Close()
	defer proxyTwo.Close()

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 3
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 3
	// Test only foreground routing. A lower utilization target starts an
	// asynchronous extra dial after the second concurrent lease.
	cfg.Gateway.OpenAIWS.PoolTargetUtilization = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	account := &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 3}
	request := openAIWSAcquireRequest{
		Account: account, WSURL: "ws" + strings.TrimPrefix(upstream.URL, "http") + "/responses",
		ProxyURL: proxyOne.URL, IdentityDigest: "same-thread",
	}
	first, err := pool.Acquire(ctx, request)
	require.NoError(t, err)
	defer first.Release()

	request.ProxyURL = proxyTwo.URL
	second, err := pool.Acquire(ctx, request)
	require.NoError(t, err)
	defer second.Release()
	require.NotEqual(t, first.ConnID(), second.ConnID())
	require.Equal(t, int64(1), proxyOneRequests.Load())
	require.Equal(t, int64(1), proxyTwoRequests.Load())

	request.WSURL += "/different-target"
	third, err := pool.Acquire(ctx, request)
	require.NoError(t, err)
	defer third.Release()
	require.NotEqual(t, second.ConnID(), third.ConnID())
	require.Equal(t, int64(2), proxyTwoRequests.Load())

	for _, lease := range []*openAIWSConnLease{first, second, third} {
		require.NoError(t, lease.WriteJSONWithContextTimeout(ctx, map[string]string{"type": "response.create"}, time.Second))
		message, err := lease.ReadMessageWithContextTimeout(ctx, time.Second)
		require.NoError(t, err, "new routes must not interrupt existing leases")
		require.JSONEq(t, `{"type":"response.create"}`, string(message))
	}
	second.Release()
	request.WSURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/responses"
	request.PreferredConnID = first.ConnID()
	reused, err := pool.Acquire(ctx, request)
	require.NoError(t, err)
	require.Equal(t, second.ConnID(), reused.ConnID(), "old preferred ID cannot override the changed proxy")
	reused.Release()

	dialer := pool.clientDialer.(*coderOpenAIWSClientDialer)
	dialer.proxyMu.Lock()
	defer dialer.proxyMu.Unlock()
	require.Len(t, dialer.proxyClients, 3)
	for key := range dialer.proxyClients {
		require.Contains(t, key, "account:71|")
	}
}

func TestOpenAIWSConnPoolReplacesIdleIncompatibleTransportAtCapacity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSFakeDialer{})
	request := openAIWSAcquireRequest{
		Account: &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		WSURL:   "wss://upstream.test/responses", ProxyURL: "http://proxy-a.test",
	}
	first, err := pool.Acquire(context.Background(), request)
	require.NoError(t, err)
	first.Release()
	request.ProxyURL = "http://proxy-b.test"
	second, err := pool.Acquire(context.Background(), request)
	require.NoError(t, err)
	defer second.Release()
	require.NotEqual(t, first.ConnID(), second.ConnID())
	old := first.conn.ws.(*openAIWSFakeConn)
	old.mu.Lock()
	defer old.mu.Unlock()
	require.True(t, old.closed)
}

func TestOpenAIWSConnPoolChangedProxyWaitsForInFlightLeaseAtCapacity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.PoolTargetUtilization = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSFakeDialer{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := openAIWSAcquireRequest{
		Account: &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		WSURL:   "wss://upstream.test/responses", ProxyURL: "http://proxy-a.test",
	}
	first, err := pool.Acquire(ctx, request)
	require.NoError(t, err)
	defer first.Release()

	request.ProxyURL = "http://proxy-b.test"
	request.PreferredConnID = first.ConnID()
	waitCtx := &openAIWSIsolationWaitContext{Context: ctx, waiting: make(chan struct{})}
	type acquireResult struct {
		lease *openAIWSConnLease
		err   error
	}
	result := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := pool.Acquire(waitCtx, request)
		result <- acquireResult{lease, acquireErr}
	}()

	// A full pool with no compatible connection evaluates Done only when it
	// reaches the topology wait. Synchronize on that point instead of sleeping.
	select {
	case <-waitCtx.waiting:
	case unexpected := <-result:
		if unexpected.lease != nil {
			unexpected.lease.Release()
		}
		t.Fatalf("changed proxy acquired before the in-flight lease released: %v", unexpected.err)
	case <-ctx.Done():
		t.Fatal("changed proxy did not reach the capacity wait")
	}
	select {
	case unexpected := <-result:
		if unexpected.lease != nil {
			unexpected.lease.Release()
		}
		t.Fatalf("changed proxy did not remain waiting: %v", unexpected.err)
	default:
	}

	require.NoError(t, first.WriteJSONWithContextTimeout(ctx, map[string]string{"type": "response.create"}, time.Second),
		"a configuration change must leave the in-flight connection usable")
	message, err := first.ReadMessageWithContextTimeout(ctx, time.Second)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_fake"}}`, string(message))
	first.Release()

	select {
	case acquired := <-result:
		require.NoError(t, acquired.err)
		require.NotNil(t, acquired.lease)
		defer acquired.lease.Release()
		require.NotEqual(t, first.ConnID(), acquired.lease.ConnID())
		require.True(t, acquired.lease.conn.matchesHandshakeCompatibility(openAIWSAcquireCompatibility(request)),
			"the released old route must be replaced with a connection for the new proxy")
	case <-ctx.Done():
		t.Fatal("releasing the in-flight lease did not wake the changed-proxy acquire")
	}
	old := first.conn.ws.(*openAIWSFakeConn)
	old.mu.Lock()
	defer old.mu.Unlock()
	require.True(t, old.closed)
}

type openAIWSIsolationWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *openAIWSIsolationWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
