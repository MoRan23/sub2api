package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSBackgroundPrewarmRecordsPhysicalHandshake(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, disableBeforeCompletion := range []bool{false, true} {
		name := "enabled"
		if disableBeforeCompletion {
			name = "disabled_before_handshake_completes"
		}
		t.Run(name, func(t *testing.T) {
			SetFingerprintObservationEnabled(false)
			SetFingerprintObservationEnabled(true)
			defer SetFingerprintObservationEnabled(false)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			headersReceived := make(chan http.Header, 2)
			allowHandshake := make(chan struct{})
			serverErrors := make(chan error, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				headersReceived <- r.Header.Clone()
				select {
				case <-allowHandshake:
				case <-ctx.Done():
					return
				}
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErrors <- err
					return
				}
				defer conn.CloseNow()
				<-ctx.Done()
			}))
			defer func() { cancel(); server.Close() }()
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			pool := newOpenAIWSConnPool(cfg)
			defer pool.Close()
			account := &Account{ID: 6021, Name: "frozen-upstream", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			key := &APIKey{ID: 9021, UserID: 7021, Name: "frozen-key", Key: "never-retain-downstream-secret",
				User: &User{ID: 7021, Username: "frozen-user", Email: "frozen@example.test"}}
			c.Set("api_key", key)
			recorder := freezeFingerprintObservationWSHandshake(c, account)
			require.NotNil(t, recorder)
			// The delayed callback must retain only scalar snapshots, never these
			// mutable actor objects or a reused request context.
			key.Name, key.User.Username = "mutated-key", "mutated-user"
			account.Name = "mutated-upstream"
			c.Request.URL.Path = "/changed-after-capture"
			headers := http.Header{}
			headers.Set(openai.CodexResidencyHeaderName, "us")
			headers.Set("Authorization", "Bearer never-retain-upstream-secret")
			headers.Set("User-Agent", "physical-prewarm-agent")
			req := openAIWSAcquireRequest{Account: account, WSURL: "ws" + strings.TrimPrefix(server.URL, "http"),
				Headers: headers, HandshakeObserver: recorder}
			ap := pool.getOrCreateAccountPool(account.ID)
			ap.mu.Lock()
			ap.lastAcquire = &req
			ap.mu.Unlock()
			startedAt := time.Now()
			pool.ensureTargetIdleAsync(account.ID)
			select {
			case actual := <-headersReceived:
				require.Equal(t, "us", actual.Get(openai.CodexResidencyHeaderName))
			case <-ctx.Done():
				t.Fatal("background prewarm did not reach the local upstream")
			}
			require.Empty(t, SnapshotFingerprintObservations(0), "a dial attempt is not yet a successful physical handshake")
			if disableBeforeCompletion {
				SetFingerprintObservationEnabled(false)
			}
			close(allowHandshake)
			require.Eventually(t, func() bool {
				ap.mu.Lock()
				defer ap.mu.Unlock()
				return len(ap.conns) == 1 && !ap.prewarmActive
			}, 3*time.Second, 10*time.Millisecond)
			require.Empty(t, serverErrors)
			rows := SnapshotFingerprintObservations(0)
			if disableBeforeCompletion {
				require.Empty(t, rows, "disabling observation while dialing suppresses the late callback")
				SetFingerprintObservationEnabled(true)
			} else {
				require.Len(t, rows, 1, "background handshakes must be visible before any lease is borrowed")
				row := rows[0]
				require.Equal(t, FingerprintObservationEventWSHandshake, row.EventKind)
				require.Equal(t, "frozen-upstream", row.AccountName)
				require.Equal(t, "frozen-key", row.APIKeyName)
				require.Equal(t, "frozen-user", row.Username)
				require.Equal(t, int64(9021), row.APIKeyID)
				require.Equal(t, int64(7021), row.UserID)
				require.Equal(t, "GET /v1/responses", row.InboundEndpoint)
				require.Equal(t, "physical-prewarm-agent", row.UserAgent)
				require.Equal(t, "us", row.OutboundCodexResidency)
				require.True(t, !row.Timestamp.Before(startedAt))
				require.Nil(t, row.InboundTimezoneObservations)
				require.Nil(t, row.OutboundTimezoneObservations)
				serialized, err := json.Marshal(row)
				require.NoError(t, err)
				require.NotContains(t, string(serialized), "never-retain")
			}
			lease, err := pool.Acquire(ctx, req)
			require.NoError(t, err)
			require.True(t, lease.Reused())
			require.True(t, lease.HandshakeObserverInstalled())
			(&OpenAIGatewayService{}).recordFingerprintObservationAfterOpenAIWSHandshake(c, account, lease, lease.FingerprintObservationHeaders())
			lease.Release()
			wantRows := 1
			if disableBeforeCompletion {
				wantRows = 0
			}
			require.Len(t, SnapshotFingerprintObservations(0), wantRows, "borrowing a background connection never creates or duplicates its handshake")
			require.Empty(t, headersReceived, "borrowing must not dial another physical connection")
		})
	}
}

func TestOpenAIWSPoolFailedDialDoesNotObserveHandshake(t *testing.T) {
	SetFingerprintObservationEnabled(false)
	SetFingerprintObservationEnabled(true)
	defer SetFingerprintObservationEnabled(false)
	pool := newOpenAIWSConnPool(&config.Config{})
	defer pool.Close()
	pool.setClientDialerForTest(&openAIWSAlwaysFailDialer{})
	account := &Account{ID: 6022, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, err := pool.dialConn(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://example.test/responses",
		HandshakeObserver: freezeFingerprintObservationWSHandshake(nil, account)})
	require.Error(t, err)
	require.Empty(t, SnapshotFingerprintObservations(0))
}
