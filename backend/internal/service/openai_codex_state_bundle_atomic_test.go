package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexCookieAtomicRepository struct {
	*codexStateMemoryRepo
	t       *testing.T
	saveErr error
	reject  bool
	writes  int
}

func (r *codexCookieAtomicRepository) SaveCAS(ctx context.Context, record CodexTurnStateRecord, version int64) (bool, error) {
	if record.EncryptedToken != "" {
		r.writes++
		require.NotEmpty(r.t, record.EncryptedCookieBundle, "ticket and cookie ciphertext must reach the same CAS")
		require.NotNil(r.t, record.CookieBundleExpiresAt)
		require.False(r.t, record.CookieBundleExpiresAt.After(record.ExpiresAt))
	}
	if r.saveErr != nil || r.reject {
		return false, r.saveErr
	}
	return r.codexStateMemoryRepo.SaveCAS(ctx, record, version)
}

func TestCodexTurnStateCookieBundlePublicationIsAtomic(t *testing.T) {
	for _, name := range []string{"accepted", "snapshot_failure", "save_failure", "cas_rejected"} {
		t.Run(name, func(t *testing.T) {
			isolateCodexHistory(t)
			isolateCodexTurnStateSummaryStore(t)
			state, memory, account := newCodexStateTestService(t)
			repo := &codexCookieAtomicRepository{codexStateMemoryRepo: memory, t: t}
			state.repo = repo
			gateway := &OpenAIGatewayService{codexTurnStateService: state}
			request := gateway.prepareOpenAICodexStateHTTPRequest(nil, account, codexStateHTTPRequest(t, `{"model":"gpt-5","input":"hello"}`))
			collector := request.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
			require.True(t, collector.attempt.Enabled)
			candidate := newCodexCookieCandidateTest()
			collector.attempt.cookieAttempt = candidate
			switch name {
			case "snapshot_failure":
				candidate.snapshotErr = openaicookies.ErrNoSnapshot
			case "save_failure":
				repo.saveErr = errors.New("synthetic CAS failure")
			case "cas_rejected":
				repo.reject = true
			}
			token := codexStateTestToken(10, state.now())
			state.Observe(collector.attempt, token)
			state.ObserveHeaders(collector.attempt, http.Header{"X-Codex-Safety-Buffering-Enabled": {"true"}, "X-Codex-Safety-Buffering-Faster-Model": {"gpt-5-mini"}})
			state.ObserveEvent(collector.attempt, []byte(`{"type":"response.completed","response":{"model":"gpt-5","status":"completed"}}`))
			err := state.Finish(context.Background(), collector.attempt, true)
			record := memory.records[collector.attempt.key]
			if name == "accepted" {
				require.NoError(t, err)
				require.Equal(t, 1, repo.writes)
				require.Equal(t, 1, candidate.commits)
				plain, decryptErr := state.encryptor.Decrypt(record.EncryptedToken)
				require.NoError(t, decryptErr)
				require.Equal(t, token, plain)
				require.NotEmpty(t, record.EncryptedCookieBundle)
				require.Equal(t, collector.attempt.AuthorizationGeneration, record.AuthorizationGeneration)
				evidence := decryptCodexCookieAtomicTestEnvelope(t, state, record).ResponseEvidence
				require.Equal(t, "gpt-5", evidence.UpstreamResponseModel)
				require.Equal(t, "exact", evidence.ModelRelation)
				require.Equal(t, "response.model", evidence.ModelEvidenceSource)
				require.NotNil(t, evidence.SafetyBufferingEnabled)
				require.True(t, *evidence.SafetyBufferingEnabled)
				require.Equal(t, "gpt-5-mini", evidence.SafetyBufferingFasterModel)
				require.Equal(t, "response", evidence.HeaderEvidenceScope)
			} else {
				if name == "cas_rejected" {
					require.NoError(t, err, "a stale publication is discarded without failing the delivered response")
					require.Positive(t, repo.writes, "publication reaches CAS and is rejected there")
				} else {
					require.Error(t, err)
				}
				require.Zero(t, candidate.commits)
				require.Equal(t, 1, candidate.discards)
				require.Empty(t, record.EncryptedToken)
				require.Empty(t, record.EncryptedCookieBundle)
				require.Nil(t, record.CookieBundleExpiresAt)
			}
		})
	}
}

func decryptCodexCookieAtomicTestEnvelope(t *testing.T, state *CodexTurnStateService, record CodexTurnStateRecord) codexTurnStateCookieEnvelope {
	t.Helper()
	plain, err := state.encryptor.Decrypt(record.EncryptedCookieBundle)
	require.NoError(t, err)
	var envelope codexTurnStateCookieEnvelope
	require.NoError(t, json.Unmarshal([]byte(plain), &envelope))
	return envelope
}

func TestCodexTurnStateCookieBundleResponseEvidenceFollowsAcceptedCAS(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "cas_rejected"
		if accepted {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			isolateCodexHistory(t)
			isolateCodexTurnStateSummaryStore(t)
			state, memory, account := newCodexStateTestService(t)
			repo := &codexCookieAtomicRepository{codexStateMemoryRepo: memory, t: t}
			state.repo = repo
			gateway := &OpenAIGatewayService{codexTurnStateService: state}
			publish := func(token, enabled, faster string) (*CodexTurnStateAttempt, *codexCookieCandidateTest) {
				request := gateway.prepareOpenAICodexStateHTTPRequest(nil, account, codexStateHTTPRequest(t, `{"model":"gpt-5","input":"hello"}`))
				attempt := request.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector).attempt
				candidate := newCodexCookieCandidateTest()
				attempt.cookieAttempt = candidate
				state.ObserveHeaders(attempt, http.Header{"X-Codex-Turn-State": {token}, "X-Codex-Safety-Buffering-Enabled": {enabled}, "X-Codex-Safety-Buffering-Faster-Model": {faster}})
				state.ObserveEvent(attempt, []byte(`{"type":"response.completed","response":{"model":"gpt-5","status":"completed"}}`))
				require.NoError(t, state.Finish(context.Background(), attempt, true))
				return attempt, candidate
			}
			seed, seedCandidate := publish(codexStateTestToken(10, state.now()), "true", "gpt-5-mini")
			require.Equal(t, 1, seedCandidate.commits)
			before := memory.records[seed.key]
			oldEnvelope := decryptCodexCookieAtomicTestEnvelope(t, state, before)
			require.NotNil(t, oldEnvelope.ResponseEvidence.SafetyBufferingEnabled)
			require.True(t, *oldEnvelope.ResponseEvidence.SafetyBufferingEnabled)
			require.Equal(t, "gpt-5-mini", oldEnvelope.ResponseEvidence.SafetyBufferingFasterModel)

			now := state.now().Add(time.Second)
			state.now = func() time.Time { return now }
			repo.reject = !accepted
			newToken := codexStateTestToken(10, now)
			_, candidate := publish(newToken, "false", "gpt-5.4")
			after := memory.records[seed.key]
			envelope := decryptCodexCookieAtomicTestEnvelope(t, state, after)
			if accepted {
				require.Equal(t, 1, candidate.commits)
				plain, err := state.encryptor.Decrypt(after.EncryptedToken)
				require.NoError(t, err)
				require.Equal(t, newToken, plain)
				require.NotEqual(t, before.EncryptedCookieBundle, after.EncryptedCookieBundle)
				require.NotNil(t, envelope.ResponseEvidence.SafetyBufferingEnabled)
				require.False(t, *envelope.ResponseEvidence.SafetyBufferingEnabled)
				require.Equal(t, "gpt-5.4", envelope.ResponseEvidence.SafetyBufferingFasterModel)
				require.Equal(t, "gpt-5", envelope.ResponseEvidence.UpstreamResponseModel)
				require.Equal(t, "exact", envelope.ResponseEvidence.ModelRelation)
			} else {
				require.Zero(t, candidate.commits)
				require.Equal(t, 1, candidate.discards)
				require.Equal(t, before.cacheIdentity(), after.cacheIdentity(), "rejected CAS preserves ticket and the entire encrypted bundle")
				require.Equal(t, oldEnvelope, envelope, "response evidence cannot escape the CAS independently of its accepted pair")
			}
		})
	}
}

func codexCookieAtomicTestBundle(now time.Time, value string) openaicookies.Bundle {
	expiresAt := now.Add(CodexTurnStateLifetime)
	return openaicookies.Bundle{ExpiresAt: expiresAt, Entries: []openaicookies.Entry{{
		Key: openaicookies.CookieKey("__oailb", "chatgpt.com", "/"), Name: "__oailb", Value: value,
		Domain: "chatgpt.com", Path: "/", HostOnly: true, Secure: true, ExpiresAt: expiresAt,
		CreatedAt: now, UpdatedAt: now,
	}}}
}

func TestCodexTurnStateCookieBundleBindingSeparatesModelOwnerAndAuthorization(t *testing.T) {
	state, _, _ := newCodexStateTestService(t)
	key := CodexTurnStateKey{OwnerAccountID: 1, Model: "gpt-5", Generation: "state-generation"}
	bundle := codexCookieAtomicTestBundle(state.now(), "synthetic-route")
	publication, err := state.encryptCodexCookiePublication(key, "authorization-one", bundle, codexStateTestBinding())
	require.NoError(t, err)
	for _, name := range []string{"windows", "macos", "linux", "other_owner", "other_model", "other_os_authorization", "corrupt_ciphertext", "expired_bundle", "bundle_outlives_ticket"} {
		t.Run(name, func(t *testing.T) {
			attempt := &CodexTurnStateAttempt{OwnerAccountID: key.OwnerAccountID, Model: key.Model, AuthorizationGeneration: "authorization-one", OSFamily: "windows",
				WireMode: "lite", OutboundBinding: codexStateTestBinding(),
				Snapshot: CodexTurnStateSnapshot{Token: "synthetic-ticket", EncryptedCookieBundle: publication.EncryptedCookieBundle, ExpiresAt: bundle.ExpiresAt, BundleBinding: codexStateTestBinding()}}
			switch name {
			case "windows", "macos", "linux":
				attempt.OSFamily = name
			case "other_owner":
				attempt.OwnerAccountID++
			case "other_model":
				attempt.Model = "gpt-5-mini"
			case "other_os_authorization":
				attempt.OSFamily, attempt.AuthorizationGeneration = "linux", "authorization-two"
			case "corrupt_ciphertext":
				attempt.Snapshot.EncryptedCookieBundle = "invalid"
			case "expired_bundle":
				before := state.now
				state.now = func() time.Time { return bundle.ExpiresAt }
				t.Cleanup(func() { state.now = before })
			case "bundle_outlives_ticket":
				attempt.Snapshot.ExpiresAt = bundle.ExpiresAt.Add(-time.Second)
			}
			decoded, err := state.codexCookieBundleForSnapshot(attempt)
			if name == "windows" || name == "macos" || name == "linux" {
				require.NoError(t, err)
				require.Equal(t, bundle, decoded, "OS changes alone do not partition a model's accepted bundle")
				decoded.Entries[0].Value = "changed-local-copy"
				again, err := state.codexCookieBundleForSnapshot(attempt)
				require.NoError(t, err)
				require.Equal(t, "synthetic-route", again.Entries[0].Value)
			} else {
				require.Error(t, err)
				require.Empty(t, decoded.Entries, "a rejected binding must never expose cookie values")
			}
		})
	}
}

type codexCookieAtomicRoundTripper func(*http.Request) (*http.Response, error)

func (f codexCookieAtomicRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func finishCodexCookieAtomicHTTPResponse(t *testing.T, request *http.Request, response *http.Response) {
	t.Helper()
	observeCodexTurnStateHTTPResponse(request, response, nil)
	beginCodexTelemetryHTTPParsing(response)
	observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.completed","response":{"model":"gpt-5","status":"completed"}}`), "")
	markCodexTurnStateHTTPDelivered(response)
	completeCodexTelemetryHTTPResponse(response, nil)
	require.NoError(t, response.Body.Close())
}

func TestCodexTurnStateCookieBundleToggleFencesFrozenRequestAndRestoresBaseline(t *testing.T) {
	isolateCodexHistory(t)
	isolateCodexTurnStateSummaryStore(t)
	state, repo, account := newCodexStateTestService(t)
	now := time.Now().UTC().Truncate(time.Second)
	state.now = func() time.Time { return now }
	gateway := &OpenAIGatewayService{codexTurnStateService: state}
	manager := openaicookies.NewManager()
	token := codexStateTestToken(10, now)
	body := `{"model":"gpt-5","input":"hello","client_metadata":{"x-codex-turn-state":"guarded-body","keep":"retained"}}`
	prepare := func() *http.Request {
		request := codexStateHTTPRequest(t, body)
		request.Header["x-codex-turn-state"] = []string{"guarded-header"}
		request.Header.Set("Cookie", "guarded-cookie=original")
		scope := openaicookies.Scope{OwnerAccountID: account.ID, OSFamily: "windows", AuthorizationGeneration: account.OpenAIOAuthAuthorizationGeneration}
		request = request.WithContext(openaicookies.WithScope(request.Context(), scope))
		return gateway.prepareOpenAICodexStateHTTPRequest(nil, account, request)
	}
	seed := prepare()
	transport := manager.Wrap(codexCookieAtomicRoundTripper(func(request *http.Request) (*http.Response, error) {
		require.Empty(t, request.Header.Get("Cookie"), "fresh collection cannot import caller cookies")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=accepted-route; Path=/; Secure; Max-Age=3600"}, "X-Codex-Turn-State": {token}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	response, err := transport.RoundTrip(seed)
	require.NoError(t, err)
	finishCodexCookieAtomicHTTPResponse(t, seed, response)
	seedAttempt := seed.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector).attempt
	seedRecord := repo.records[seedAttempt.key]
	require.NotEmpty(t, seedRecord.EncryptedToken)
	require.NotEmpty(t, seedRecord.EncryptedCookieBundle)
	frozen := prepare()
	require.Equal(t, token, frozen.Header.Get(openAICodexTurnStateHeader))
	// Later normalization remains intact when this feature restores its carriers.
	reader, err := frozen.GetBody()
	require.NoError(t, err)
	current, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	current, err = sjson.SetBytes(current, "normalized", true)
	require.NoError(t, err)
	frozen.Body = io.NopCloser(strings.NewReader(string(current)))
	frozen.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(current))), nil }

	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = false
	account.Extra["codex_turn_state_generation"] = "disabled-generation"
	disabled := prepare()
	disabledAttempt := disabled.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector).attempt
	require.False(t, disabledAttempt.Enabled)
	require.Nil(t, disabledAttempt.cookieAttempt)
	account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = true
	account.Extra["codex_turn_state_generation"] = "reenabled-generation"
	transport = manager.Wrap(codexCookieAtomicRoundTripper(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, []string{"guarded-header"}, request.Header["x-codex-turn-state"])
		require.Equal(t, "guarded-cookie=original", request.Header.Get("Cookie"))
		require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
		wire, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.Equal(t, "guarded-body", gjson.GetBytes(wire, "client_metadata.x-codex-turn-state").String())
		require.Equal(t, "retained", gjson.GetBytes(wire, "client_metadata.keep").String())
		require.True(t, gjson.GetBytes(wire, "normalized").Bool())
		require.Equal(t, int64(len(wire)), request.ContentLength)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=must-not-save; Path=/; Secure"}, "X-Codex-Turn-State": {token}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	response, err = transport.RoundTrip(frozen)
	require.NoError(t, err)
	finishCodexCookieAtomicHTTPResponse(t, frozen, response)
	require.Equal(t, seedRecord.cacheIdentity(), repo.records[seedAttempt.key].cacheIdentity())

	fresh := prepare()
	freshAttempt := fresh.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector).attempt
	require.True(t, freshAttempt.Enabled)
	require.NotEqual(t, seedAttempt.key.Generation, freshAttempt.key.Generation)
	require.Empty(t, freshAttempt.Snapshot.Token, "reenabling cannot revive a prior generation's ticket")
	require.Empty(t, freshAttempt.Snapshot.EncryptedCookieBundle)
	transport = manager.Wrap(codexCookieAtomicRoundTripper(func(request *http.Request) (*http.Response, error) {
		require.Empty(t, request.Header.Get("Cookie"), "new generation cannot reuse old or rejected cookies")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"__oailb=new-generation; Path=/; Secure"}, "X-Codex-Turn-State": {token}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	}))
	response, err = transport.RoundTrip(fresh)
	require.NoError(t, err)
	finishCodexCookieAtomicHTTPResponse(t, fresh, response)
	replay := prepare()
	replayAttempt := replay.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector).attempt
	bundle, err := state.codexCookieBundleForSnapshot(replayAttempt)
	require.NoError(t, err)
	require.Len(t, bundle.Entries, 1)
	require.Equal(t, "new-generation", bundle.Entries[0].Value)
	require.NoError(t, state.Finish(context.Background(), replayAttempt, false))
}
