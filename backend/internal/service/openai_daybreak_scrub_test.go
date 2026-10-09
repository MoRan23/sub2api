package service

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func assertDaybreakTopLevelCyberAbsent(t *testing.T, body []byte) {
	t.Helper()
	require.True(t, json.Valid(body))
	gjson.ParseBytes(body).ForEach(func(key, value gjson.Result) bool {
		var name string
		require.NoError(t, json.Unmarshal([]byte(key.Raw), &name))
		if name == "access_programs" && value.IsObject() {
			value.ForEach(func(key, _ gjson.Result) bool {
				var field string
				require.NoError(t, json.Unmarshal([]byte(key.Raw), &field))
				require.NotEqual(t, "cyber", field, "every duplicate or escaped member must be stripped")
				return true
			})
		}
		return true
	})
}

func TestStripOpenAIRequestCyberPreservesRawUntouchedData(t *testing.T) {
	body := []byte(`{
  "model":"gpt-6-astra",
  "opaque":900719925474099312345678901234567890,
  "exponent":1.234567890123456789e+100,
  "access_programs": {"cyber":"daybreak_blue", "other":{"cyber":"keep nested"}, "precise":9007199254740993},
  "input":[{"role":"user","content":"\"cyber\": secret"},{"type":"reasoning","encrypted_content":"enc_opaque=","access_programs":{"cyber":"history"}}],
  "tools":[{"parameters":{"properties":{"cyber":{"type":"string"}},"access_programs":{"cyber":"schema"}}}],
  "cyber":"keep root unrelated"
}`)
	before := bytes.Clone(body)
	wire, changed, err := stripOpenAIRequestCyber(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, before, body)
	assertDaybreakTopLevelCyberAbsent(t, wire)
	for _, path := range []string{"model", "opaque", "exponent", "input", "tools", "cyber", "access_programs.other", "access_programs.precise"} {
		require.Equal(t, gjson.GetBytes(before, path).Raw, gjson.GetBytes(wire, path).Raw, path)
	}
	clear(wire)
	require.Equal(t, before, body, "rewritten body owns its own backing storage")
}

func TestStripOpenAIRequestCyberDuplicateAndEscapedMembers(t *testing.T) {
	for _, body := range []string{
		`{"access_programs":{"cyber":"one","cyber":null,"other":true,"cyber":{"nested":1}}}`,
		`{"access_programs":{"cyber":"first"},"access_programs":{"cyber":"last","keep":1}}`,
		`{"access_programs":{"cyber":null},"access_programs":null,"access_programs":{"cyber":false}}`,
		`{"access_programs":{"cyber":true},"access_progr\u0061ms":{"\u0063yber":"second"}}`,
		`{"\u0061ccess_programs":{"cy\u0062er":"blue","cyber":"red","keep":2}}`,
		`{"access_programs":{"keep":2,"cyber":0}}`,
		`{"access_programs":{"cyber":0,"keep":2}}`,
		`{"access_programs":{"left":1,"cyber":0,"right":2}}`,
		`{"access_programs":{"cyber":0,"cyber":1}}`,
		`{"access_programs":{"cyber":0},"keep":{"access_programs":{"cyber":1}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			raw := []byte(body)
			wire, changed, err := stripOpenAIRequestCyber(raw)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, body, string(raw))
			assertDaybreakTopLevelCyberAbsent(t, wire)
		})
	}
}

func TestStripOpenAIRequestCyberAllValuesAndEmptyObject(t *testing.T) {
	for _, value := range []string{`null`, `true`, `false`, `""`, `"arbitrary"`, `42`, `-12.5e+8`, `[]`, `{}`, `{"cyber":"nested"}`, `["cyber",{"cyber":true}]`} {
		body := []byte(`{"access_programs":{"cyber":` + value + `},"model":"x"}`)
		wire, changed, err := stripOpenAIRequestCyber(body)
		require.NoError(t, err, value)
		require.True(t, changed, value)
		assertDaybreakTopLevelCyberAbsent(t, wire)
		require.JSONEq(t, `{"access_programs":{},"model":"x"}`, string(wire))
	}
}

func TestStripOpenAIRequestCyberNoopReusesBody(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"access_programs":{}}`, `{"access_programs":{"other":"value"}}`,
		`{"access_programs":null}`, `{"access_programs":"cyber"}`, `{"access_programs":false}`,
		`{"access_programs":7}`, `{"access_programs":[{"cyber":true}]}`,
		`{"cyber":true,"input":[{"access_programs":{"cyber":"history"}}]}`,
		`{"Access_programs":{"cyber":true},"access_programs":{"Cyber":false}}`,
	} {
		body := []byte(raw)
		wire, changed, err := stripOpenAIRequestCyber(body)
		require.NoError(t, err, raw)
		require.False(t, changed, raw)
		require.Equal(t, raw, string(wire))
		require.Same(t, &body[0], &wire[0], "no-op must not clone complete request")
	}
}

func TestStripOpenAIRequestCyberMalformedFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"access_programs":{"cyber":true}`, `{"access_programs":{"cyber":}}`,
		`{"access_programs":{"cyber":true,}}`, `{"access_programs":{"cyber":true}} trailing`,
		`{"access_programs":{"cyber":NaN}}`, `{"access_programs":{"cyber":01}}`,
	} {
		wire, _, err := stripOpenAIRequestCyber([]byte(raw))
		require.Error(t, err, raw)
		require.Nil(t, wire, "failed strip must not return an unsanitized sendable body")
	}
}

func TestStripOpenAIAccessProgramsCyberMultipartJSONField(t *testing.T) {
	for _, raw := range []string{
		`{"cyber":"daybreak_blue","remaining":9007199254740993}`,
		`{"\u0063yber":null,"cyber":true,"remaining":9007199254740993}`,
	} {
		body := []byte(raw)
		wire, changed, err := stripOpenAIAccessProgramsCyber(body)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, raw, string(body))
		require.JSONEq(t, `{"remaining":9007199254740993}`, string(wire))
		require.Equal(t, "9007199254740993", gjson.GetBytes(wire, "remaining").Raw)
	}
	for _, raw := range []string{`{}`, `{"other":{"cyber":"nested"}}`, `null`, `[]`, `false`, `"cyber"`} {
		body := []byte(raw)
		wire, changed, err := stripOpenAIAccessProgramsCyber(body)
		require.NoError(t, err)
		require.False(t, changed)
		require.Same(t, &body[0], &wire[0])
	}
	wire, _, err := stripOpenAIAccessProgramsCyber([]byte(`{"cyber":true`))
	require.Error(t, err)
	require.Nil(t, wire)
}
