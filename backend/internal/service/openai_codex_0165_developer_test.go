package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Codex 0.165 sends its base prompt as a stable developer input item, including
// on non-Lite requests. Projection must preserve that prompt and opaque replay
// data instead of injecting another copy of the bundled instructions.
func TestCodex0165DeveloperPromptAndEncryptedReplayFinalWire(t *testing.T) {
	const model = "gpt-6-astra"
	const body = `{"model":"gpt-6-astra","include":["reasoning.encrypted_content","output.encrypted_content"],"input":[{"id":"msg_stable_developer","type":"message","role":"developer","content":[{"type":"input_text","text":"Keep this caller policy exactly.\n"}]},{"id":"rs_replay","type":"reasoning","encrypted_content":"opaque+/==\\cipher","summary":[]},{"role":"user","content":"continue"}]}`
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, transport := range []string{"http", "ws"} {
			for _, lite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/lite=%t", accountType, transport, lite), func(t *testing.T) {
					account := &Account{ID: 42, Platform: PlatformOpenAI, Type: accountType}
					svc := &OpenAIGatewayService{}
					plan := codexWireProjectionTestPlan(t)
					plan.CredentialOwnerNamespace = "account:42"
					input := []byte(body)
					var out []byte
					var err error
					if transport == "http" {
						svc.codexModelCapabilities.observeManifest("account:42", []byte(fmt.Sprintf(`{"models":[{"slug":"%s","use_responses_lite":%t}]}`, model, lite)), time.Now())
						req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodPost, chatgptCodexURL, nil)
						require.NoError(t, reqErr)
						out, err = svc.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, input, OpenAIOAuthResponsesFinalizeOptions{Plan: plan, FinalModel: model, RequestKind: "turn"})
					} else {
						input = append([]byte(`{"type":"response.create",`), input[1:]...)
						plan.WireProfile.ToolNamespacesAllowed = lite
						out, err = svc.projectOpenAIOAuthWSFrame(nil, account, plan, input)
					}
					require.NoError(t, err)
					require.False(t, gjson.GetBytes(out, "instructions").Exists())
					require.JSONEq(t, gjson.GetBytes(input, "input").Raw, gjson.GetBytes(out, "input").Raw)
					require.Equal(t, "msg_stable_developer", gjson.GetBytes(out, "input.0.id").String())
					require.Equal(t, gjson.GetBytes(input, "input.1.encrypted_content").String(), gjson.GetBytes(out, "input.1.encrypted_content").String())
					require.JSONEq(t, gjson.GetBytes(input, "include").Raw, gjson.GetBytes(out, "include").Raw)
				})
			}
		}
	}
}
