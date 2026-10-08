package service

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIIdentityBodyViewPreservesRawValuesAndLastDuplicate(t *testing.T) {
	body := []byte(" \n\t" + `{"input":"discard","in\u0070ut": [ { "text": "<opaque>\u2028", "n": 9007199254740993 } ],"keep":null,"unknown":{"a":true},"client_metadata":{"keep":"yes"}}` + "\n")
	original := bytes.Clone(body)
	root, err := decodeOpenAIIdentityBodyView(body)
	require.NoError(t, err)
	require.Equal(t, `[ { "text": "<opaque>\u2028", "n": 9007199254740993 } ]`, string(root["input"]))
	require.Equal(t, "null", string(root["keep"]))
	root["client_metadata"] = json.RawMessage(`{"session_id":"new"}`)
	delete(root, "unknown")
	out, err := marshalOpenAIIdentityBodyView(root)
	require.NoError(t, err)
	require.True(t, json.Valid(out))
	require.Contains(t, string(out), string(root["input"]))
	require.Equal(t, "9007199254740993", gjson.GetBytes(out, "input.0.n").Raw)
	require.Equal(t, "new", gjson.GetBytes(out, "client_metadata.session_id").String())
	require.False(t, gjson.GetBytes(out, "unknown").Exists())
	require.Equal(t, original, body, "identity rewriting must not contaminate another account attempt")
	out[0] = '['
	require.Equal(t, original, body, "the written body owns its changed output")
}

func TestOpenAIIdentityBodyViewInvalidInputMatchesStandardDecoder(t *testing.T) {
	for _, input := range []string{``, `null`, `[]`, `"text"`, `{"input":}`, `{"a":1} trailing`} {
		t.Run(input, func(t *testing.T) {
			var want map[string]json.RawMessage
			wantErr := json.Unmarshal([]byte(input), &want)
			got, err := decodeOpenAIIdentityBodyView([]byte(input))
			require.Equal(t, want, got)
			if wantErr == nil {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, wantErr.Error())
			}
		})
	}
}

func TestOpenAIIdentityBodyViewEscapedKeysMatchStandardDecoder(t *testing.T) {
	body := []byte(`{"\ud800\u0061":1,"\ufffd":2,"\u0069nput":"first","input":"last","quote\"key":null}`)
	var want map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &want))
	got, err := decodeOpenAIIdentityBodyView(body)
	require.NoError(t, err)
	require.Equal(t, want, got)
	out, err := marshalOpenAIIdentityBodyView(got)
	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &decoded))
	require.Equal(t, want, decoded)
}

func TestOpenAIIdentityProjectionDoesNotCopyHistoryForEveryField(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","input":"` + strings.Repeat("x", 4<<20) + `","opaque":9007199254740993}`)
	identity := OpenAICodexTurnIdentity{SessionID: testOutboundSessionUUID, ThreadID: testOutboundThreadUUID}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out, err := MergeOpenAIOutboundSessionIdentityBody(body, identity)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Equal(t, 4<<20, len(gjson.GetBytes(out, "input").String()))
	require.Equal(t, "9007199254740993", gjson.GetBytes(out, "opaque").Raw)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(len(body))*2,
		"identity metadata changes should allocate one output body, not copies of the entire input plus encoder buffers")
	runtime.KeepAlive(body)
	runtime.KeepAlive(out)
}

func BenchmarkOpenAIIdentityBodyViewLargeHistory(b *testing.B) {
	body := []byte(`{"model":"gpt-6.1-sol","input":"` + strings.Repeat("x", 8<<20) + `","client_metadata":{"keep":"yes"}}`)
	for _, legacy := range []bool{true, false} {
		name := "shared_view"
		if legacy {
			name = "legacy_full_decode"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				var root map[string]json.RawMessage
				var err error
				if legacy {
					err = json.Unmarshal(body, &root)
				} else {
					root, err = decodeOpenAIIdentityBodyView(body)
				}
				if err != nil {
					b.Fatal(err)
				}
				root["client_metadata"] = json.RawMessage(`{"session_id":"new"}`)
				var out []byte
				if legacy {
					out, err = marshalJSONWithoutHTMLEscape(root)
				} else {
					out, err = marshalOpenAIIdentityBodyView(root)
				}
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(out)
			}
		})
	}
}
