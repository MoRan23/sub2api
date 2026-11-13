package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestCodex0169PoolConnectObserverOnlyOnPhysicalDial(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	var observed atomic.Int32
	req := openAIWSAcquireRequest{
		Account:         &Account{ID: 169, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		WSURL:           "wss://example.com/v1/responses",
		ConnectObserver: func() *CodexTelemetryConnectObserver { observed.Add(1); return nil },
	}
	first, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	first.Release()
	second, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	second.Release()
	require.Equal(t, int32(1), observed.Load(), "pool reuse must not create a connection measurement")
	req.ForceNewConn = true
	third, err := pool.Acquire(context.Background(), req)
	require.NoError(t, err)
	third.Release()
	require.Equal(t, int32(2), observed.Load())
	require.Equal(t, 2, dialer.DialCount())
}

func TestCodex0169PoolPrewarmObservesEachPhysicalDial(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCountingDialer{}
	pool.setClientDialerForTest(dialer)
	var observed atomic.Int32
	ap := pool.getOrCreateAccountPool(169)
	ap.mu.Lock()
	ap.lastAcquire = &openAIWSAcquireRequest{
		Account:         &Account{ID: 169, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		WSURL:           "wss://example.com/v1/responses",
		ConnectObserver: func() *CodexTelemetryConnectObserver { observed.Add(1); return nil },
	}
	ap.mu.Unlock()
	pool.ensureTargetIdleAsync(169)
	require.Eventually(t, func() bool { return dialer.DialCount() == 2 }, time.Second, time.Millisecond)
	require.Equal(t, int32(2), observed.Load())
}

func TestCodex0169PoolObservesFailedDial(t *testing.T) {
	pool := newOpenAIWSConnPool(&config.Config{})
	t.Cleanup(pool.Close)
	pool.setClientDialerForTest(&openAIWSNilConnDialer{})
	var observed atomic.Int32
	_, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account:         &Account{ID: 169, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		WSURL:           "wss://example.com/v1/responses",
		ConnectObserver: func() *CodexTelemetryConnectObserver { observed.Add(1); return nil },
	})
	require.Error(t, err)
	require.Equal(t, int32(1), observed.Load())
}

func TestCodex0169FailedDialCarriesSanitizedPhysicalHeaders(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ws"+strings.TrimPrefix(target.URL, "http"), http.StatusFound)
	}))
	defer source.Close()
	headers := http.Header{}
	headers.Set(openai.CodexResidencyHeaderName, "us")
	headers.Set("Authorization", "Bearer local-test-secret")
	headers.Set("User-Agent", "codex-tui/0.169.0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, status, _, err := newDefaultOpenAIWSClientDialer().Dial(ctx, "ws"+strings.TrimPrefix(source.URL, "http"), headers, "")
	require.Nil(t, conn)
	require.Equal(t, http.StatusForbidden, status)
	var handshakeErr *openAIWSHandshakeError
	require.ErrorAs(t, err, &handshakeErr)
	require.NotNil(t, handshakeErr.ObservationHeaders)
	require.Empty(t, handshakeErr.ObservationHeaders.Get(openai.CodexResidencyHeaderName), "redirected failed handshake must not report first-hop residency")
	require.Empty(t, handshakeErr.ObservationHeaders.Get("Authorization"))
	require.Equal(t, "codex-tui/0.169.0", handshakeErr.ObservationHeaders.Get("User-Agent"))
}
