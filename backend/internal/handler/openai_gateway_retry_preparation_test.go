package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func retryPreparationEngineAccount() *service.Account {
	return &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Extra: map[string]any{service.OpenAIAPIKeyModeExtraKey: "codex_engine"}}
}

// This is the pre-cache handler sequence, retained as a behavioral and benchmark
// reference. Account-specific forwarding is intentionally outside both versions.
func uncachedOpenAIResponsesPreparation(h *OpenAIGatewayHandler, c *gin.Context, logger *zap.Logger, body []byte) ([]byte, bool) {
	body, valid := h.normalizeOpenAIResponsesCompactRequest(c, logger, body)
	if !valid {
		return nil, false
	}
	body, _ = normalizeCodexPageContext(body)
	body, _ = normalizeCodexAutomationBootstrap(body)
	body, _ = normalizeCodexDelegationBootstrap(body)
	return body, h.validateFunctionCallOutputRequest(c, body, logger)
}

func TestOpenAIResponsesRetryPreparationMixedEngine(t *testing.T) {
	h, logger := &OpenAIGatewayHandler{}, zap.NewNop()
	engine := retryPreparationEngineAccount()
	oauth := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	for _, fixture := range []struct{ name, body string }{
		{"page", `{"model":"gpt-6-astra","stream":false,"unknown":9007199254740993123,"input":[{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":null}"},{"role":"user","content":"hello"}]}`},
		{"compact", `{"model":"gpt-6-astra","stream":false,"unknown":9007199254740993123,"prompt_cache_key":"original-seed","input":[{"role":"user","content":"hello"},{"type":"compaction_trigger"}]}`},
		{"native_v2", `{"model":"gpt-6-astra","stream":true,"input":[{"type":"compaction_trigger"},{"role":"user","content":"hello"}]}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			original := []byte(fixture.body)
			c := newCompactBodySignalTestContext(t, "/v1/responses", original)
			p := openAIResponsesRetryPreparation{body: original, path: c.Request.URL.Path}
			got, valid := p.forAccount(h, c, logger, engine)
			require.True(t, valid)
			require.Equal(t, original, got)
			require.False(t, p.prepared, "Engine must not trigger compatibility parsing")

			reference := newCompactBodySignalTestContext(t, "/v1/responses", original)
			want, valid := uncachedOpenAIResponsesPreparation(h, reference, logger, original)
			require.True(t, valid)
			got, valid = p.forAccount(h, c, logger, oauth)
			require.True(t, valid)
			require.JSONEq(t, string(want), string(got))
			require.Equal(t, reference.Request.URL.Path, c.Request.URL.Path)
			require.Equal(t, openAICompactionRouteFromContext(reference), openAICompactionRouteFromContext(c))
			firstNormalized := got

			// A later Engine attempt must retain every original field and route,
			// followed by another non-Engine attempt with the cached compact route.
			c.Request.URL.Path = "/changed-by-forward"
			got, valid = p.forAccount(h, c, logger, engine)
			require.True(t, valid)
			require.Equal(t, fixture.body, string(got))
			require.Equal(t, "/v1/responses", c.Request.URL.Path)
			for range 15 {
				c.Request.URL.Path = "/changed-by-forward"
				got, valid = p.forAccount(h, c, logger, oauth)
				require.True(t, valid)
				require.Equal(t, reference.Request.URL.Path, c.Request.URL.Path)
				require.Equal(t, firstNormalized, got)
				require.True(t, &firstNormalized[0] == &got[0], "retry should borrow immutable normalized bytes")
			}
			require.Equal(t, fixture.body, string(original))
			require.Equal(t, service.IsOpenAINativeCompactionV2(reference), service.IsOpenAINativeCompactionV2(c))
			if fixture.name == "compact" {
				require.Equal(t, "original-seed", c.GetString(service.OpenAICompactSessionSeedKeyForTest()))
			}
		})
	}
}

func TestOpenAIResponsesRetryPreparationRejectsInvalidNonEngine(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-6-astra","input":[{"type":"function_call_output","call_id":"","output":"orphan"}]}`,
		`{"model":"gpt-6-astra","stream":"true","input":[]}`,
		`{"model":`,
	} {
		t.Run(body, func(t *testing.T) {
			c := newCompactBodySignalTestContext(t, "/v1/responses", []byte(body))
			p := openAIResponsesRetryPreparation{body: []byte(body), path: "/v1/responses"}
			h := &OpenAIGatewayHandler{}
			// Engine's own validation is not replaced by the compatibility check.
			_, valid := p.forAccount(h, c, zap.NewNop(), retryPreparationEngineAccount())
			require.True(t, valid)
			require.False(t, c.Writer.Written())
			_, valid = p.forAccount(h, c, zap.NewNop(), newOpenAIPassthroughAccount(1, false))
			require.False(t, valid)
			require.Equal(t, http.StatusBadRequest, c.Writer.Status())
		})
	}
}

func TestOpenAIResponsesRetryPreparationPreservesPerAccountDerivation(t *testing.T) {
	h, logger := &OpenAIGatewayHandler{}, zap.NewNop()
	original := []byte(kiroReasoningCanonicalBody)
	c := newCompactBodySignalTestContext(t, "/v1/responses", original)
	p := openAIResponsesRetryPreparation{body: original, path: "/v1/responses"}
	state := openAIPassthroughFailoverState{}
	for i, passthrough := range []bool{true, false, false, true} {
		account := newOpenAIPassthroughAccount(int64(i+1), passthrough)
		canonical, valid := p.forAccount(h, c, logger, account)
		require.True(t, valid)
		actual := h.deriveOpenAIForwardAttemptBody(logger, canonical, account, &state)
		require.Equal(t, passthrough, gjson.GetBytes(actual, "input.#(type==\"reasoning\")").Exists())
		require.Equal(t, 1, reasoningItemCount(t, canonical), "account-derived changes must not poison cached input")
	}
}

func TestOpenAIUsagePayloadHashUsesOriginalBodyOnce(t *testing.T) {
	original := []byte(`{"model":"public","input":"hello"}`)
	h := openAIUsagePayloadHash{body: original}
	require.False(t, h.calculated, "unsuccessful retries without policy markers should not hash")
	want := service.HashUsageRequestPayload(original)
	require.Equal(t, want, h.value())
	require.True(t, h.calculated)
	// A settled hash has independent ownership and cannot become an attempt's
	// mapped/normalized body hash on a later billing or policy read.
	h.body = []byte(`{"model":"upstream","input":"hello"}`)
	require.Equal(t, want, h.value())
	for _, body := range [][]byte{nil, []byte{}} {
		empty := openAIUsagePayloadHash{body: body}
		require.Equal(t, service.HashUsageRequestPayload(body), empty.value())
		require.True(t, empty.calculated)
	}
}

var benchmarkOpenAIRetryHash string

func BenchmarkOpenAIResponsesRetryPreparation(b *testing.B) {
	gin.SetMode(gin.TestMode)
	h, logger := &OpenAIGatewayHandler{}, zap.NewNop()
	account := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		body := []byte(`{"model":"gpt-6-astra","stream":true,"input":[{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","output":"{\"page_id\":null}"},{"role":"user","content":"` + strings.Repeat("x", size) + `"}]}`)
		for _, attempts := range []int{1, 5, 15} {
			for _, cached := range []bool{false, true} {
				label := "before"
				if cached {
					label = "after"
				}
				b.Run(fmt.Sprintf("%dKiB/%dattempts/%s", size>>10, attempts, label), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					for b.Loop() {
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
						p := openAIResponsesRetryPreparation{body: body, path: "/v1/responses"}
						hash := openAIUsagePayloadHash{body: body}
						for range attempts {
							var valid bool
							if cached {
								_, valid = p.forAccount(h, c, logger, account)
							} else {
								c.Request.URL.Path = "/v1/responses"
								_, valid = uncachedOpenAIResponsesPreparation(h, c, logger, body)
								benchmarkOpenAIRetryHash = service.HashUsageRequestPayload(body)
							}
							if !valid {
								b.Fatal("valid fixture rejected")
							}
						}
						if cached {
							benchmarkOpenAIRetryHash = hash.value()
						} else {
							benchmarkOpenAIRetryHash = service.HashUsageRequestPayload(body)
						}
					}
				})
			}
		}
	}
}
