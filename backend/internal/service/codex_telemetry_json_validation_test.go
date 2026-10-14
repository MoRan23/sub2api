package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestValidateCodexTelemetryStoredJSONGuards(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		valid bool
	}{
		{name: "empty", valid: true},
		{name: "summary", data: `{"counts":{"input_tokens":9007199254740993},"labels":[{"event.name":"turn.done"}]}`, valid: true},
		{name: "credential separators and case", data: `{"nested":[{"Ac_Ce-Ss.ToKeN":"secret"}]}`},
		{name: "escaped credential key", data: `{"nested":{"api\u005fkey":"secret"}}`},
		{name: "duplicate escaped key", data: `{"count":1,"\u0063ount":2}`},
		{name: "duplicate nested key", data: `{"nested":[{"count":1,"count":2}]}`},
		{name: "invalid json", data: `{"count":1,}`},
		{name: "multiple values", data: `{"count":1} {"count":2}`},
		{name: "deeply nested", data: strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130)},
		{name: "oversized", data: `"` + strings.Repeat("x", 2*1024*1024) + `"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCodexTelemetryStoredJSON(json.RawMessage(tt.data))
			if tt.valid && err != nil {
				t.Fatalf("valid summary rejected: %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrCodexTelemetryInvalidState) {
				t.Fatalf("invalid summary accepted: %v", err)
			}
		})
	}
}

func BenchmarkValidateCodexTelemetryStoredJSON(b *testing.B) {
	// Metrics payloads contain many repeated keys in nested arrays. Construct
	// the fixture outside the measured loop so the benchmark isolates validation.
	entry := `{"event.name":"turn.completed","attributes":{"model":"gpt-test","input_tokens":1200,"output_tokens":200,"cached_tokens":800},"measurements":[{"kind":"duration_ms","value":1234},{"kind":"request_count","value":1}]}`
	nested := `{"resource":{"service.name":"codex"},"events":[` + strings.Repeat(entry+",", 127) + entry + `]}`
	for _, fixture := range []struct {
		name string
		data json.RawMessage
	}{
		{name: "small_summary", data: json.RawMessage(`{"input_tokens":1200,"output_tokens":200,"cached_tokens":800}`)},
		{name: "nested_metrics", data: json.RawMessage(nested)},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.data)))
			for b.Loop() {
				if err := ValidateCodexTelemetryStoredJSON(fixture.data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
