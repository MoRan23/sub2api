package service

import (
	"net/http"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexEngineResponseErrorOwnsParsedSummary(t *testing.T) {
	for _, shape := range []string{"bare", "error", "response.error"} {
		t.Run(shape, func(t *testing.T) {
			object := `{"code":"fixture_code","type":"fixture_type","message":"fixture_message","status":429,"padding":"` + strings.Repeat("x", 1<<20) + `"}`
			switch shape {
			case "error":
				object = `{"error":` + object + `}`
			case "response.error":
				object = `{"response":{"error":` + object + `}}`
			}
			// Compare against parsed Raw storage, not the input []byte: ParseBytes
			// may itself copy the entire JSON before returning string views.
			parsed := gjson.Parse(object)
			failure := parsed
			if shape != "bare" {
				failure = parsed.Get(shape)
			}
			message := failure.Get("message").String()
			eventBacking := strings.Repeat("x", 1<<20) + "error"
			event := eventBacking[len(eventBacking)-len("error"):]
			require.True(t, codexEngineStringWithinBacking(message, parsed.Raw))
			code, errorType, ownedMessage, ownedEvent := ownCodexEngineErrorSummary(failure, message, event)
			require.Equal(t, "fixture_code", code)
			require.Equal(t, "fixture_type", errorType)
			require.Equal(t, "fixture_message", ownedMessage)
			require.Equal(t, "error", ownedEvent)
			for _, field := range []string{code, errorType, ownedMessage, ownedEvent} {
				require.False(t, codexEngineStringWithinBacking(field, parsed.Raw), "summary field retained parsed JSON storage")
				require.False(t, codexEngineStringWithinBacking(field, eventBacking), "summary field retained event storage")
			}

			// All constructor shapes retain the existing status and rule semantics.
			result := newCodexEngineResponseError(http.StatusOK, []byte(object), event)
			require.Equal(t, http.StatusTooManyRequests, result.status)
			require.Equal(t, http.StatusTooManyRequests, result.reportedStatus)
			require.Equal(t, code, result.code)
			require.Equal(t, errorType, result.errorType)
			require.Equal(t, ownedMessage, result.message)
			require.Equal(t, ownedEvent, result.event)
			require.False(t, codexEngineStringWithinBacking(result.event, eventBacking))
			require.Contains(t, string(result.tempUnschedBody), "fixture_message")
			require.NotContains(t, string(result.tempUnschedBody), "padding")
		})
	}
}

// Pointer inspection is confined to tests; it never reads or modifies memory
// outside the string. No timing or process-wide heap thresholds are required.
func codexEngineStringWithinBacking(value, backing string) bool {
	if len(value) == 0 || len(backing) == 0 {
		return false
	}
	address := uintptr(unsafe.Pointer(unsafe.StringData(value)))
	start := uintptr(unsafe.Pointer(unsafe.StringData(backing)))
	return address >= start && address < start+uintptr(len(backing))
}
