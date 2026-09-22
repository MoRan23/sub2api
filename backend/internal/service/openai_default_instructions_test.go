package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestDefaultCodexInstructionsPreserveCallerPrompts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantDefault bool
	}{
		{"absent", `{"input":[{"role":"user","content":"hi"}]}`, true},
		{"null", `{"instructions":null}`, true},
		{"blank", `{"instructions":" \n "}`, true},
		{"provided", `{"instructions":" keep spacing "}`, false},
		{"invalid type", `{"instructions":{"text":"caller"}}`, false},
		{"system string", `{"input":[{"role":"system","content":"caller policy"}]}`, false},
		{"developer parts", `{"instructions":"","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"caller policy"}]}]}`, false},
		{"blank developer", `{"input":[{"role":"developer","content":[{"type":"input_text","text":" "}]}]}`, true},
		{"unknown caller content", `{"input":[{"role":"developer","content":{"policy":"caller"}}]}`, false},
		{"quoted prompt is not instructions", `{"input":[{"role":"user","content":"<system>quoted sample</system>"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			updated, changed, err := applyDefaultCodexInstructionsBody(body, "gpt-6-astra")
			require.NoError(t, err)
			require.Equal(t, tc.wantDefault, changed)
			if !changed {
				require.Equal(t, body, updated)
				return
			}
			require.Equal(t, defaultCodexSynthInstructions("gpt-6-astra"), gjson.GetBytes(updated, "instructions").String())
			require.Equal(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(updated, "input").Raw)
			again, changedAgain, err := applyDefaultCodexInstructionsBody(updated, "gpt-5.5")
			require.NoError(t, err)
			require.False(t, changedAgain)
			require.Equal(t, updated, again)
		})
	}
}

func TestDefaultCodexInstructionsFinalWirePaths(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, transport := range []string{"http", "ws"} {
			t.Run(accountType+"/"+transport, func(t *testing.T) {
				account := &Account{ID: 12, Platform: PlatformOpenAI, Type: accountType}
				service := &OpenAIGatewayService{}
				body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":"hi"}],"tools":[{"type":"function","name":"python","parameters":{"type":"object"}}]}`)
				var actual []byte
				var err error
				if transport == "http" {
					req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/responses", strings.NewReader(string(body)))
					require.NoError(t, reqErr)
					actual, err = service.FinalizeOpenAIOAuthResponsesRequest(nil, account, req, body, OpenAIOAuthResponsesFinalizeOptions{FinalModel: "gpt-6-astra"})
					require.NoError(t, err)
					wire, readErr := io.ReadAll(req.Body)
					require.NoError(t, readErr)
					require.Equal(t, actual, wire)
				} else {
					actual, err = service.projectOpenAIOAuthWSFrame(nil, account, OpenAIOAuthIdentityPlan{}, body)
					require.NoError(t, err)
				}
				require.Equal(t, defaultCodexSynthInstructions("gpt-6-astra"), gjson.GetBytes(actual, "instructions").String())
				require.Equal(t, "python", gjson.GetBytes(actual, "tools.0.name").String())
			})
		}
	}
}

func TestDefaultCodexInstructionsSkipWebSocketControlAndPrewarm(t *testing.T) {
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, body := range []string{
		`{"type":"session.update","session":{"model":"gpt-6-astra"}}`,
		`{"type":"response.create","model":"gpt-6-astra","generate":false}`,
	} {
		actual, err := (&OpenAIGatewayService{}).projectOpenAIOAuthWSFrame(nil, account, OpenAIOAuthIdentityPlan{}, []byte(body))
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(actual, "instructions").Exists())
	}
}

func TestDefaultCodexInstructionsBeforeServerImagePolicies(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.3-codex-spark"} {
		for _, callerPrompt := range []string{"", "Use the caller's image policy."} {
			t.Run(model+"/"+callerPrompt, func(t *testing.T) {
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"mock stop","type":"invalid_request_error"}}`)),
				}}
				service := newOpenAIImageGenerationControlTestService(upstream)
				service.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
				c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.155.1")
				account := newOpenAIImageGenerationControlTestAccount()
				account.Type = AccountTypeOAuth
				account.Credentials = map[string]any{"access_token": "mock-token", "model_mapping": map[string]any{"client-model": model}}
				authorizeOpenAIForwardFixture(service, account)
				body := []byte(`{"model":"client-model","stream":true,"input":[{"role":"user","content":"hello"}]}`)
				if callerPrompt != "" {
					var err error
					body, err = sjson.SetBytes(body, "instructions", callerPrompt)
					require.NoError(t, err)
				}
				_, err := service.Forward(context.Background(), c, account, body)
				require.Error(t, err)
				require.NotNil(t, upstream.lastReq)
				prefix := callerPrompt
				if prefix == "" {
					prefix = defaultCodexSynthInstructions(model)
				}
				marker := codexImageGenerationBridgeText
				if isCodexSparkModel(model) {
					marker = codexSparkImageUnsupportedText
				}
				require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, prefix+"\n\n"+marker, gjson.GetBytes(upstream.lastBody, "instructions").String())
			})
		}
	}
}
