package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const billingTestInstance = "cei_0123456789abcdef0123456789abcdef"

var billingTestReceipt = "ceu_" + strings.Repeat("a", 43)

func billingTestEvent(id string, sequence int) map[string]any {
	return map[string]any{"event_id": id, "sequence": sequence, "kind": "model", "purpose": "response", "model": "gpt-6-astra", "service_tier": "standard", "occurred_at": "2026-11-06T01:02:03Z", "usage_complete": true, "applicable_usage": []string{"input_tokens", "cached_input_tokens", "output_tokens"}, "usage": map[string]any{"input_tokens": 120, "cached_input_tokens": 80, "output_tokens": 20}}
}

func billingTestPage(events []map[string]any, finalized bool) map[string]any {
	n := len(events)
	groups := []any{}
	if n > 0 {
		groups = append(groups, map[string]any{"kind": "model", "purpose": "response", "model": "gpt-6-astra", "service_tier": "standard", "usage": map[string]any{"input_tokens": 120 * n, "cached_input_tokens": 80 * n, "output_tokens": 20 * n}})
	}
	p := map[string]any{"object": "codex_engine.billing", "contract": CodexEngineBillingContract, "instance_id": billingTestInstance, "receipt_id": billingTestReceipt, "client_request_id": "client-1", "revision": 1, "execution_status": "canceled", "finalized": finalized, "usage_complete": true, "last_sequence": n, "page_through_sequence": n, "events": events, "summary": map[string]any{"as_of_sequence": n, "groups": groups}, "pending_usage": map[string]any{"open_sources": 1, "gaps": 0}, "seal": nil, "has_more": false, "next_cursor": "opaque-checkpoint", "poll_after_seconds": 5}
	if finalized {
		p["pending_usage"] = map[string]any{"open_sources": 0, "gaps": 0}
		p["seal"] = map[string]any{"last_sequence": n, "sealed_at": "2026-11-06T01:03:03Z", "reason": "complete"}
	}
	return p
}
func billingJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return b
}

func TestCodexEngineBillingPageSeparatesEventAndContextUsage(t *testing.T) {
	w := billingTestPage([]map[string]any{billingTestEvent("event1", 1), billingTestEvent("event2", 2)}, true)
	w["usage"] = map[string]any{"input_tokens": 99999999, "output_tokens": 99999999}
	p, err := ParseCodexEngineBillingPage(billingJSON(t, w))
	require.NoError(t, err)
	require.True(t, p.Finalized)
	require.Len(t, p.Events, 2)
	require.Equal(t, 120, p.Events[0].Usage.InputTokens)
	require.Equal(t, 80, p.Events[0].Usage.CacheReadInputTokens)
	require.False(t, p.Events[0].UsagePresent["image_input_tokens"])
	require.False(t, p.Events[0].ApplicableUsage["image_input_tokens"])
	require.Equal(t, int64(240), p.Summary[0].Usage["input_tokens"])
	require.Len(t, p.Events[0].Fingerprint, 64)
}

func TestCodexEngineBillingPageRejectsInvalidContract(t *testing.T) {
	tests := map[string]func(map[string]any){
		"contract absent":      func(p map[string]any) { delete(p, "contract") },
		"version not guessed":  func(p map[string]any) { p["contract"] = "v1" },
		"instance invalid":     func(p map[string]any) { p["instance_id"] = "url-derived" },
		"receipt absent":       func(p map[string]any) { delete(p, "receipt_id") },
		"seal missing":         func(p map[string]any) { p["seal"] = nil },
		"empty cursor":         func(p map[string]any) { p["next_cursor"] = "" },
		"nonintegral sequence": func(p map[string]any) { p["last_sequence"] = 1.5 },
		"duplicate event":      func(p map[string]any) { p["events"] = []any{billingTestEvent("same", 1), billingTestEvent("same", 1)} },
		"negative usage": func(p map[string]any) {
			p["events"].([]map[string]any)[0]["usage"].(map[string]any)["input_tokens"] = -1
		},
		"null usage": func(p map[string]any) {
			p["events"].([]map[string]any)[0]["usage"].(map[string]any)["input_tokens"] = nil
		},
		"fraction usage": func(p map[string]any) {
			p["events"].([]map[string]any)[0]["usage"].(map[string]any)["input_tokens"] = 1.5
		},
		"overflow usage": func(p map[string]any) {
			p["events"].([]map[string]any)[0]["usage"].(map[string]any)["input_tokens"] = json.Number("9223372036854775808")
		},
		"inconsistent subset": func(p map[string]any) {
			p["events"].([]map[string]any)[0]["usage"].(map[string]any)["cached_input_tokens"] = 121
		},
		"missing applicable": func(p map[string]any) { delete(p["events"].([]map[string]any)[0], "applicable_usage") },
		"unknown applicable": func(p map[string]any) { p["events"].([]map[string]any)[0]["applicable_usage"] = []string{"bogus"} },
		"missing complete value": func(p map[string]any) {
			delete(p["events"].([]map[string]any)[0]["usage"].(map[string]any), "cached_input_tokens")
		},
		"summary null counter": func(p map[string]any) {
			p["summary"].(map[string]any)["groups"].([]any)[0].(map[string]any)["usage"].(map[string]any)["input_tokens"] = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := billingTestPage([]map[string]any{billingTestEvent("one", 1)}, true)
			mutate(p)
			_, err := ParseCodexEngineBillingPage(billingJSON(t, p))
			require.ErrorIs(t, err, ErrCodexEngineBillingProtocol)
		})
	}
	valid := billingJSON(t, billingTestPage([]map[string]any{billingTestEvent("one", 1)}, true))
	for _, raw := range [][]byte{append(valid, []byte(`{}`)...), []byte(strings.Replace(string(valid), `"revision":1`, `"revision":1,"revision":2`, 1)), []byte(`{"usage":{"input_tokens":1,"output_tokens":2}}`)} {
		_, err := ParseCodexEngineBillingPage(raw)
		require.ErrorIs(t, err, ErrCodexEngineBillingProtocol)
	}
}

func TestCodexEngineBillingSourceClosedWithPermanentGap(t *testing.T) {
	w := billingTestPage([]map[string]any{billingTestEvent("one", 1)}, true)
	w["usage_complete"] = false
	w["pending_usage"] = map[string]any{"open_sources": 0, "gaps": 1}
	w["seal"].(map[string]any)["reason"] = "source_closed_with_gaps"
	p, err := ParseCodexEngineBillingPage(billingJSON(t, w))
	require.NoError(t, err)
	require.True(t, p.Finalized)
	require.False(t, p.UsageComplete)
	require.Len(t, p.Events, 1)
	// Execution cancellation without the authoritative seal remains pollable.
	w["finalized"] = false
	w["seal"] = nil
	p, err = ParseCodexEngineBillingPage(billingJSON(t, w))
	require.NoError(t, err)
	require.False(t, p.Finalized)
}

func TestCodexEngineBillingSearchAndImageFacts(t *testing.T) {
	search := billingTestEvent("search", 1)
	search["kind"] = "search"
	search["purpose"] = "search"
	search["usage"] = map[string]any{"search_operations": 1}
	search["applicable_usage"] = []string{"search_operations"}
	e, err := parseCodexEngineBillingEvent(billingJSON(t, search))
	require.NoError(t, err)
	require.Equal(t, 1, e.SearchOperations)
	require.Zero(t, e.SearchCalls)
	image := billingTestEvent("image", 2)
	image["kind"] = "image"
	image["purpose"] = "image"
	image["model"] = "gpt-image-2.6-sunburst"
	image["usage"] = map[string]any{"images": 1}
	image["applicable_usage"] = []string{"images"}
	image["image"] = map[string]any{"actual": map[string]any{"count": 1, "output_format": "gif"}, "requested": map[string]any{"n": 1, "size": "1024x1024", "frame_count": 16}}
	e, err = parseCodexEngineBillingEvent(billingJSON(t, image))
	require.NoError(t, err)
	require.Equal(t, 1, e.ImageCount)
	require.Empty(t, e.ImageSize)
	require.Equal(t, "1024x1024", e.ImageRequested.Size)
	partial := billingTestEvent("partial", 3)
	partial["usage_complete"] = false
	delete(partial["usage"].(map[string]any), "cached_input_tokens")
	e, err = parseCodexEngineBillingEvent(billingJSON(t, partial))
	require.NoError(t, err)
	require.True(t, e.ApplicableUsage["cached_input_tokens"])
	require.False(t, e.UsagePresent["cached_input_tokens"])
}

func TestCodexEngineBillingPhysicalHeaderIdentity(t *testing.T) {
	h := http.Header{}
	h.Set(CodexEngineBillingContractHeader, CodexEngineBillingContract)
	h.Set(CodexEngineBillingInstanceHeader, billingTestInstance)
	require.NoError(t, ValidateCodexEngineBillingHeaders(h, billingTestInstance))
	h.Add(CodexEngineBillingInstanceHeader, billingTestInstance)
	require.Error(t, ValidateCodexEngineBillingHeaders(h, billingTestInstance))
}

func TestCodexEngineBillingEventFingerprintUsesOnlyImmutableFacts(t *testing.T) {
	event := billingTestEvent("one", 1)
	a, err := parseCodexEngineBillingEvent(billingJSON(t, event))
	require.NoError(t, err)
	event["diagnostic"] = map[string]any{"observed_at": "later"}
	event["applicable_usage"] = []string{"cached_input_tokens", "output_tokens", "input_tokens"}
	b, err := parseCodexEngineBillingEvent(billingJSON(t, event))
	require.NoError(t, err)
	require.Equal(t, a.Fingerprint, b.Fingerprint)
	event["usage"].(map[string]any)["output_tokens"] = 21
	c, err := parseCodexEngineBillingEvent(billingJSON(t, event))
	require.NoError(t, err)
	require.NotEqual(t, a.Fingerprint, c.Fingerprint)
}
