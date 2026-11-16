package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Preserve the previous two independent root decoders as a differential oracle
// and allocation baseline. This intentionally copies every top-level value.
func legacyFingerprintObservationBodyView(body []byte) fingerprintObservationBodyView {
	view := fingerprintObservationBodyView{present: len(body) > 0}
	view.validObject = json.Unmarshal(body, &view.root) == nil && view.root != nil
	var carriers struct {
		ClientMetadata json.RawMessage `json:"client_metadata"`
		TurnMetadata   json.RawMessage `json:"x-codex-turn-metadata"`
	}
	view.validCarrier = utf8.Valid(body) && json.Unmarshal(body, &carriers) == nil
	view.clientCarrier, view.turnCarrier = carriers.ClientMetadata, carriers.TurnMetadata
	return view
}

func legacyFinalFingerprintCodexWireProfile(headers http.Header, body []byte) CodexWireProfile {
	view := legacyFingerprintObservationBodyView(body)
	return finalFingerprintCodexWireProfileFromView(headers, view, fingerprintFinalMetadataCarriersFromView(headers, view))
}

func TestFingerprintBodyProjectionMatchesLegacy(t *testing.T) {
	header := http.Header{"X-Codex-Turn-Metadata": {`{"session_id":"header-session","window_id":"header-window","request_kind":"turn"}`}}
	fixtures := []string{
		``, `null`, `{}`, `[]`, `"body"`, `1`, `{"input":]}`, `{"input":1} trailing`,
		`{"session_id":"root","client_metadata":{"session_id":"client"},"input":[{"content":"text"}]}`,
		`{"session_id":"first","session_id":null,"client_metadata":{"session_id":"old"},"client_metadata":null}`,
		`{"session_id":"first","session\u005fid":"second","client\u005fmetadata":{"session_id":"escaped"}}`,
		`{"client_metadata":{"x-codex-turn-metadata":"{\"session_id\":\"canonical\"}"},"x-codex-turn-metadata":{"session_id":"compatibility"}}`,
		`{"client_metadata":{"session_id":"exact"},"CLIENT_METADATA":{"x-codex-turn-metadata":"{\"session_id\":\"case-folded\"}"}}`,
		`{"CLIENT_METADATA":{"session_id":"ignored-flat"},"X-CODEX-TURN-METADATA":{"window_id":"folded"}}`,
		`{"client_metadata":false,"prompt_cache_key":"key","service_tier":"priority"}`,
		`{"client_metadata":{"x-codex-turn-metadata":null},"x-codex-turn-metadata":"invalid"}`,
		`{"client_metadata":{"guardian_credits_requested":true,"parent_response_id":"resp_123","mcp_attribution":"{\"status\":\"complete\"}"},"prompt_cache_key":"root","session_id":"root"}`,
		`{"session_id":"bad` + string([]byte{0xff}) + `utf8","input":"text"}`,
		`{"guardian_classifier_source_thread_id":"019589c8-7e60-7e31-884f-7d9844243ef1","context_window_id":"ctx","turn_started_at_unix_ms":123,"window_number":4}`,
		`{"prompt_cache_key":"a","prompt_cache_key":"b","input":[{"session_id":"nested-not-root"}]}`,
		" \n\t" + `{"prompt_cache_key":"a","prompt_cache_key":"b","session\u005fid":"spaced"}` + " \n",
	}
	for i, fixture := range fixtures {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			body := []byte(fixture)
			view, old := parseFingerprintObservationBodyView(body), legacyFingerprintObservationBodyView(body)
			if view.validObject != old.validObject || view.validCarrier != old.validCarrier ||
				(view.validCarrier && (!bytes.Equal(view.clientCarrier, old.clientCarrier) || !bytes.Equal(view.turnCarrier, old.turnCarrier))) {
				t.Fatalf("projection validity/carriers differ: new=%+v old=%+v", view, old)
			}
			got, want := finalFingerprintCodexWireProfile(header, body), legacyFinalFingerprintCodexWireProfile(header, body)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("profile differs: new=%+v old=%+v", got, want)
			}
			if !reflect.DeepEqual(fingerprintFinalMetadataCarriersFromView(header, view), fingerprintFinalMetadataCarriersFromView(header, old)) {
				t.Fatal("metadata carriers differ")
			}
			var actual, expected FingerprintObservationEntry
			populateFingerprintObservationBodyMetadataFromView(&actual, view, got)
			populateFingerprintObservationBodyMetadataFromView(&expected, old, want)
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("body metadata differs: new=%+v old=%+v", actual, expected)
			}
		})
	}
}

func TestFingerprintBodyProjectionFlatFields(t *testing.T) {
	// Exercise each alias independently so an accidentally omitted lower-priority
	// field cannot be hidden by a higher-priority field in the same fixture.
	for _, field := range []string{
		"x-codex-installation-id", "installation_id", "session_id", "session-id",
		"thread_id", "thread-id", "x-codex-window-id", "window_id", "window_number",
		"context_window_id", "context-window-id", "x-codex-context-window-id", "x-codex-context_window_id",
		"x-openai-subagent", "x-codex-parent-thread-id", "parent_thread_id", "turn_id", "turn-id",
		"parent_turn_id", "root_turn_id", "turn_started_at_unix_ms", "guardian_classifier_source_thread_id",
	} {
		t.Run(field, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{field: "019589c8-7e60-7e31-884f-7d9844243ef1"})
			view := parseFingerprintObservationBodyView(body)
			if _, ok := view.root[field]; !ok {
				t.Fatal("flat alias omitted")
			}
			if got, want := finalFingerprintCodexWireProfile(nil, body), legacyFinalFingerprintCodexWireProfile(nil, body); !reflect.DeepEqual(got, want) {
				t.Fatalf("alias behavior differs: new=%+v old=%+v", got, want)
			}
		})
	}
}

func TestFingerprintBodyProjectionOwnershipAndIgnoredPayload(t *testing.T) {
	body := []byte(`{"session_id":"owned-session","client_metadata":{"session_id":"owned-client","x-codex-turn-metadata":"{\"window_id\":\"owned-window\"}"},"input":"` + strings.Repeat("x", 1024*1024) + `","tools":[{"name":"large"}],"extension":{"unused":true}}`)
	view := parseFingerprintObservationBodyView(body)
	for _, field := range []string{"input", "tools", "extension"} {
		if _, exists := view.root[field]; exists {
			t.Fatalf("unused large field %s retained", field)
		}
	}
	profile := finalFingerprintCodexWireProfile(nil, body)
	identity := parseFingerprintObservationBodyIdentity(body)
	for i := range body {
		body[i] = 'x'
	}
	if profile.SessionID != "owned-client" || profile.WindowID != "owned-window" || identity.metadata["session_id"] != "owned-client" {
		t.Fatal("published profile/identity retained borrowed body storage")
	}
}

func TestFingerprintBodyIdentityIgnoresUnrelatedNumbers(t *testing.T) {
	// Identity observation needs only client_metadata. A syntactically valid
	// number in an unrelated extension must not require float64 conversion or
	// suppress those small fields. Malformed metadata retains its old behavior.
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{"extension overflow", `{"extension":1e1000,"client_metadata":{"session_id":"session"}}`, true},
		{"nested payload overflow", `{"input":[{"tool_output":{"number":1e1000}}],"client_metadata":{"session_id":"session"}}`, true},
		{"metadata overflow", `{"client_metadata":{"session_id":"session","number":1e1000}}`, false},
		{"malformed body", `{"extension":1e,"client_metadata":{"session_id":"session"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity := parseFingerprintObservationBodyIdentity([]byte(test.body))
			if got := identity.metadata["session_id"] == "session"; got != test.want {
				t.Fatalf("metadata observed=%v, want %v", got, test.want)
			}
		})
	}
}

func BenchmarkFingerprintObservationBodyProjection(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		body := []byte(`{"input":[{"role":"user","content":"` + strings.Repeat("x", size) + `"}],"session_id":"root","client_metadata":{"session_id":"client","x-codex-turn-metadata":"{\"window_id\":\"window\",\"request_kind\":\"turn\"}"},"prompt_cache_key":"client"}`)
		for _, implementation := range []struct {
			name string
			read func(http.Header, []byte) CodexWireProfile
		}{{"legacy", legacyFinalFingerprintCodexWireProfile}, {"projected", finalFingerprintCodexWireProfile}} {
			b.Run(fmt.Sprintf("%dKiB/%s", size>>10, implementation.name), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					profile := implementation.read(nil, body)
					if profile.SessionID != "client" {
						b.Fatal("lost profile")
					}
				}
			})
		}
	}
}
