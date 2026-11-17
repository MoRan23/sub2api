package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Frozen pre-optimization extraction order and ownership for differential tests
// and same-process benchmarks. The shared leaf helpers only interpret values;
// the old body-sized ParseBytes/GetBytes allocations remain here deliberately.
func legacyCodexTelemetryInputFromWire(account *Account, headers http.Header, body []byte, proxyURL string, websocket bool, profile CodexWireProfile) CodexTelemetryInput {
	identity := func(value string, names ...string) string {
		if websocket && value != "" {
			return codexTelemetryUUID(value)
		}
		for _, name := range names {
			if values := headerValuesCaseInsensitive(headers, name); len(values) > 0 {
				return codexTelemetryUUID(values[0])
			}
		}
		return codexTelemetryUUID(value)
	}
	userAgent := codexTelemetryHeader(headers, "User-Agent")
	version := codexTelemetryHeader(headers, "version")
	if version == "" {
		version = codexClientVersionFromUA(userAgent)
	}
	auth := strings.TrimSpace(codexTelemetryHeader(headers, "Authorization"))
	token := ""
	if scheme, value, ok := strings.Cut(auth, " "); ok && strings.EqualFold(scheme, "Bearer") {
		token = strings.TrimSpace(value)
	}
	var review *bool
	if profile.AutoReviewEnabled != nil {
		value := *profile.AutoReviewEnabled
		review = &value
	}
	subagent := profile.SubagentKind
	if subagent == "" {
		subagent = profile.SubagentHeader
	}
	if subagent == "" {
		subagent = codexTelemetryHeader(headers, "x-openai-subagent")
	}
	approval := profile.ExtraMetadata["approval_policy"]
	if approval == "" {
		approval = gjson.GetBytes(body, "approval_policy").String()
	}
	reviewer := codexTelemetryReviewFieldFromView(headers, gjson.ParseBytes(body), "approvals_reviewer").String()
	if reviewer != "user" && reviewer != "auto_review" {
		reviewer = ""
	}
	guardianV2 := codexTelemetryOptionalBool(codexTelemetryReviewFieldFromView(headers, gjson.ParseBytes(body), "guardian_v2_enabled").String())
	var shell string
	var ids []string
	if len(body) <= openAIRequestOSBodyLimit {
		if gjson.ValidBytes(body) {
			shell = codexTelemetryShellFromValidatedView(gjson.ParseBytes(body))
		}
		ids = codexTelemetryReturnedToolCallIDsFromView(gjson.GetBytes(body, "input"))
	}
	return CodexTelemetryInput{
		AccountID: account.ID, AccountName: strings.Clone(account.Name),
		OSFamily: openai.DetectOSFamilyFromUserAgent(userAgent), InstallationID: identity(profile.InstallationID, codexInstallationIDKey),
		Shell: shell, ReturnedToolCallIDs: ids,
		AccessToken: strings.Clone(token), ChatGPTAccountID: strings.Clone(codexTelemetryHeader(headers, "Chatgpt-Account-Id")), ProxyURL: strings.Clone(proxyURL),
		UserAgent: strings.Clone(userAgent), Originator: strings.Clone(codexTelemetryHeader(headers, "originator")), Version: strings.Clone(version),
		ProductSKU: codexTelemetryAuxiliaryHeader(headers, "X-OpenAI-Product-Sku"), CodexResidency: codexTelemetryAuxiliaryHeader(headers, "X-OpenAI-Internal-Codex-Residency"),
		SessionID: identity(profile.SessionID, "session-id", "session_id"), ThreadID: identity(profile.ThreadID, "thread-id", "thread_id"), TurnID: strings.Clone(profile.TurnID.Value),
		ParentThreadID: identity(profile.TurnLineage.ParentThreadID, "x-codex-parent-thread-id"), ParentTurnID: strings.Clone(profile.TurnLineage.ParentTurnID.Value), RootTurnID: strings.Clone(profile.TurnLineage.RootTurnID.Value),
		ForkedFromThreadID: codexTelemetryUUID(profile.TurnLineage.ForkedFromThreadID), ThreadSource: strings.Clone(profile.ThreadSource), TurnTrigger: strings.Clone(profile.TurnTrigger),
		AgentName: strings.Clone(profile.AgentName), SubagentKind: strings.Clone(subagent), Sandbox: strings.Clone(profile.Sandbox), SandboxMode: strings.Clone(profile.SandboxMode),
		OpenAISubagent: strings.Clone(firstNonEmptyString(codexTelemetryHeader(headers, "x-openai-subagent"), profile.SubagentHeader)), GuardianRequest: strings.Clone(openAIGuardianKind(headers)),
		ApprovalPolicy: strings.Clone(approval), ApprovalsReviewer: strings.Clone(reviewer), AutoReviewEnabled: review, GuardianV2Enabled: guardianV2,
		Model: strings.Clone(gjson.GetBytes(body, "model").String()), Effort: strings.Clone(gjson.GetBytes(body, "reasoning.effort").String()),
		ServiceTier: strings.Clone(gjson.GetBytes(body, "service_tier").String()), WebSocket: websocket, StartedAt: time.Now(),
	}
}

func TestCodexTelemetryBodyViewMatchesLegacy(t *testing.T) {
	account := &Account{ID: 17, Name: "test-account", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	headers := http.Header{"X-Codex-Turn-Metadata": {`{"guardian_v2_enabled":true,"approvals_reviewer":"auto_review"}`}}
	for _, body := range []string{
		``, `null`, `[]`, `"text"`, `{}`, `{"model":"test","model":"later","approval_policy":"never","reasoning":{"effort":"max"},"service_tier":"priority"}`,
		`{"client_metadata":{"x-codex-turn-metadata":"{\"guardian_v2_enabled\":false,\"approvals_reviewer\":\"user\"}"},"guardian_v2_enabled":true}`,
		`{"client_metadata":{"guardian_v2_enabled":null,"approvals_reviewer":"invalid"}}`,
		`{"x-codex-turn-metadata":{"guardian_v2_enabled":"true","approvals_reviewer":"user"},"guardian_v2_enabled":false}`,
		`{"model":"test","input":[{"type":"function_call_output","call_id":"abc","output":"text"},{"type":"custom_tool_call_output","call_id":"def","output":"text"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"old"},{"role":"user","content":"new"},{"type":"function_call_output","call_id":"new"}]}`,
		`{"input":[{"role":"user","content":"<environment_context><shell>bash</shell></environment_context>"}]}`,
		`{"messages":[{"role":"user","content":"<environment_context><shell>fish</shell></environment_context>"}],"input":[]}`,
		`{"model":"partial","guardian_v2_enabled":true,`, `{"approvals_reviewer":"user","input":[{"type":"function_call_output","call_id":"partial"}]`,
		`{"model":"first"} {"model":"second"}`, `broken {"model":"test"}`, `{"model":true,"reasoning":{"effort":42}}`,
		`broken {"model":"test","approval_policy":"never","reasoning":{"effort":"high"},"service_tier":"priority","input":[{"type":"function_call_output","call_id":"partial"}]}`,
	} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			for _, ws := range []bool{false, true} {
				raw := []byte(body)
				profile := finalFingerprintCodexWireProfile(headers, raw)
				want := legacyCodexTelemetryInputFromWire(account, headers, raw, "http://proxy", ws, profile)
				got := codexTelemetryInputFromWire(account, headers, raw, "http://proxy", ws, profile)
				got.StartedAt = want.StartedAt
				require.Equal(t, want, got)
			}
		})
	}
}

func TestCodexTelemetryBodyViewResultsOwnTheirScalars(t *testing.T) {
	for _, input := range []string{
		`[{"type":"function_call_output","call_id":"private-call","output":"result"}]`,
		`[{"role":"user","content":"<environment_context><shell>bash</shell></environment_context>"}]`,
	} {
		body := []byte(`{"model":"gpt-6-luna","approval_policy":"never","approvals_reviewer":"user","guardian_v2_enabled":true,"reasoning":{"effort":"high"},"service_tier":"priority","input":` + input + `}`)
		account := &Account{ID: 17}
		want := legacyCodexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{})
		got := codexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{})
		for i := range body {
			body[i] = 'x'
		}
		got.StartedAt = want.StartedAt
		require.Equal(t, want, got, "extracted scalars must survive reuse of the borrowed request buffer")
	}
}

func telemetryBodyViewFixture(size int, image bool) []byte {
	prefix := `{"model":"gpt-6-luna","input":[{"role":"user","content":"`
	suffix := `"}],"approvals_reviewer":"user","guardian_v2_enabled":true,"reasoning":{"effort":"high"},"service_tier":"priority"}`
	fill := "x"
	if image {
		prefix = `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,`
		suffix = `"}]}],"approvals_reviewer":"user","guardian_v2_enabled":true,"reasoning":{"effort":"high"},"service_tier":"priority"}`
		fill = "A"
	}
	return []byte(prefix + strings.Repeat(fill, max(0, size-len(prefix)-len(suffix))) + suffix)
}

func TestCodexTelemetryBodyViewKeepsLimits(t *testing.T) {
	account := &Account{ID: 17}
	for _, size := range []int{openAIRequestOSBodyLimit - 512, openAIRequestOSBodyLimit, 32 << 20} {
		body := telemetryBodyViewFixture(size, true)
		want := legacyCodexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{})
		got := codexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{})
		got.StartedAt = want.StartedAt
		require.Equal(t, want, got)
		require.Equal(t, "user", got.ApprovalsReviewer, "review fields are independent of shell's body-size limit")
		require.Empty(t, got.Shell)
	}
	items := make([]map[string]string, openAIRequestOSMessageLimit+1)
	for i := range items {
		items[i] = map[string]string{"type": "function_call_output", "call_id": "a"}
	}
	body, err := json.Marshal(map[string]any{"input": items})
	require.NoError(t, err)
	require.Nil(t, codexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{}).ReturnedToolCallIDs)
}

func TestOpenAIMessagesGuardRetainsBorrowableSnapshot(t *testing.T) {
	resetTurnStateLocalStore(t, 32)
	svc := newTurnStateTestService()
	account := oauthTurnStateAccount(42)
	c, _ := newTurnStateIdentityTestContext(t, 7, "owner:42", "11111111-1111-4111-8111-111111111111", turnStateSessionA, turnStateThreadA)
	plan, ok := OpenAIOAuthIdentityPlanFromContext(c)
	require.True(t, ok)
	svc.noteOpenAICodexTurnStateProvenanceForPlan(c, account, "valid-state", plan)
	plan.RequestTurn.ID = turnStateTurnB
	body := []byte(`{"model":"gpt","client_metadata":{"keep":true,"x-codex-turn-state":"valid-state"}}`)
	req := &http.Request{Header: http.Header{"X-Codex-Turn-State": {"valid-state"}}}
	setOpenAIRequestBodySnapshot(req, body)
	guarded := svc.guardOpenAIMessagesTurnStateForPlan(c, account, req, body, plan)
	require.False(t, gjson.GetBytes(guarded, "client_metadata.x-codex-turn-state").Exists())
	want := bytes.Clone(guarded)
	reader, err := req.GetBody()
	require.NoError(t, err)
	snapshot, ok := reader.(*openAIRequestBodySnapshotReader)
	require.True(t, ok)
	for i := range guarded {
		guarded[i] = 'x'
	}
	require.Equal(t, want, snapshot.snapshot, "mutable caller result must not change the private wire snapshot")
	wire, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, want, wire)
	require.NoError(t, reader.Close())
}

var telemetryBodyViewBenchmarkSink CodexTelemetryInput

func BenchmarkCodexTelemetryBodyView(b *testing.B) {
	account := &Account{ID: 17}
	for _, size := range []int{1 << 20, 8 << 20, 32 << 20} {
		for _, image := range []bool{false, true} {
			body := telemetryBodyViewFixture(size, image)
			for _, legacy := range []bool{true, false} {
				mode := "shared_view"
				if legacy {
					mode = "legacy"
				}
				b.Run(fmt.Sprintf("bytes_%d/image_%t/%s", size, image, mode), func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if legacy {
							telemetryBodyViewBenchmarkSink = legacyCodexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{})
						} else {
							telemetryBodyViewBenchmarkSink = codexTelemetryInputFromWire(account, nil, body, "", false, CodexWireProfile{})
						}
					}
				})
			}
		}
	}
}
