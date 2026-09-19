package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexStatePassthroughAccounts struct {
	AccountRepository
	mu      sync.Mutex
	account *Account
}

type codexStatePassthroughEncryptor struct{}

func (codexStatePassthroughEncryptor) Encrypt(value string) (string, error) {
	return "encrypted:" + value, nil
}

func (codexStatePassthroughEncryptor) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, "encrypted:") {
		return "", fmt.Errorf("invalid ciphertext")
	}
	return strings.TrimPrefix(value, "encrypted:"), nil
}

func (r *codexStatePassthroughAccounts) GetByID(context.Context, int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := *r.account
	account.Extra = maps.Clone(r.account.Extra)
	account.Credentials = maps.Clone(r.account.Credentials)
	return &account, nil
}

func (r *codexStatePassthroughAccounts) disable() {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := *r.account
	account.Extra = maps.Clone(account.Extra)
	account.Extra[CodexTurnStateExtraKey] = CodexTurnStateConfig{AccountType: "personal"}
	account.Extra[CodexTurnStateGenerationExtraKey] = "changed-generation"
	r.account = &account
}

type codexStatePassthroughRepository struct {
	CodexTurnStateRepository
	mu      sync.Mutex
	records map[CodexTurnStateKey]CodexTurnStateRecord
	ended   int
}

func (r *codexStatePassthroughRepository) BeginBusiness(_ context.Context, key CodexTurnStateKey, _ string, now, _ time.Time) (*CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.records[key]
	record.OwnerAccountID, record.Model, record.Generation = key.OwnerAccountID, key.Model, key.Generation
	record.LastBusinessAt = now
	r.records[key] = record
	return &record, nil
}

func (r *codexStatePassthroughRepository) EndBusiness(context.Context, CodexTurnStateKey, string) error {
	r.mu.Lock()
	r.ended++
	r.mu.Unlock()
	return nil
}

func (r *codexStatePassthroughRepository) Get(_ context.Context, key CodexTurnStateKey) (*CodexTurnStateRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, exists := r.records[key]
	if !exists {
		return nil, nil
	}
	return &record, nil
}

func (r *codexStatePassthroughRepository) SaveCAS(_ context.Context, record CodexTurnStateRecord, expected int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records[record.Key()].Version != expected {
		return false, nil
	}
	record.Version = expected + 1
	r.records[record.Key()] = record
	return true, nil
}

func (*codexStatePassthroughRepository) PublishCancel(context.Context, CodexTurnStateKey) error {
	return nil
}

type codexStatePassthroughDialer struct {
	conn    *stagedPassthroughConn
	request chan http.Header
	headers http.Header
}

func (d *codexStatePassthroughDialer) Dial(_ context.Context, _ string, headers http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.request <- headers.Clone()
	return d.conn, http.StatusSwitchingProtocols, d.headers.Clone(), nil
}

func codexStatePassthroughToken(marker byte, issuedAt time.Time) string {
	value := make([]byte, 57+160)
	value[0] = 0x80
	binary.BigEndian.PutUint64(value[1:9], uint64(issuedAt.Unix()))
	for index := 9; index < len(value); index++ {
		value[index] = marker
	}
	return base64.URLEncoding.EncodeToString(value)
}

func newCodexStatePassthroughHarness(t *testing.T, enabled bool) (*OpenAIGatewayService, *Account, *codexStatePassthroughAccounts, *codexStatePassthroughRepository, *codexStatePassthroughDialer) {
	t.Helper()
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	upstream := newStagedPassthroughConn()
	svc := newPassthroughLifecycleService(cfg, upstream)
	account := passthroughLifecycleAccount()
	account.Type = AccountTypeOAuth
	account.Credentials = map[string]any{"access_token": "sk-test", "chatgpt_account_id": "test-chatgpt-account"}
	account.Extra = map[string]any{
		"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		CodexTurnStateExtraKey:                      CodexTurnStateConfig{Enabled: enabled, AccountType: "personal"},
		CodexTurnStateGenerationExtraKey:            "initial-generation",
	}
	accounts := &codexStatePassthroughAccounts{account: account}
	repo := &codexStatePassthroughRepository{records: make(map[CodexTurnStateKey]CodexTurnStateRecord)}
	svc.accountRepo = accounts
	svc.codexTurnStateService = NewCodexTurnStateService(repo, accounts, codexStatePassthroughEncryptor{}, nil)
	svc.codexTurnStateService.modelPolicy = newCodexStateTestModelPolicy("gpt-5.5", "gpt-5.4")
	dialer := &codexStatePassthroughDialer{conn: upstream, request: make(chan http.Header, 1), headers: make(http.Header)}
	svc.openaiWSPassthroughDialer = dialer
	return svc, account, accounts, repo, dialer
}

func seedCodexStatePassthroughModel(t *testing.T, repo *codexStatePassthroughRepository, account *Account, model, token string) CodexTurnStateKey {
	t.Helper()
	shape, err := ParseCodexTurnState(token, "personal", time.Now())
	require.NoError(t, err)
	encrypted, err := (codexStatePassthroughEncryptor{}).Encrypt(token)
	require.NoError(t, err)
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: model, Generation: CodexTurnStateGenerationForAccount(account)}
	repo.records[key] = CodexTurnStateRecord{OwnerAccountID: key.OwnerAccountID, Model: key.Model, Generation: key.Generation, Version: 1,
		EncryptedToken: encrypted, IssuedAt: shape.IssuedAt, ExpiresAt: shape.ExpiresAt, TokenLength: shape.TokenLength, CipherBlocks: shape.CipherBlocks, Source: "collector", Shape: shape.Shape}
	return key
}

func readCodexStatePassthroughFrame(t *testing.T, ctx context.Context, client *coderws.Conn) []byte {
	t.Helper()
	_, payload, err := client.Read(ctx)
	require.NoError(t, err)
	return payload
}

func TestCodexStatePassthroughUsesFinalModelAndLearnsLateMetadata(t *testing.T) {
	svc, account, _, repo, dialer := newCodexStatePassthroughHarness(t, true)
	now := time.Now()
	firstToken := codexStatePassthroughToken(1, now.Add(-10*time.Minute))
	secondToken := codexStatePassthroughToken(2, now.Add(-9*time.Minute))
	newToken := codexStatePassthroughToken(3, now.Add(-time.Minute))
	keyFirst := seedCodexStatePassthroughModel(t, repo, account, "gpt-5.5", firstToken)
	keySecond := seedCodexStatePassthroughModel(t, repo, account, "gpt-5.4", secondToken)
	dialer.headers.Set(openAIWSTurnStateHeader, codexStatePassthroughToken(4, now.Add(-2*time.Minute)))
	server, _ := startPassthroughLifecycleServer(t, context.Background(), svc, account)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"old-client-header"}}})
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[],"client_metadata":{"x-codex-turn-state":"old-client-frame"}}`)))
	first := requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
	require.Equal(t, firstToken, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").String())
	require.Empty(t, (<-dialer.request).Get(openAIWSTurnStateHeader))
	dialer.conn.Send(`{"type":"response.output_text.delta","delta":"ready"}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	dialer.conn.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"X-Codex-Turn-State":%q}}`, newToken))
	readCodexStatePassthroughFrame(t, ctx, client)
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	require.Eventually(t, func() bool {
		record, _ := repo.Get(ctx, keyFirst)
		return record != nil && record.Source == "business" && record.IssuedAt.Equal(time.Unix(now.Add(-time.Minute).Unix(), 0))
	}, time.Second, time.Millisecond)
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)))
	second := requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
	require.Equal(t, secondToken, gjson.GetBytes(second, "client_metadata.x-codex-turn-state").String())
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_second","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	require.Eventually(t, func() bool { repo.mu.Lock(); defer repo.mu.Unlock(); return repo.ended == 2 }, time.Second, time.Millisecond)
	record, err := repo.Get(ctx, keySecond)
	require.NoError(t, err)
	require.Equal(t, int64(1), record.Version, "a reused socket must not relearn its original handshake state under the next model")
}

func TestCodexStatePassthroughMigratesOnlyFirstGuardedHeader(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			svc, account, _, _, dialer := newCodexStatePassthroughHarness(t, enabled)
			server, _ := startPassthroughLifecycleServer(t, context.Background(), svc, account)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"client-header"}}})
			require.NoError(t, err)
			defer client.CloseNow()
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
			first := requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
			headers := <-dialer.request
			if enabled {
				require.Empty(t, headers.Get(openAIWSTurnStateHeader))
				require.Equal(t, "client-header", gjson.GetBytes(first, "client_metadata.x-codex-turn-state").String())
			} else {
				require.Equal(t, "client-header", headers.Get(openAIWSTurnStateHeader))
				require.False(t, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").Exists())
			}
			dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_migration","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
			readCodexStatePassthroughFrame(t, ctx, client)
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
			second := requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
			require.False(t, gjson.GetBytes(second, "client_metadata.x-codex-turn-state").Exists())
		})
	}
}

func TestCodexStatePassthroughAbandonedMetadataDoesNotLearn(t *testing.T) {
	svc, account, _, repo, dialer := newCodexStatePassthroughHarness(t, true)
	server, serverErr := startPassthroughLifecycleServer(t, context.Background(), svc, account)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
	requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
	token := codexStatePassthroughToken(9, time.Now().Add(-time.Minute))
	dialer.conn.Send(fmt.Sprintf(`{"type":"response.metadata","headers":{"x-codex-turn-state":%q}}`, token))
	readCodexStatePassthroughFrame(t, ctx, client)
	dialer.conn.Fail(fmt.Errorf("upstream abandoned before semantic output"))
	_, _, _ = client.Read(ctx)
	select {
	case <-serverErr:
	case <-ctx.Done():
		t.Fatal("passthrough did not finish")
	}
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.5", Generation: CodexTurnStateGenerationForAccount(account)}
	record, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Empty(t, record.EncryptedToken)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.ended)
}

func TestCodexStatePassthroughPrewarmDiscardsHandshakeState(t *testing.T) {
	svc, account, _, repo, dialer := newCodexStatePassthroughHarness(t, true)
	dialer.headers.Set(openAIWSTurnStateHeader, codexStatePassthroughToken(7, time.Now().Add(-time.Minute)))
	server, _ := startPassthroughLifecycleServer(t, context.Background(), svc, account)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], &coderws.DialOptions{HTTPHeader: http.Header{"X-Codex-Turn-State": {"prewarm-header"}}})
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","generate":false,"input":[]}`)))
	first := requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
	require.False(t, gjson.GetBytes(first, "client_metadata.x-codex-turn-state").Exists())
	require.Empty(t, (<-dialer.request).Get(openAIWSTurnStateHeader))
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_prewarm","model":"gpt-5.5"}}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","input":[]}`)))
	second := requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
	require.False(t, gjson.GetBytes(second, "client_metadata.x-codex-turn-state").Exists(), "prewarm must consume the first-header migration privilege")
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_after_prewarm","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`)
	readCodexStatePassthroughFrame(t, ctx, client)
	require.Eventually(t, func() bool { repo.mu.Lock(); defer repo.mu.Unlock(); return repo.ended == 1 }, time.Second, time.Millisecond)
	key := CodexTurnStateKey{OwnerAccountID: account.ID, Model: "gpt-5.4", Generation: CodexTurnStateGenerationForAccount(account)}
	record, err := repo.Get(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Empty(t, record.EncryptedToken, "a later model cannot claim the prewarm handshake response")
}

func TestCodexStatePassthroughModeChangeClosesAfterTerminal(t *testing.T) {
	svc, account, accounts, _, dialer := newCodexStatePassthroughHarness(t, true)
	server, _ := startPassthroughLifecycleServer(t, context.Background(), svc, account)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+server.URL[4:], nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","input":[]}`)))
	requirePassthroughUpstreamWrite(t, dialer.conn, 3*time.Second)
	accounts.disable()
	dialer.conn.Send(`{"type":"response.output_text.delta","delta":"still running"}`)
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
	dialer.conn.Send(`{"type":"response.completed","response":{"id":"resp_changed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
	require.Equal(t, "response.completed", gjson.GetBytes(readCodexStatePassthroughFrame(t, ctx, client), "type").String())
	_, _, err = client.Read(ctx)
	require.Equal(t, coderws.StatusNormalClosure, coderws.CloseStatus(err))
}
