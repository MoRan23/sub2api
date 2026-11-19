package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIEffortConfigurationLastWins(t *testing.T) {
	body := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"max"},"input":[{"type":"configuration_update","reasoning":{"effort":"disabled"}},{"type":"message","content":"same"},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
	got, changed, err := ApplyOpenAIReasoningEffortPolicy(body, "medium", []ReasoningEffortMapping{{From: "disabled", To: "deny", Model: "gpt-6.2-sol"}}, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "max", gjson.GetBytes(got, "reasoning.effort").Str)
	require.Equal(t, "disabled", gjson.GetBytes(got, "input.0.reasoning.effort").Str)
	require.Equal(t, "medium", gjson.GetBytes(got, "input.2.reasoning.effort").Str)
	require.Equal(t, "medium", *CanonicalRequestedReasoningEffort(got))
	escaped := []byte(`{"reasoning":{"effort":"low"},"input":[{"type":"\u0063onfiguration_update","reasoning":{"effort":"high"}}]}`)
	require.Equal(t, "high", selectOpenAIEffort(escaped, nil).value.Value)
}

func TestOpenAIEffortTypedValuesAndOpaqueCap(t *testing.T) {
	for _, literal := range []string{`18446744073709551615`, `0`, `"123"`, `"disabled"`, `"Custom-Mode"`} {
		t.Run(literal, func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":` + literal + `}}`)
			got, changed, err := ApplyOpenAIReasoningEffortPolicy(body, "", nil, "")
			require.NoError(t, err)
			require.False(t, changed)
			require.Same(t, &body[0], &got[0])
			_, _, err = ApplyOpenAIReasoningEffortPolicy(body, "high", nil, "")
			require.Error(t, err)
			require.True(t, IsReasoningEffortPolicyDenied(err))
		})
	}
	mappings := []ReasoningEffortMapping{{From: "123", FromType: "number", To: "medium", Model: "gpt-6.2-sol"}, {From: "123", To: "low", Model: "gpt-6.2-sol"}}
	for _, tt := range []struct{ value, want string }{{`123`, "medium"}, {`"123"`, "low"}} {
		got, _, err := ApplyOpenAIReasoningEffortPolicy([]byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":`+tt.value+`}}`), "high", mappings, "")
		require.NoError(t, err)
		require.Equal(t, tt.want, gjson.GetBytes(got, "reasoning.effort").Str)
	}
}

func TestOpenAIEffortMappingSourceValidation(t *testing.T) {
	for _, source := range []string{"-1", "1.5", "1e2", "18446744073709551616", "+1"} {
		_, err := NormalizeReasoningEffortMappings(PlatformOpenAI, []ReasoningEffortMapping{{From: source, FromType: "number", To: "low", Model: "gpt"}})
		require.Error(t, err)
	}
	values, err := NormalizeReasoningEffortMappings(PlatformOpenAI, []ReasoningEffortMapping{{From: "00123", FromType: "number", To: "low", Model: "gpt"}, {From: "Custom-Mode", To: "medium", Model: "gpt"}})
	require.NoError(t, err)
	require.Equal(t, "123", values[0].From)
	require.Equal(t, "Custom-Mode", values[1].From)
	_, err = NormalizeReasoningEffortMappings(PlatformOpenAI, []ReasoningEffortMapping{{From: "disabled", To: "low"}})
	require.ErrorContains(t, err, "scope")
}

func TestOpenAIEffortOversizeAndRetainedContext(t *testing.T) {
	body := []byte(`{"reasoning":{"effort":"` + strings.Repeat("x", 65) + `"}}`)
	_, _, err := ApplyOpenAIReasoningEffortPolicy(body, "", nil, "")
	require.ErrorContains(t, err, "64-byte")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	s := &OpenAIGatewayService{}
	first := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"high"}}`)
	ctx, _, err := s.prepareOpenAIWSEffort(context.Background(), c, first, nil, &OpenAIWSIngressHooks{})
	require.NoError(t, err)
	second := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"low"}}`)
	next, _, err := s.prepareOpenAIWSEffort(ctx, c, second, nil, &OpenAIWSIngressHooks{})
	require.NoError(t, err)
	require.Equal(t, "high", *EffectiveOpenAIReasoningEffort(ctx))
	require.Equal(t, "low", *EffectiveOpenAIReasoningEffort(next))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if *EffectiveOpenAIReasoningEffort(ctx) != "high" {
					t.Error("prior turn mutated")
				}
			}
		}()
	}
	CaptureFinalOpenAIReasoningEffort(next, []byte(`{"reasoning":{"effort":"medium"}}`))
	wg.Wait()
}

func TestOpenAIEffortImplicitModelSuffix(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	s := &OpenAIGatewayService{}
	ctx, _, err := s.prepareOpenAIWSEffort(context.Background(), c, []byte(`{"model":"gpt-5.4-xhigh","input":[]}`), nil, &OpenAIWSIngressHooks{})
	require.NoError(t, err)
	require.Equal(t, "xhigh", *effectiveOpenAIEffortForBody(ctx, []byte(`{"model":"gpt-5.4","input":[]}`), "gpt-5.4"))
}

func TestOpenAIEffortContinuationAndRetry(t *testing.T) {
	state := &openAIEffortRequestState{previousID: "resp_previous", found: true, inherited: OpenAIResponseEffortState{Override: true, Effort: OpenAIEffortValue{Type: "string", Value: "high"}}}
	ctx := context.WithValue(context.Background(), openAIEffortContextKey{}, state)
	ctx = WithOpenAIReasoningEffortPolicy(ctx, "medium", []ReasoningEffortMapping{{From: "high", To: "medium"}, {From: "medium", To: "low"}}, "")
	body := []byte(`{"previous_response_id":"resp_previous","reasoning":{"effort":"low"},"input":[]}`)
	first, _, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	require.NoError(t, err)
	require.Equal(t, "medium", gjson.GetBytes(first, "input.0.reasoning.effort").Str)
	require.Equal(t, "low", gjson.GetBytes(first, "reasoning.effort").Str)
	again, _, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(again))
	require.Equal(t, "medium", *EffectiveOpenAIReasoningEffort(ctx))
	require.True(t, state.override)
	missing := WithOpenAIReasoningEffortPolicy(context.Background(), "low", nil, "")
	_, _, err = ApplyOpenAIReasoningEffortPolicyFromContext(missing, body)
	require.ErrorContains(t, err, "resend")
	unknown := &openAIEffortRequestState{previousID: "resp_previous"}
	ctx = context.WithValue(context.Background(), openAIEffortContextKey{}, unknown)
	got, changed, err := applyEffectiveOpenAIEffortPolicy(ctx, body, "", nil, "")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, got)
	require.Nil(t, EffectiveOpenAIReasoningEffort(ctx))
	full := []byte(`{"reasoning":{"effort":"low"},"input":[]}`)
	require.Equal(t, "low", selectOpenAIEffort(full, state).value.Value)
	stringBody := []byte(`{"previous_response_id":"resp_previous","input":"Continue the report"}`)
	stringCtx := context.WithValue(context.Background(), openAIEffortContextKey{}, state)
	converted, changed, err := applyEffectiveOpenAIEffortPolicy(stringCtx, stringBody, "medium", nil, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "Continue the report", gjson.GetBytes(converted, "input.0.content").Str)
	require.Equal(t, "medium", gjson.GetBytes(converted, "input.1.reasoning.effort").Str)
}

func TestOpenAIEffortMapAndRawAgree(t *testing.T) {
	for _, literal := range []string{`18446744073709551615`, `"disabled"`, `"Custom-Mode"`} {
		body := []byte(`{"reasoning":{"effort":"high"},"input":[{"type":"configuration_update","reasoning":{"effort":` + literal + `}}]}`)
		var object map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&object))
		raw := CanonicalRequestedReasoningEffort(body)
		mapped := CanonicalRequestedReasoningEffortFromReqBody(object)
		require.NotNil(t, raw)
		require.Equal(t, raw, mapped)
	}
}

func TestOpenAIResponseEffortStateIsolationAndExpiry(t *testing.T) {
	store := NewOpenAIWSStateStore(nil).(OpenAIResponseEffortStore)
	ctx := context.Background()
	state := OpenAIResponseEffortState{AccountID: 4, Identity: "owner", Override: true, Effort: OpenAIEffortValue{Type: "number", Value: "123"}}
	require.NoError(t, store.BindResponseEffort(ctx, 1, 2, "resp_1", state, time.Hour))
	for _, scope := range [][2]int64{{2, 2}, {1, 3}} {
		_, found, err := store.GetResponseEffort(ctx, scope[0], scope[1], "resp_1")
		require.NoError(t, err)
		require.False(t, found)
	}
	got, found, err := store.GetResponseEffort(ctx, 1, 2, "resp_1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "123", got.Effort.Value)
	concrete := store.(*defaultOpenAIWSStateStore)
	concrete.responseEffortMu.Lock()
	binding := concrete.responseEfforts["1:2:resp_1"]
	binding.state.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	concrete.responseEfforts["1:2:resp_1"] = binding
	concrete.responseEffortMu.Unlock()
	_, found, err = store.GetResponseEffort(ctx, 1, 2, "resp_1")
	require.NoError(t, err)
	require.False(t, found)
	concrete.lastCleanupUnixNano.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	concrete.maybeCleanup()
	require.Empty(t, concrete.responseEfforts)
	require.NoError(t, store.BindResponseEffort(ctx, 1, 2, strings.Repeat("x", 1025), state, time.Hour))
	require.Empty(t, concrete.responseEfforts)
}

func TestOpenAIEffortPrepareVerifiesIdentityAndPreservesCapture(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	groupID := int64(1)
	c.Set("api_key", &APIKey{ID: 3, UserID: 2, GroupID: &groupID})
	s := &OpenAIGatewayService{}
	account := &Account{ID: 4, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "first", "base_url": "https://example.invalid"}}
	body := []byte(`{"model":"gpt-6.2-sol","input":[{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
	ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "medium", nil, "")
	ctx, err := s.PrepareOpenAIReasoningEffortContext(ctx, c, body, nil)
	require.NoError(t, err)
	patched, _, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	require.NoError(t, err)
	ctx, err = s.PrepareOpenAIReasoningEffortContext(ctx, c, patched, account)
	require.NoError(t, err)
	require.Equal(t, "high", *RequestedReasoningEffortFromContext(ctx))
	require.Equal(t, "medium", *EffectiveOpenAIReasoningEffort(ctx))
	require.NoError(t, s.RecordOpenAIResponseEffort(ctx, c, patched, "resp_next", account))
	next := []byte(`{"previous_response_id":"resp_next","reasoning":{"effort":"low"},"input":[]}`)
	continued, err := s.PrepareOpenAIReasoningEffortContext(context.Background(), c, next, account)
	require.NoError(t, err)
	require.Equal(t, "medium", *EffectiveOpenAIReasoningEffort(continued))
	changed := *account
	changed.Credentials = map[string]any{"api_key": "replacement", "base_url": "https://example.invalid"}
	continued, err = s.PrepareOpenAIReasoningEffortContext(continued, c, next, &changed)
	require.NoError(t, err)
	require.Nil(t, EffectiveOpenAIReasoningEffort(continued))
}

func TestOpenAIResponseEffortConcurrentReads(t *testing.T) {
	store := NewOpenAIWSStateStore(nil).(OpenAIResponseEffortStore)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 25; n++ {
				id := fmt.Sprintf("resp_%d_%d", i, n)
				state := OpenAIResponseEffortState{AccountID: 1, Override: true, Effort: OpenAIEffortValue{Type: "string", Value: "disabled"}}
				if err := store.BindResponseEffort(context.Background(), 1, 2, id, state, time.Minute); err != nil {
					t.Error(err)
				}
				_, found, err := store.GetResponseEffort(context.Background(), 1, 2, id)
				if err != nil || !found {
					t.Errorf("missing %s: %v", id, err)
				}
			}
		}(i)
	}
	wg.Wait()
}

func BenchmarkOpenAIEffortConfigurationLargeInput(b *testing.B) {
	body := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"high"},"input":[{"type":"message","content":"` + strings.Repeat("a", 1<<20) + `"},{"type":"configuration_update","reasoning":{"effort":"low"}}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, changed, err := ApplyOpenAIReasoningEffortPolicy(body, "medium", nil, "")
		if err != nil || changed {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpenAIEffortPreparedLargeInput(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		for _, configuration := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes_%d/configuration_%v", size, configuration), func(b *testing.B) {
				input := `{"type":"message","content":"` + strings.Repeat("a", size) + `"}`
				if configuration {
					input += `,{"type":"configuration_update","reasoning":{"effort":"low"}}`
				}
				body := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"low"},"input":[` + input + `]}`)
				s := &OpenAIGatewayService{}
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "medium", nil, "")
					ctx, err := s.PrepareOpenAIReasoningEffortContext(ctx, nil, body, nil)
					if err != nil {
						b.Fatal(err)
					}
					out, changed, err := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
					if err != nil || changed {
						b.Fatal(err)
					}
					CaptureFinalOpenAIReasoningEffort(ctx, out)
				}
			})
		}
	}
}

// Compare the optional byte prefilter against the original structural walk in
// the same process so machine clock/temperature does not skew the comparison.
func BenchmarkOpenAIEffortConfigurationPrefilter(b *testing.B) {
	for _, configuration := range []bool{false, true} {
		input := `{"type":"message","content":"` + strings.Repeat("a", 1<<20) + `"}`
		if configuration {
			input += `,{"type":"configuration_update","reasoning":{"effort":"low"}}`
		}
		body := []byte(`{"model":"gpt-6.2-sol","reasoning":{"effort":"low"},"input":[` + input + `]}`)
		b.Run(fmt.Sprintf("configuration_%v/alternating_pair", configuration), func(b *testing.B) {
			var structuralTime, prefilterTime time.Duration
			for i := 0; i < b.N; i++ {
				for j := 0; j < 2; j++ {
					start := time.Now()
					if (i+j)%2 == 0 {
						_ = selectOpenAIEffortStructuralBenchmark(body)
						structuralTime += time.Since(start)
					} else {
						_ = selectOpenAIEffort(body, nil)
						prefilterTime += time.Since(start)
					}
				}
			}
			b.ReportMetric(float64(structuralTime.Nanoseconds())/float64(b.N), "structural-ns/op")
			b.ReportMetric(float64(prefilterTime.Nanoseconds())/float64(b.N), "prefilter-ns/op")
		})
		for _, prefilter := range []bool{false, true} {
			b.Run(fmt.Sprintf("configuration_%v/prefilter_%v", configuration, prefilter), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for i := 0; i < b.N; i++ {
					if prefilter {
						_ = selectOpenAIEffort(body, nil)
						continue
					}
					_ = selectOpenAIEffortStructuralBenchmark(body)
				}
			})
		}
	}
}

func selectOpenAIEffortStructuralBenchmark(body []byte) openAIEffortSelection {
	selection := openAIEffortSelection{}
	index := 0
	view := parseRawJSONView(body)
	view.Get("input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").Str == "configuration_update" {
			if field := item.Get("reasoning.effort"); field.Exists() {
				value, _ := effortScalar(field)
				selection = openAIEffortSelection{value: value, path: "input." + strconv.Itoa(index) + ".reasoning.effort", override: true, unknown: !value.valid(), oversized: field.Type == gjson.String && len(field.Str) > maxReasoningEffortValueLen}
			}
		}
		index++
		return true
	})
	if selection.path != "" {
		return selection
	}
	if strings.TrimSpace(view.Get("previous_response_id").Str) != "" {
		return openAIEffortSelection{unknown: true}
	}
	for _, path := range []string{"reasoning.effort", "reasoning_effort", "output_config.effort"} {
		field := view.Get(path)
		if field.Exists() {
			value, ok := effortScalar(field)
			return openAIEffortSelection{value: value, path: path, unknown: !ok, oversized: field.Type == gjson.String && len(field.Str) > maxReasoningEffortValueLen}
		}
	}
	return selection
}
