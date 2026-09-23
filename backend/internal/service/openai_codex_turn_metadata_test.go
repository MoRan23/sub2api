package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexTurnMetadataRewritePreservesHeaderSafeJSON(t *testing.T) {
	identity, err := newOpenAICodexRootIdentity()
	require.NoError(t, err)
	// The fork replaced the legacy account/fingerprint mutators with one final
	// projector. Exercise both carriers for every business projection mode.
	rewriters := map[string]func(*testing.T, string) string{}
	for _, mode := range []OpenAIOAuthIdentityProjectionMode{
		OpenAIOAuthIdentityProjectionRegular,
		OpenAIOAuthIdentityProjectionPassthrough,
		OpenAIOAuthIdentityProjectionCompact,
	} {
		for _, carrier := range []string{"header", "body"} {
			if mode == OpenAIOAuthIdentityProjectionCompact && carrier == "body" {
				continue // Compact's narrow schema has no client_metadata body.
			}
			rewriters[string(mode)+"_"+carrier] = func(t *testing.T, raw string) string {
				headers := make(http.Header)
				headers.Set(openAIWSTurnMetadataHeader, raw)
				body, err := json.Marshal(map[string]any{"client_metadata": map[string]any{openAIWSTurnMetadataHeader: raw}})
				require.NoError(t, err)
				out, err := ApplyOpenAIOAuthIdentityPlan(headers, body, OpenAIOAuthIdentityPlan{
					TurnIdentity: identity, TurnIdentityEnabled: true,
					ProjectionMode: mode, InstallationPolicy: OpenAIOAuthInstallationAccountPin,
					InstallationEnabled: true, InstallationID: "77777777-7777-4777-8777-777777777777",
				})
				require.NoError(t, err)
				if carrier == "header" {
					return headers.Get(openAIWSTurnMetadataHeader)
				}
				return gjson.GetBytes(out, "client_metadata."+openAIWSTurnMetadataHeader).String()
			}
		}
	}

	for _, raw := range []string{
		`{"installation_id":"client-install","workspaces":{"C:\\work\\中文🚀":{}}}`,
		`{"installation_id":"client-install","workspaces":{"C:\\work\\中文🚀":{"label":"café"}},"literal":"\\u4e2d","quote":"\"\\","controls":"\b\f\n\r\t\u0000\u007f","timestamp":1789302780858,"enabled":true}`,
		`{"installation_id":"client-install","workspaces":{"C:\\work\\\u4e2d\u6587\ud83d\ude80":{"label":"caf\u00e9"}},"literal":"\\u4e2d","quote":"\"\\","controls":"\b\f\n\r\t\u0000\u007f","timestamp":1789302780858,"enabled":true}`,
		`{"installation_id":"client-install","workspaces":{"C:\\work\\ascii":{}},"literal":"\\u4e2d"}`,
	} {
		var original map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &original))
		for name, rewrite := range rewriters {
			t.Run(name, func(t *testing.T) {
				updated := rewrite(t, raw)
				var decoded map[string]any
				require.NoError(t, json.Unmarshal([]byte(updated), &decoded))
				for key, value := range original {
					if key != "installation_id" {
						require.Equal(t, value, decoded[key], key)
					}
				}
				require.NotEqual(t, original["installation_id"], decoded["installation_id"])
				for _, b := range []byte(updated) {
					require.True(t, b >= 0x20 && b < 0x7f, "non-printable ASCII header byte: 0x%02x", b)
				}
			})
		}
	}
}
