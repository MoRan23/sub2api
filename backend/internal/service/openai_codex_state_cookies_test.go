package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type codexCookieCandidateTest struct {
	commits, discards int
	closed            bool
	diagnostic        openaicookies.Diagnostic
	snapshotErr       error
	snapshotExpiresAt time.Time
}

func newCodexCookieCandidateTest() *codexCookieCandidateTest {
	return &codexCookieCandidateTest{diagnostic: openaicookies.Diagnostic{Reason: "cookie_staged", Names: []string{"session"}}}
}

func (c *codexCookieCandidateTest) Snapshot(expiresAt time.Time) (openaicookies.Bundle, error) {
	if c.closed {
		return openaicookies.Bundle{}, openaicookies.ErrAttemptClosed
	}
	c.snapshotExpiresAt = expiresAt
	if c.snapshotErr != nil {
		return openaicookies.Bundle{}, c.snapshotErr
	}
	return openaicookies.Bundle{Entries: []openaicookies.Entry{}, ExpiresAt: expiresAt}, nil
}

func (c *codexCookieCandidateTest) MarkPublished() {
	if !c.closed {
		c.commits++
		c.closed = true
		c.diagnostic.Reason, c.diagnostic.SavedCount = "cookie_bundle_saved", 1
	}
}
func (c *codexCookieCandidateTest) Discard() {
	if !c.closed {
		c.discards++
		c.closed = true
		c.diagnostic.Reason = "cookie_target_rejected"
	}
}
func (c *codexCookieCandidateTest) Diagnostic() openaicookies.Diagnostic { return c.diagnostic }

type codexCookieCompletionTestCollector func(context.Context, CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error)

func (f codexCookieCompletionTestCollector) Collect(ctx context.Context, request CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
	return f(ctx, request)
}

func TestCodexTurnStateCookieHTTPRequiresDeliveredCompletedTarget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		mode := "passive"
		if enabled {
			mode = "cache_enabled"
		}
		for _, name := range []string{"header_target", "metadata_target", "nonstream_target", "business_model_mismatch", "model_excluded", "maintenance_unavailable", "extended", "wrong_package", "invalid", "expired", "expires_before_delivery", "missing", "no_terminal", "failed_then_completed", "incomplete_then_completed", "eof", "timeout", "undelivered", "stale_credentials"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				isolateCodexHistory(t)
				isolateCodexTurnStateSummaryStore(t)
				state, _, account := newCodexStateTestService(t)
				account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = enabled
				gateway := &OpenAIGatewayService{codexTurnStateService: state}
				request := codexStateHTTPRequest(t, `{"model":"gpt-5","input":"hello"}`)
				if name == "model_excluded" {
					request = codexStateHTTPRequest(t, `{"model":"excluded-model","input":"hello"}`)
				}
				if name == "maintenance_unavailable" {
					state.repo = nil
				}
				if name == "stale_credentials" {
					request.Header.Set("Authorization", "Bearer stale")
				}
				request = gateway.prepareOpenAICodexStateHTTPRequest(nil, account, request)
				collector, _ := request.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
				require.NotNil(t, collector)
				cookies := newCodexCookieCandidateTest()
				wantCandidate := enabled && name != "model_excluded" && name != "maintenance_unavailable" && name != "stale_credentials"
				if wantCandidate {
					require.NotNil(t, collector.attempt.cookieAttempt, "enabled physical request has its own staged-cookie boundary")
					collector.attempt.cookieAttempt = cookies
				} else {
					require.Nil(t, collector.attempt.cookieAttempt, "passive requests cannot retain cookie candidates")
				}
				token := codexStateTestToken(10, state.now())
				switch name {
				case "extended":
					token = codexStateTestToken(11, state.now())
				case "wrong_package":
					token = codexStateTestToken(12, state.now())
				case "invalid":
					token = "invalid"
				case "expired":
					token = codexStateTestToken(10, state.now().Add(-CodexTurnStateLifetime))
				case "missing":
					token = ""
				}
				response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
				if name != "metadata_target" {
					response.Header.Set(openAICodexTurnStateHeader, token)
				}
				observeCodexTurnStateHTTPResponse(request, response, nil)
				beginCodexTelemetryHTTPParsing(response)
				if name == "metadata_target" {
					payload, err := json.Marshal(map[string]any{"headers": map[string]string{openAICodexTurnStateHeader: token}})
					require.NoError(t, err)
					observeCodexTelemetryHTTPPayload(response, payload, "response.metadata")
				}
				if name == "failed_then_completed" {
					observeCodexTelemetryHTTPPayload(response, []byte(`{"error":{"code":"server_error"}}`), "response.failed")
				}
				if name == "incomplete_then_completed" {
					observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.incomplete"}`), "")
				}
				switch name {
				case "no_terminal":
					observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.created","response":{"status":"in_progress"}}`), "")
				case "nonstream_target":
					observeCodexTelemetryHTTPPayload(response, []byte(`{"object":"response","status":"completed","model":"gpt-5"}`), "")
				case "business_model_mismatch":
					observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.completed","response":{"model":"gpt-5.6-luna"}}`), "")
				default:
					observeCodexTelemetryHTTPPayload(response, []byte(`{"response":{"model":"gpt-5"}}`), "response.completed")
				}
				if name != "undelivered" {
					markCodexTurnStateHTTPDelivered(response)
				}
				var parseErr error
				if name == "eof" {
					parseErr = io.ErrUnexpectedEOF
				}
				if name == "timeout" {
					parseErr = context.DeadlineExceeded
				}
				if name == "expires_before_delivery" {
					later := state.now().Add(CodexTurnStateLifetime + time.Second)
					state.now = func() time.Time { return later }
				}
				completeCodexTelemetryHTTPResponse(response, parseErr)
				require.NoError(t, response.Body.Close())
				wantCommit := wantCandidate && (name == "header_target" || name == "metadata_target" || name == "nonstream_target" || name == "business_model_mismatch")
				if wantCommit {
					require.Equal(t, 1, cookies.commits)
				} else {
					require.Zero(t, cookies.commits)
				}
				if !enabled {
					require.Empty(t, collector.attempt.candidates, "passive cookie qualification must not retain token values")
				}
				wantReason := ""
				if wantCandidate {
					wantReason = "cookie_target_rejected"
				}
				if wantCommit {
					wantReason = "cookie_bundle_saved"
				}
				diagnostic := collector.attempt.SafeObservation().CookieDiagnostic
				if wantCandidate {
					require.NotNil(t, diagnostic)
					require.Equal(t, wantReason, diagnostic.Reason)
				} else {
					require.Nil(t, diagnostic, "passive requests have no cookie publication diagnostics")
				}
				completeCodexTelemetryHTTPResponse(response, nil)
				if wantCandidate {
					require.Equal(t, 1, cookies.commits+cookies.discards, "late completion cannot re-admit rejected cookies")
				} else {
					require.Zero(t, cookies.commits+cookies.discards, "passive requests never create or publish a cookie candidate")
				}
			})
		}
	}
}

func TestCodexTurnStateCookieBusinessRequiresCacheAdmissionButAcceptsSameExpiringTarget(t *testing.T) {
	for _, name := range []string{"same_expiring", "later_cache_wins", "store_failure", "snapshot_failure"} {
		t.Run(name, func(t *testing.T) {
			isolateCodexHistory(t)
			isolateCodexTurnStateSummaryStore(t)
			state, repo, account := newCodexStateTestService(t)
			issued := state.now().Add(-CodexTurnStateLifetime + 15*time.Second)
			token := codexStateTestToken(10, issued)
			seed, err := state.Prepare(context.Background(), account, "gpt-5")
			require.NoError(t, err)
			state.Observe(seed, token)
			require.NoError(t, state.Finish(context.Background(), seed, true))
			gateway := &OpenAIGatewayService{codexTurnStateService: state}
			request := gateway.prepareOpenAICodexStateHTTPRequest(nil, account, codexStateHTTPRequest(t, `{"model":"gpt-5","input":"hello"}`))
			collector := request.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
			cookies := newCodexCookieCandidateTest()
			if name == "snapshot_failure" {
				cookies.snapshotErr = openaicookies.ErrNoSnapshot
			}
			collector.attempt.cookieAttempt = cookies
			response := &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {token}}, Body: io.NopCloser(strings.NewReader(""))}
			observeCodexTurnStateHTTPResponse(request, response, nil)
			beginCodexTelemetryHTTPParsing(response)
			observeCodexTelemetryHTTPPayload(response, []byte(`{"type":"response.completed"}`), "")
			markCodexTurnStateHTTPDelivered(response)
			if name == "later_cache_wins" {
				newer, err := state.Prepare(context.Background(), account, "gpt-5")
				require.NoError(t, err)
				state.Observe(newer, codexStateTestToken(10, state.now()))
				require.NoError(t, state.Finish(context.Background(), newer, true))
			}
			if name == "store_failure" {
				repo.getErr = errors.New("local store unavailable")
			}
			before := repo.records[collector.attempt.key]
			completeCodexTelemetryHTTPResponse(response, nil)
			require.NoError(t, response.Body.Close())
			after := repo.records[collector.attempt.key]
			if name == "same_expiring" {
				require.Equal(t, 1, cookies.commits)
				require.NotEmpty(t, after.EncryptedCookieBundle)
				require.Equal(t, before.EncryptedToken, after.EncryptedToken)
				require.Equal(t, before.IssuedAt, after.IssuedAt)
				require.Equal(t, before.ExpiresAt, after.ExpiresAt, "same target never renews ticket lifetime")
				require.Equal(t, before.ExpiresAt, cookies.snapshotExpiresAt)
				require.Equal(t, before.ExpiresAt, *after.CookieBundleExpiresAt)
			} else {
				require.Zero(t, cookies.commits)
				require.Equal(t, before.cacheIdentity(), after.cacheIdentity(), "rejected candidate cannot replace either half of the cached pair")
			}
		})
	}
}

func TestCodexTurnStateCollectorCookieRequiresAcceptedTargetCAS(t *testing.T) {
	for _, name := range []string{"target", "expiring_target", "same_expiring_target", "extended", "missing", "model_mismatch", "failed", "incomplete", "late_attempt"} {
		t.Run(name, func(t *testing.T) {
			isolateCodexHistory(t)
			isolateCodexTurnStateSummaryStore(t)
			state, repo, account, now := newCodexRotationTestService(t, "personal", 11)
			key := seedCodexRotationTestDemand(t, state, account, "gpt-5", 11)
			cookies := newCodexCookieCandidateTest()
			issued := *now
			if name == "expiring_target" || name == "same_expiring_target" {
				issued = now.Add(-CodexTurnStateLifetime + 15*time.Second)
			}
			token := codexStateTestToken(10, issued)
			if name == "same_expiring_target" || name == "expiring_target" {
				record := repo.records[key]
				record.IssuedAt = issued.Add(-time.Second)
				if name == "same_expiring_target" {
					record.IssuedAt = issued
					record.EncryptedToken, _ = state.encryptor.Encrypt(token)
					record.ExpiresAt = issued.Add(CodexTurnStateLifetime)
					bindCodexStateTestBundle(t, state, account, &record, CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: 11, ProxyRouteGeneration: 1})
				}
				repo.records[key] = record
			}
			state.collector = codexCookieCompletionTestCollector(func(_ context.Context, input CodexTurnStateCollectRequest) (CodexTurnStateCollectResult, error) {
				result := CodexTurnStateCollectResult{StatusCode: 200, completed: true, Tokens: []string{token}, cookieAttempt: cookies,
					BundleBinding: CodexTurnStateBundleBinding{WireMode: "lite", EgressKind: "proxy", ProxyID: input.ProxyID, ProxyRouteGeneration: 1},
					ModelEvidence: CodexModelEvidence{UpstreamResponseModel: "gpt-5", ModelRelation: "exact", ModelEvidenceSource: "response.model", HeaderEvidenceScope: "response", SafetyBufferingFasterModel: "gpt-5-mini"}}
				switch name {
				case "extended":
					result.Tokens = []string{codexStateTestToken(11, *now)}
				case "missing":
					result.Tokens = nil
				case "model_mismatch":
					result.ModelEvidence.ModelRelation = "different"
				case "failed":
					return result, errors.New("stream failed")
				case "incomplete":
					result.completed = false
				case "late_attempt":
					record := repo.records[key]
					record.CollectorAttemptID = "replacement-attempt"
					repo.records[key] = record
				}
				return result, nil
			})
			state.collect(context.Background(), key)
			if name == "target" || name == "expiring_target" || name == "same_expiring_target" {
				require.Equal(t, 1, cookies.commits)
				evidence := decryptCodexCookieAtomicTestEnvelope(t, state, repo.records[key]).ResponseEvidence
				require.Equal(t, "gpt-5", evidence.UpstreamResponseModel)
				require.Equal(t, "exact", evidence.ModelRelation)
				require.Equal(t, "response.model", evidence.ModelEvidenceSource)
				require.Equal(t, "response", evidence.HeaderEvidenceScope)
				require.Equal(t, "gpt-5-mini", evidence.SafetyBufferingFasterModel)
			} else {
				require.Zero(t, cookies.commits)
			}
			require.True(t, cookies.closed)
		})
	}
}

type codexCookieGatewayUpstream struct {
	*httpUpstreamRecorder
	t       *testing.T
	cookies *codexCookieCandidateTest
	enabled bool
}

func (u *codexCookieGatewayUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	collector, _ := req.Context().Value(codexTurnStateHTTPRequestKey{}).(*codexTurnStateHTTPCollector)
	require.NotNil(u.t, collector, "all Responses transport paths must prepare a physical attempt")
	if u.enabled {
		require.NotNil(u.t, collector.attempt.cookieAttempt)
		collector.attempt.cookieAttempt = u.cookies
	} else {
		require.Nil(u.t, collector.attempt.cookieAttempt)
	}
	return u.httpUpstreamRecorder.Do(req, proxy, id, concurrency)
}

func (u *codexCookieGatewayUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestCodexTurnStateCookieGatewayAllHTTPEntryPaths(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				for _, failed := range []bool{false, true} {
					name := path + "/stream=" + strconv.FormatBool(stream) + "/cache=" + strconv.FormatBool(enabled) + "/failed=" + strconv.FormatBool(failed)
					t.Run(name, func(t *testing.T) {
						isolateCodexHistory(t)
						isolateCodexTurnStateSummaryStore(t)
						state, _, account := newCodexStateTestService(t)
						account.Concurrency = 1
						account.Extra[CodexTurnStateExtraKey].(map[string]any)["enabled"] = enabled
						if path == "passthrough" {
							account.Extra["openai_passthrough"] = true
						}
						cookies := newCodexCookieCandidateTest()
						upstream := &codexCookieGatewayUpstream{t: t, cookies: cookies, enabled: enabled, httpUpstreamRecorder: &httpUpstreamRecorder{
							resp: codexStateHTTPIntegrationResponse(codexStateTestToken(10, state.now()), "header", failed),
						}}
						gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, codexTurnStateService: state}
						_, recorder, err := codexStateHTTPIntegrationForward(t, gateway, account, path, codexStateHTTPIntegrationBody(path, stream))
						if !failed {
							require.NoError(t, err, recorder.Body.String())
						}
						if enabled && !failed {
							require.Equal(t, 1, cookies.commits)
						} else {
							require.Zero(t, cookies.commits)
						}
						require.Equal(t, enabled, cookies.closed)
					})
				}
			}
		}
	}
}
