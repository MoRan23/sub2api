package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// AccountCandyTestTransport reuses the same pinned-account HTTP path as business
// requests. The internal purpose removes health/usage side effects and replay.
type AccountCandyTestTransport struct {
	accounts    AccountRepository
	gateway     *OpenAIGatewayService
	fetchModels func(context.Context, *Account) (*OpenAIModelsResponse, error)
}

func NewAccountCandyTestTransport(accounts AccountRepository, gateway *OpenAIGatewayService) *AccountCandyTestTransport {
	runner := &AccountCandyTestTransport{accounts: accounts, gateway: gateway}
	if gateway != nil {
		runner.fetchModels = gateway.FetchCandyTestModels
	}
	return runner
}

func (r *AccountCandyTestTransport) Execute(parent context.Context, item *CandyTestItem) (*CandyTestExecution, error) {
	started := time.Now()
	if r == nil || r.accounts == nil || r.gateway == nil || item == nil {
		return nil, candyTestError("runner_unavailable")
	}
	if item.PromptVersion != CandyTestPromptVersion {
		return nil, candyTestError("prompt_version_unsupported")
	}
	account, err := r.accounts.GetByID(parent, item.AccountID)
	if err != nil || account == nil {
		return nil, candyTestError("account_missing")
	}
	if account.Platform != PlatformOpenAI {
		return nil, candyTestError("unsupported_platform")
	}
	account = snapshotOAuthRefreshAccount(account)
	if !candyTestRouteMatches(item, account) {
		return nil, candyTestError("configuration_changed")
	}
	account.openAICandyTest = true
	models, err := r.candyTestAccountModelOptions(parent, account)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, model := range models {
		if model.ID == item.Model && (item.ReasoningEffort == "" || containsCandyEffort(model.ReasoningEfforts, item.ReasoningEffort)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, candyTestError("configuration_changed")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	attempt := &openAICandyTestAttempt{}
	ctx = withOpenAICandyTest(ctx, attempt)
	ctx = WithHTTPUpstreamRedirectsDisabled(ctx)
	// An unknown ingress OS deliberately selects the account default, never the
	// administrator's browser or the server's own operating system.
	ctx = ContextWithOpenAIRequestOS(ctx, OpenAIRequestOS{Captured: true, Source: "account_default"})
	credential, err := resolveCredentialAccount(ctx, r.accounts, account)
	if err != nil || credential == nil {
		return nil, candyTestError("authorization_missing")
	}
	if RequiresOpenAIOAuthOSAuthorization(credential) {
		credential, err = ResolveOpenAIOAuthCredentialAccount(ctx, r.accounts, credential, "")
		if err != nil {
			return nil, candyTestError("authorization_missing")
		}
	}
	initial := snapshotOAuthRefreshAccount(credential)
	attempt.validate = func(check context.Context) error {
		business, readErr := r.accounts.GetByID(check, account.ID)
		if readErr != nil || business == nil {
			return candyTestError("authorization_changed")
		}
		if business.OpenAIUpstreamKind() != account.OpenAIUpstreamKind() || business.OpenAIUpstreamRouteGeneration() != account.OpenAIUpstreamRouteGeneration() {
			return candyTestError("configuration_changed")
		}
		current, readErr := resolveCredentialAccount(check, r.accounts, business)
		if readErr != nil || current == nil || current.ID != initial.ID {
			return candyTestError("authorization_changed")
		}
		if initial.OpenAIOAuthAuthorizationGeneration != "" {
			current, readErr = ReloadOpenAIOAuthCredentialAccount(check, r.accounts, initial)
		}
		if readErr != nil || !sameCandyAuthorization(initial, current) {
			return candyTestError("authorization_changed")
		}
		if !initial.IsOpenAIOAuth() || initial.IsOpenAIPersonalAccessToken() || initial.IsOpenAIAgentIdentity() {
			if !sameCandyStaticAuthorization(initial, current) {
				return candyTestError("authorization_changed")
			}
		}
		return nil
	}
	session := uuid.NewString()
	payload := map[string]any{
		"model": item.Model, "stream": true, "store": false,
		"instructions":     "仅按用户题目独立推理，不联网、不运行代码、不调用工具。",
		"input":            []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": CandyTestPrompt}}}},
		"prompt_cache_key": session,
	}
	if item.ReasoningEffort != "" {
		payload["reasoning"] = map[string]any{"effort": item.ReasoningEffort}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, candyTestError("request_invalid")
	}
	writer := newCandyResponseWriter(cancel)
	c, _ := gin.CreateTestContext(writer)
	c.Request, err = http.NewRequestWithContext(ctx, http.MethodPost, "/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, candyTestError("request_invalid")
	}
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Accept", "text/event-stream")
	c.Request.Header.Set("Originator", openai.CodexDefaultOriginator)
	c.Request.Header.Set("session_id", session)
	c.Request.Header.Set("conversation_id", session)
	ua := credential.GetOpenAIOutboundUserAgent()
	if IsOpenAIOAuthOSProfileOwner(credential) {
		profile, profileErr := ResolveOpenAIOAuthOSProfile(ctx, r.accounts, credential, "")
		if profileErr != nil {
			return nil, candyTestError("identity_unavailable")
		}
		ua = profile.UserAgent
	}
	if ua == "" {
		ua = CodexCanonicalUserAgent()
	}
	if ua != "" {
		c.Request.Header.Set("User-Agent", ua)
	}
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	monitorDone := make(chan struct{})
	monitorFailure := make(chan error, 1)
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(CandyTestHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if checkErr := attempt.validate(ctx); checkErr != nil {
					monitorFailure <- checkErr
					cancel()
					return
				}
			}
		}
	}()
	result, forwardErr := r.gateway.Forward(ctx, c, account, body)
	cancel()
	<-monitorDone
	writer.finish()
	execution := &CandyTestExecution{RequestedModel: item.Model, ReasoningEffort: item.ReasoningEffort,
		UpstreamKind: account.OpenAIUpstreamKind(),
		ResponseText: writer.answer(), Completed: writer.completed && !writer.failed, DurationMs: time.Since(started).Milliseconds(),
		UpstreamModel: observedUpstreamResponseModel(c), ModelConflict: observedUpstreamResponseModelConflict(c)}
	if execution.UpstreamModel != "" {
		execution.ModelEvidenceSource = "upstream_json"
	}
	attempt.mu.Lock()
	execution.ActualModel = attempt.actualModel
	execution.ReasoningEffort = attempt.actualEffort
	attempt.mu.Unlock()
	if result != nil {
		usage := result.Usage
		execution.Usage = &usage
		if execution.ActualModel == "" {
			execution.ActualModel = firstNonEmpty(result.UpstreamModel, result.Model)
		}
	}
	select {
	case checkErr := <-monitorFailure:
		return execution, checkErr
	default:
	}
	if parent.Err() != nil {
		return execution, safeCandyTestError(parent, parent.Err())
	}
	if writer.failure != "" {
		return execution, candyTestError(writer.failure)
	}
	if forwardErr != nil {
		return execution, safeCandyTestError(parent, forwardErr)
	}
	if writer.status >= 400 {
		return execution, candyTestError(fmt.Sprintf("upstream_http_%d", writer.status))
	}
	if !execution.Completed {
		return execution, candyTestError("missing_terminal")
	}
	if err := attempt.validate(parent); err != nil {
		return execution, err
	}
	return execution, nil
}

func candyTestRouteMatches(item *CandyTestItem, account *Account) bool {
	if item.ExpectedUpstreamKind == "" {
		// Existing queued jobs predate route snapshots and were Codex jobs.
		return !account.IsOpenAIExcelUpstreamEnabled() && account.OpenAIUpstreamRouteGeneration() == ""
	}
	return item.ExpectedUpstreamKind == account.OpenAIUpstreamKind() && item.ExpectedRouteGeneration == account.OpenAIUpstreamRouteGeneration()
}

func sameCandyStaticAuthorization(before, after *Account) bool {
	if !reflect.DeepEqual(openAIRefreshAuthIdentity(before.Credentials), openAIRefreshAuthIdentity(after.Credentials)) || before.GetOpenAIProtocolAPIKey() != after.GetOpenAIProtocolAPIKey() {
		return false
	}
	if before.IsOpenAIAgentIdentity() {
		// task_id may be initialized before the first inference; it is not an
		// authorization. Runtime identity and signing key changes are different.
		for _, key := range []string{"agent_private_key", "agent_runtime_id"} {
			if before.GetCredential(key) != after.GetCredential(key) {
				return false
			}
		}
	}
	return true
}

// Discards reasoning and protocol frames as they arrive. Unlike a response
// recorder this does not retain the full SSE stream in memory or history.
type candyResponseWriter struct {
	header             http.Header
	status             int
	line               []byte
	data               []byte
	text               strings.Builder
	finalText          string
	finalAuthoritative bool
	completed          bool
	failed             bool
	failure            string
	cancel             context.CancelFunc
	phases             map[int]string
}

func newCandyResponseWriter(cancel context.CancelFunc) *candyResponseWriter {
	return &candyResponseWriter{header: make(http.Header), cancel: cancel, phases: make(map[int]string)}
}
func (w *candyResponseWriter) Header() http.Header { return w.header }
func (w *candyResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *candyResponseWriter) Flush() {}
func (w *candyResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.failure != "" {
		return 0, candyTestError(w.failure)
	}
	for _, b := range p {
		if b == '\n' {
			w.consumeLine()
			w.line = w.line[:0]
		} else {
			w.line = append(w.line, b)
			// Only one bounded frame is held; large reasoning or final envelopes
			// are rejected rather than silently truncating a possibly valid answer.
			if len(w.line)+len(w.data) > 2*CandyTestMaxResponseBytes {
				w.fail("response_too_large")
				return 0, candyTestError(w.failure)
			}
		}
	}
	return len(p), nil
}
func (w *candyResponseWriter) consumeLine() {
	line := bytes.TrimSuffix(w.line, []byte("\r"))
	if len(line) == 0 {
		w.consumeEvent()
		return
	}
	if bytes.HasPrefix(line, []byte("data:")) {
		if len(w.data) > 0 {
			w.data = append(w.data, '\n')
		}
		w.data = append(w.data, bytes.TrimSpace(line[5:])...)
	}
}
func (w *candyResponseWriter) consumeEvent() {
	if len(w.data) == 0 {
		return
	}
	data := w.data
	defer func() { w.data = w.data[:0] }()
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if !gjson.ValidBytes(data) {
		w.fail("invalid_response")
		return
	}
	event := gjson.GetBytes(data, "type").String()
	switch event {
	case "response.output_text.delta":
		phase := w.phases[int(gjson.GetBytes(data, "output_index").Int())]
		if phase != "commentary" && phase != "analysis" {
			w.appendText(gjson.GetBytes(data, "delta").String())
		}
	case "response.failed", "response.incomplete", "error":
		w.fail("upstream_stream_failed")
	case "response.completed", "response.done":
		if w.failed {
			return
		}
		response := gjson.GetBytes(data, "response")
		if status := response.Get("status").String(); status != "" && status != "completed" {
			w.fail("upstream_stream_failed")
			return
		}
		text, tool := candyFinalResponseText(response)
		w.finalAuthoritative = response.Get("output").Exists()
		if tool {
			w.fail("unexpected_tool_call")
			return
		}
		if len(text) > CandyTestMaxResponseBytes {
			w.fail("response_too_large")
			return
		}
		if text != "" {
			w.finalText = text
		}
		w.completed = true
	case "response.output_item.added", "response.output_item.done":
		item := gjson.GetBytes(data, "item")
		index := int(gjson.GetBytes(data, "output_index").Int())
		if _, known := w.phases[index]; !known && len(w.phases) >= 128 {
			w.fail("response_too_large")
			return
		}
		w.phases[index] = item.Get("phase").String()
		kind := item.Get("type").String()
		if strings.Contains(kind, "call") {
			w.fail("unexpected_tool_call")
		}
	}
}
func (w *candyResponseWriter) appendText(text string) {
	if w.text.Len()+len(text) > CandyTestMaxResponseBytes {
		w.fail("response_too_large")
		return
	}
	_, _ = w.text.WriteString(text)
}
func (w *candyResponseWriter) fail(code string) {
	w.failed = true
	if w.failure == "" {
		w.failure = code
	}
	if w.cancel != nil {
		w.cancel()
	}
}
func (w *candyResponseWriter) finish() {
	if len(w.line) > 0 {
		w.consumeLine()
		w.line = nil
	}
	w.consumeEvent()
}
func (w *candyResponseWriter) answer() string {
	if w.finalAuthoritative || w.finalText != "" {
		return w.finalText
	}
	return w.text.String()
}
func candyFinalResponseText(response gjson.Result) (string, bool) {
	var all, final strings.Builder
	for _, item := range response.Get("output").Array() {
		kind := item.Get("type").String()
		if strings.Contains(kind, "call") {
			return "", true
		}
		if kind != "message" || (item.Get("role").String() != "" && item.Get("role").String() != "assistant") {
			continue
		}
		if phase := item.Get("phase").String(); phase == "commentary" || phase == "analysis" {
			continue
		}
		var message strings.Builder
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "output_text" {
				_, _ = message.WriteString(part.Get("text").String())
			}
		}
		if message.Len() == 0 {
			continue
		}
		if all.Len() > 0 {
			_ = all.WriteByte('\n')
		}
		_, _ = all.WriteString(message.String())
		if item.Get("phase").String() == "final_answer" {
			if final.Len() > 0 {
				_ = final.WriteByte('\n')
			}
			_, _ = final.WriteString(message.String())
		}
	}
	if final.Len() > 0 {
		return final.String(), false
	}
	return all.String(), false
}
