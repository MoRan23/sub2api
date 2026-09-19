package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexWSStateTestRepo struct {
	CodexTurnStateRepository
	mu       sync.Mutex
	records  map[CodexTurnStateKey]CodexTurnStateRecord
	active   map[string]bool
	failRead bool
}

func (r *codexWSStateTestRepo) BeginBusiness(_ context.Context, key CodexTurnStateKey, id string, now, _ time.Time) (*CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failRead {
		return nil, errors.New("storage unavailable")
	}
	row := r.records[key]
	row.OwnerAccountID, row.Model, row.Generation = key.OwnerAccountID, key.Model, key.Generation
	row.LastBusinessAt = now
	r.records[key] = row
	r.active[id] = true
	return &row, nil
}

func (r *codexWSStateTestRepo) EndBusiness(_ context.Context, _ CodexTurnStateKey, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, id)
	return nil
}

func (r *codexWSStateTestRepo) Get(_ context.Context, key CodexTurnStateKey) (*CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failRead {
		return nil, errors.New("storage unavailable")
	}
	row, ok := r.records[key]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (r *codexWSStateTestRepo) SaveCAS(_ context.Context, row CodexTurnStateRecord, version int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.records[row.Key()]
	if !ok || current.Version != version {
		return false, nil
	}
	row.Version = version + 1
	r.records[row.Key()] = row
	return true, nil
}

func (r *codexWSStateTestRepo) PublishCancel(context.Context, CodexTurnStateKey) error { return nil }

func (r *codexWSStateTestRepo) record(model string) CodexTurnStateRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, row := range r.records {
		if key.Model == model {
			return row
		}
	}
	return CodexTurnStateRecord{}
}

func (r *codexWSStateTestRepo) activeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

type codexWSStateTestAccounts struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
}

type codexWSStateTestEncryptor struct{}

func (codexWSStateTestEncryptor) Encrypt(token string) (string, error) {
	return "encrypted:" + token, nil
}
func (codexWSStateTestEncryptor) Decrypt(token string) (string, error) {
	if !strings.HasPrefix(token, "encrypted:") {
		return "", errors.New("invalid test ciphertext")
	}
	return strings.TrimPrefix(token, "encrypted:"), nil
}

func (r *codexWSStateTestAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, errors.New("account unavailable")
	}
	copy := *r.account
	copy.Extra = maps.Clone(r.account.Extra)
	copy.Credentials = maps.Clone(r.account.Credentials)
	return &copy, nil
}

func (r *codexWSStateTestAccounts) update(fn func(*Account)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.account)
}

func newCodexWSStateTestGateway(t *testing.T, accountType string) (*OpenAIGatewayService, *Account, *codexWSStateTestRepo, *codexWSStateTestAccounts) {
	t.Helper()
	account := &Account{ID: 8911, Name: "turn-state-ws", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 2,
		Credentials: map[string]any{"access_token": "test-access", "chatgpt_account_id": "test-chatgpt-account", "plan_type": "plus"},
		Extra:       map[string]any{CodexTurnStateExtraKey: map[string]any{"enabled": true, "account_type": accountType}, CodexTurnStateGenerationExtraKey: "generation-one"}}
	copy := *account
	copy.Extra, copy.Credentials = maps.Clone(account.Extra), maps.Clone(account.Credentials)
	accounts := &codexWSStateTestAccounts{account: &copy}
	repo := &codexWSStateTestRepo{records: make(map[CodexTurnStateKey]CodexTurnStateRecord), active: make(map[string]bool)}
	service := NewCodexTurnStateService(repo, accounts, codexWSStateTestEncryptor{}, nil)
	service.modelPolicy = newCodexStateTestModelPolicy("gpt-final", "gpt-other", "gpt-5.1", "gpt-5.5", "gpt-5.4")
	svc := &OpenAIGatewayService{codexTurnStateService: service, accountRepo: accounts, toolCorrector: NewCodexToolCorrector(), cache: &stubGatewayCache{}}
	return svc, account, repo, accounts
}

func makeCodexWSStateTestToken(blocks int, issued time.Time) string {
	raw := make([]byte, 57+16*blocks)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued.Unix()))
	return base64.URLEncoding.EncodeToString(raw)
}

func codexWSStateTestHeaders(account *Account) http.Header {
	return http.Header{"Authorization": {"Bearer " + account.GetCredential("access_token")}, "Chatgpt-Account-Id": {account.GetCredential("chatgpt_account_id")}}
}

func seedCodexWSState(t *testing.T, svc *OpenAIGatewayService, account *Account, model, token string) {
	t.Helper()
	attempt, err := svc.codexTurnStateService.Prepare(context.Background(), account, model)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	svc.codexTurnStateService.Observe(attempt, token)
	require.NoError(t, svc.codexTurnStateService.Finish(context.Background(), attempt, true))
}

func TestCodexWSStateFrameOverridesAndSeparatesModels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		blocks int
	}{{"personal", 10}, {"team_business", 12}} {
		t.Run(tc.name, func(t *testing.T) {
			svc, account, repo, _ := newCodexWSStateTestGateway(t, tc.name)
			token := makeCodexWSStateTestToken(tc.blocks, time.Now().Add(-time.Minute))
			seedCodexWSState(t, svc, account, "gpt-final", token)
			payload := []byte(`{"type":"response.create","model":"gpt-final","client_metadata":{"x-codex-turn-state":"guarded-client","other":"kept"}}`)
			final, attempt, err := svc.prepareOpenAICodexWSStateFrame(context.Background(), nil, account, payload, "guarded-header", codexWSStateTestHeaders(account))
			require.NoError(t, err)
			require.Equal(t, token, gjson.GetBytes(final, "client_metadata.x-codex-turn-state").String())
			require.Equal(t, "kept", gjson.GetBytes(final, "client_metadata.other").String())
			require.Contains(t, string(payload), "guarded-client", "caller-owned retry payload stays pristine")
			svc.finishOpenAICodexWSState(context.Background(), attempt, false)
			other := []byte(`{"type":"response.create","model":"gpt-other"}`)
			final, attempt, err = svc.prepareOpenAICodexWSStateFrame(context.Background(), nil, account, other, "", codexWSStateTestHeaders(account))
			require.NoError(t, err)
			require.Equal(t, other, final)
			require.Empty(t, attempt.Snapshot.Token)
			svc.finishOpenAICodexWSState(context.Background(), attempt, false)
			require.Zero(t, repo.activeCount())
		})
	}
}

func TestCodexWSStateCacheMissMigrationAndControlFrames(t *testing.T) {
	svc, account, repo, _ := newCodexWSStateTestGateway(t, "personal")
	ctx := context.Background()
	payload := []byte(`{"type":"response.create","model":"gpt-final"}`)
	first, attempt, err := svc.prepareOpenAICodexWSStateFrame(ctx, nil, account, payload, "guarded-header", codexWSStateTestHeaders(account))
	require.NoError(t, err)
	require.Equal(t, "guarded-header", gjson.GetBytes(first, "client_metadata.x-codex-turn-state").String())
	svc.finishOpenAICodexWSState(ctx, attempt, true)
	next, attempt, err := svc.prepareOpenAICodexWSStateFrame(ctx, nil, account, payload, "", codexWSStateTestHeaders(account))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(next, "client_metadata.x-codex-turn-state").Exists())
	svc.finishOpenAICodexWSState(ctx, attempt, true)
	for _, payload := range [][]byte{[]byte(`{"type":"response.cancel"}`), []byte(`{"type":"response.create","model":"gpt-final","generate":false}`)} {
		final, skipped, err := svc.prepareOpenAICodexWSStateFrame(ctx, nil, account, payload, "guarded-header", codexWSStateTestHeaders(account))
		require.NoError(t, err)
		require.Nil(t, skipped)
		require.Equal(t, payload, final)
	}
	require.Zero(t, repo.activeCount())
}

func TestCodexWSStateDisabledAndUnavailablePreserveInput(t *testing.T) {
	svc, account, repo, accounts := newCodexWSStateTestGateway(t, "personal")
	accounts.update(func(a *Account) {
		a.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": false, "account_type": "personal"}
	})
	payload := []byte(`{"type":"response.create","model":"gpt-final","client_metadata":{"x-codex-turn-state":"guarded-client"}}`)
	final, attempt, err := svc.prepareOpenAICodexWSStateFrame(context.Background(), nil, account, payload, "", codexWSStateTestHeaders(account))
	require.NoError(t, err)
	require.Nil(t, attempt)
	require.Equal(t, payload, final)
	accounts.update(func(a *Account) {
		a.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": true, "account_type": "personal"}
	})
	repo.mu.Lock()
	repo.failRead = true
	repo.mu.Unlock()
	final, attempt, err = svc.prepareOpenAICodexWSStateFrame(context.Background(), nil, account, payload, "", codexWSStateTestHeaders(account))
	require.NoError(t, err)
	require.Nil(t, attempt)
	require.Equal(t, payload, final)
}

func TestCodexWSStateMetadataRequiresDeliveryAndIsBoundToAttempt(t *testing.T) {
	svc, account, repo, _ := newCodexWSStateTestGateway(t, "personal")
	token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
	metadata := []byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"X-CODEX-TURN-STATE":%q}}`, token))
	for _, delivered := range []bool{false, true} {
		payload := []byte(`{"type":"response.create","model":"gpt-final"}`)
		_, attempt, err := svc.prepareOpenAICodexWSStateFrame(context.Background(), nil, account, payload, "", codexWSStateTestHeaders(account))
		require.NoError(t, err)
		svc.observeOpenAICodexWSStateEvent(attempt, metadata)
		svc.finishOpenAICodexWSState(context.Background(), attempt, delivered)
		if delivered {
			require.NotEmpty(t, repo.record("gpt-final").EncryptedToken)
		} else {
			require.Empty(t, repo.record("gpt-final").EncryptedToken)
		}
	}
	require.Zero(t, repo.activeCount())
	require.Empty(t, repo.record("unrelated").EncryptedToken)
}

func TestCodexWSStateGenerationChangesRetireSocketAndRejectOldResponse(t *testing.T) {
	svc, account, repo, accounts := newCodexWSStateTestGateway(t, "personal")
	ctx := context.Background()
	mode := svc.openAICodexWSStateMode(ctx, account)
	_, attempt, err := svc.prepareOpenAICodexWSStateFrame(ctx, nil, account, []byte(`{"type":"response.create","model":"gpt-final"}`), "", codexWSStateTestHeaders(account))
	require.NoError(t, err)
	accounts.update(func(a *Account) { a.Extra[CodexTurnStateGenerationExtraKey] = "generation-two" })
	require.True(t, svc.openAICodexWSStateModeChanged(ctx, account, mode))
	svc.observeOpenAICodexWSStateEvent(attempt, []byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, makeCodexWSStateTestToken(10, time.Now()))))
	svc.finishOpenAICodexWSState(ctx, attempt, true)
	require.Empty(t, repo.record("gpt-final").EncryptedToken)
	require.Zero(t, repo.activeCount())
}

func TestCodexWSStateAccountReadFailureDoesNotCloseExistingSocket(t *testing.T) {
	svc, account, _, accounts := newCodexWSStateTestGateway(t, "personal")
	ctx := context.Background()
	mode := svc.openAICodexWSStateMode(ctx, account)
	accounts.mu.Lock()
	accounts.account = nil
	accounts.mu.Unlock()
	require.False(t, svc.openAICodexWSStateModeChanged(ctx, account, mode), "outage is not a verified configuration change")
	payload := []byte(`{"type":"response.create","model":"gpt-final","client_metadata":{"x-codex-turn-state":"client"}}`)
	final, attempt, err := svc.prepareOpenAICodexWSStateFrame(ctx, nil, account, payload, "", codexWSStateTestHeaders(account))
	require.NoError(t, err)
	require.Nil(t, attempt)
	require.Equal(t, payload, final)
}

func TestCodexWSStateStalePhysicalCredentialDoesNotInjectOrLearn(t *testing.T) {
	svc, account, repo, _ := newCodexWSStateTestGateway(t, "personal")
	token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
	seedCodexWSState(t, svc, account, "gpt-final", token)
	payload := []byte(`{"type":"response.create","model":"gpt-final","client_metadata":{"x-codex-turn-state":"guarded-client"}}`)
	oldHeaders := codexWSStateTestHeaders(account)
	oldHeaders.Set("Authorization", "Bearer old-physical-credential")
	final, attempt, err := svc.prepareOpenAICodexWSStateFrame(context.Background(), nil, account, payload, "", oldHeaders)
	require.NoError(t, err)
	require.Nil(t, attempt)
	require.Equal(t, payload, final)
	require.Zero(t, repo.activeCount())
	require.Equal(t, int64(1), repo.record("gpt-final").Version)
}

func TestCodexWSStatePhysicalCredentialSnapshotUsesActualHeaders(t *testing.T) {
	actual := http.Header{"Authorization": {"Bearer physical"}, "Chatgpt-Account-Id": {"owner"}, "X-Unrelated": {"discard"}}
	conn := &coderOpenAIWSClientConn{codexStateCredentialHeaders: cloneOpenAICodexWSCredentialHeaders(actual)}
	result := openAIWSCodexStateCredentialHeaders(conn, http.Header{"Authorization": {"Bearer stale-fallback"}})
	require.Equal(t, "Bearer physical", result.Get("Authorization"))
	require.Equal(t, "owner", result.Get("ChatGPT-Account-Id"))
	require.Empty(t, result.Get("X-Unrelated"))
	result.Set("Authorization", "mutated")
	require.Equal(t, "Bearer physical", conn.CodexStateCredentialHeaders().Get("Authorization"))
	require.Empty(t, conn.FingerprintObservationHeaders().Get("Authorization"))
}

func TestCodexWSStateHandshakeCandidateClaimedOnceAcrossLeases(t *testing.T) {
	headers := http.Header{}
	headers.Set(openAIWSTurnStateHeader, "first-model-only")
	conn := newOpenAIWSConn("claim", 1, nil, headers)
	first := &openAIWSConnLease{conn: conn}
	second := &openAIWSConnLease{conn: conn}
	require.Equal(t, "first-model-only", first.ClaimCodexStateHandshakeHeaders().Get(openAIWSTurnStateHeader))
	require.Nil(t, second.ClaimCodexStateHandshakeHeaders())
	require.Equal(t, "first-model-only", conn.handshakeHeaders.Get(openAIWSTurnStateHeader), "claim does not change legacy response headers")
}

func TestCodexWSStatePoolCompatibilityIncludesServerMode(t *testing.T) {
	base := openAIWSAcquireRequest{Account: &Account{ID: 1}, WSURL: "wss://example.test/responses", Headers: http.Header{}}
	enabled := base
	enabled.CodexStateMode = (openAICodexWSStateMode{Enabled: true, Generation: "one"}).poolKey()
	changed := enabled
	changed.CodexStateMode = (openAICodexWSStateMode{Enabled: true, Generation: "two"}).poolKey()
	require.NotEqual(t, openAIWSAcquireCompatibility(base), openAIWSAcquireCompatibility(enabled))
	require.NotEqual(t, openAIWSAcquireCompatibility(enabled), openAIWSAcquireCompatibility(changed))
	require.Empty(t, base.Headers)
}

func TestCodexWSStatePooledHTTPFinalWireAndNaturalLearning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			svc, account, repo, _ := newCodexWSStateTestGateway(t, "personal")
			cfg := newOpenAIWSV2TestConfig()
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			svc.cfg = cfg
			svc.httpUpstream = &httpUpstreamRecorder{}
			svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
			token := makeCodexWSStateTestToken(10, time.Now().Add(-time.Minute))
			wantOutboundToken := "guarded-client-header"
			if stream {
				wantOutboundToken = makeCodexWSStateTestToken(10, time.Now().Add(-2*time.Minute))
				seedCodexWSState(t, svc, account, "gpt-5.1", wantOutboundToken)
			}
			conn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(fmt.Sprintf(`{"type":"response.metadata","headers":{"X-CoDeX-TuRn-StAtE":%q}}`, token)),
				[]byte(`{"type":"response.completed","response":{"id":"resp_state","model":"gpt-5.1","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}`),
			}}
			pool := newOpenAIWSConnPool(cfg)
			dialer := &openAIWSCaptureDialer{conn: conn}
			pool.setClientDialerForTest(dialer)
			svc.openaiWSPool = pool
			defer pool.Close()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
			c.Request.Header.Set(openAIWSTurnStateHeader, "guarded-client-header")
			var recovered bool
			result, err := svc.forwardOpenAIWSV2(context.Background(), c, account, map[string]any{"model": "gpt-5.1", "stream": stream, "input": "hi"}, "", "", "test-access", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, stream, "gpt-5.1", "gpt-5.1", time.Now(), 0, "", &recovered)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotEmpty(t, repo.record("gpt-5.1").EncryptedToken)
			require.Equal(t, "business", repo.record("gpt-5.1").Source)
			require.Zero(t, repo.activeCount())
			frame, _ := json.Marshal(conn.lastWrite)
			require.Equal(t, wantOutboundToken, gjson.GetBytes(frame, "client_metadata.x-codex-turn-state").String())
			dialer.mu.Lock()
			handshakeState := dialer.lastHeaders.Get(openAIWSTurnStateHeader)
			dialer.mu.Unlock()
			require.Empty(t, handshakeState, "enabled physical WS handshake must omit turn state")
		})
	}
}

type codexWSStatePooledConn struct{ *stagedPassthroughConn }

func (c *codexWSStatePooledConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

type codexWSStatePooledDialer struct {
	conn     *codexWSStatePooledConn
	request  chan http.Header
	response http.Header
}

func (d *codexWSStatePooledDialer) Dial(_ context.Context, _ string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.request <- headers.Clone()
	return d.conn, http.StatusSwitchingProtocols, d.response.Clone(), nil
}

func newCodexWSStateIngressHarness(t *testing.T) (*OpenAIGatewayService, *Account, *codexWSStateTestRepo, *codexWSStateTestAccounts, *codexWSStatePooledDialer) {
	t.Helper()
	svc, account, repo, accounts := newCodexWSStateTestGateway(t, "personal")
	account.Credentials["access_token"] = "sk-test" // Existing downstream test server's explicit transport credential.
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	accounts.update(func(a *Account) { a.Credentials["access_token"] = "sk-test" })
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 0
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	svc.cfg = cfg
	svc.httpUpstream = &httpUpstreamRecorder{}
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	dialer := &codexWSStatePooledDialer{conn: &codexWSStatePooledConn{newStagedPassthroughConn()}, request: make(chan http.Header, 2), response: make(http.Header)}
	svc.openaiWSPool = newOpenAIWSConnPool(cfg)
	svc.openaiWSPool.setClientDialerForTest(dialer)
	t.Cleanup(svc.openaiWSPool.Close)
	return svc, account, repo, accounts, dialer
}

func TestCodexWSStateIngressSeparatesTurnsModelsAndLateMetadata(t *testing.T) {
	svc, account, repo, _, dialer := newCodexWSStateIngressHarness(t)
	now := time.Now()
	firstToken := makeCodexWSStateTestToken(10, now.Add(-10*time.Minute))
	secondToken := makeCodexWSStateTestToken(10, now.Add(-9*time.Minute))
	newToken := makeCodexWSStateTestToken(10, now.Add(-time.Minute))
	seedCodexWSState(t, svc, account, "gpt-5.5", firstToken)
	seedCodexWSState(t, svc, account, "gpt-5.4", secondToken)
	dialer.response.Set(openAIWSTurnStateHeader, makeCodexWSStateTestToken(10, now.Add(-2*time.Minute)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, _ := startPassthroughLifecycleServer(t, ctx, svc, account)
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"client-header"}}})
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
	first := requirePassthroughUpstreamWrite(t, dialer.conn.stagedPassthroughConn, 3*time.Second)
	require.Equal(t, firstToken, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").String())
	require.Empty(t, (<-dialer.request).Get(openAIWSTurnStateHeader))
	dialer.conn.Send(`{"type":"response.output_text.delta","delta":"ready"}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	dialer.conn.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"X-CODEX-TURN-STATE":%q}}`, newToken))
	readCodexStatePassthroughFrame(t, ctx, client)
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_pooled_first","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	require.Eventually(t, func() bool { return repo.record("gpt-5.5").IssuedAt.Equal(time.Unix(now.Add(-time.Minute).Unix(), 0)) }, time.Second, time.Millisecond)
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)))
	second := requirePassthroughUpstreamWrite(t, dialer.conn.stagedPassthroughConn, 3*time.Second)
	require.Equal(t, secondToken, gjson.GetBytes(second, "client_metadata.x-codex-turn-state").String())
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_pooled_second","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	require.Eventually(t, func() bool { return repo.activeCount() == 0 }, time.Second, time.Millisecond)
	require.Equal(t, int64(1), repo.record("gpt-5.4").Version, "first physical handshake candidate must not be relearned for second model")
	require.Empty(t, dialer.request, "two turns should keep the same physical socket")
}

func TestCodexWSStateIngressModeChangeClosesAfterDeliveredTurn(t *testing.T) {
	svc, account, repo, accounts, dialer := newCodexWSStateIngressHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
	requirePassthroughUpstreamWrite(t, dialer.conn.stagedPassthroughConn, 3*time.Second)
	accounts.update(func(a *Account) {
		a.Extra[CodexTurnStateExtraKey] = map[string]any{"enabled": false, "account_type": "personal"}
		a.Extra[CodexTurnStateGenerationExtraKey] = "disabled-generation"
	})
	dialer.conn.Send(`{"type":"response.output_text.delta","delta":"still running"}`)
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_pooled_disabled","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
	require.Equal(t, "response.completed", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
	select {
	case err := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode(), "the gateway handler turns this service result into its 1000 close frame")
	case <-ctx.Done():
		t.Fatal("ingress did not retire after the delivered terminal event")
	}
	require.Zero(t, repo.activeCount())
}
