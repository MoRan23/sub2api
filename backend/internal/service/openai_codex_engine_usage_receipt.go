package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

const codexEngineUsageIDHeader = "X-Codex-Engine-Usage-Id"

type codexEngineUsageReceipt struct {
	requestID   string
	fingerprint string
}

// Read only the selected Engine's physical upstream response. The receipt is
// never accepted from client headers, body fields, or a generic API-key account.
func codexEngineUsageReceiptID(account *Account, headers http.Header) string {
	if account == nil || !account.IsCodexEngine() || account.ID <= 0 {
		return ""
	}
	id := codexEngineUsageIDFromHeaders(headers)
	if id == "" {
		return ""
	}
	// usage_logs.request_id is VARCHAR(64). A versioned account/receipt digest
	// preserves the scope without storing a potentially oversized concatenation.
	digest := sha256.Sum256([]byte(fmt.Sprintf("codex-engine-usage-v1:%d:%s", account.ID, id)))
	return "ce:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func codexEngineUsageIDFromHeaders(headers http.Header) string {
	var values []string
	for name, entries := range headers {
		if strings.EqualFold(name, codexEngineUsageIDHeader) {
			values = append(values, entries...)
		}
	}
	if len(values) != 1 {
		return ""
	}
	id := values[0]
	if len(id) != 47 || !strings.HasPrefix(id, "ceu_") {
		return ""
	}
	for _, ch := range id[4:] {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return ""
		}
	}
	return id
}

func newCodexEngineUsageReceipt(id string, input *OpenAIRecordUsageInput) *codexEngineUsageReceipt {
	// The first accepted charge owns the price, route, and body snapshot. Replays
	// compare only identity and upstream meter facts, so a changed client request
	// ID, body encoding, group, or current price cannot create a second charge.
	summary := struct {
		Version        int
		ID             string
		UserID         int64
		APIKeyID       int64
		Usage          OpenAIUsage
		ImageCount     int
		WebSearchCalls int
		SearchCount    int
	}{1, id, input.User.ID, input.APIKey.ID, input.Result.Usage, input.Result.ImageCount, input.Result.WebSearchCalls, input.Result.SearchCount}
	encoded, _ := json.Marshal(summary)
	digest := sha256.Sum256(encoded)
	// The dedup table stores VARCHAR(64); Version above is already hash-bound.
	return &codexEngineUsageReceipt{requestID: id, fingerprint: hex.EncodeToString(digest[:])}
}

// A receipt acknowledges an accepted execution, not final metering. Responses
// settle from completed/incomplete usage; failed usage remains partial. Chat
// needs [DONE]; Messages needs message_stop after a complete final delta.
func observeCodexEngineUsageFinality(result *OpenAIForwardResult, data []byte, event string) {
	observeCodexEngineUsageFinalityParsed(result, data, event, nil)
}

// An image observation may already own a validated JSON root for this frame.
// Share it for receipt finality instead of materializing the same image again.
func observeCodexEngineUsageFinalityParsed(result *OpenAIForwardResult, data []byte, event string, parsed *gjson.Result) {
	if result == nil || codexEngineUsageIDFromHeaders(result.UpstreamHeaders) == "" {
		return
	}
	defer func() {
		if result.CodexEngineUsageFinalized {
			// Only trusted Engine receipts use authoritative final counters,
			// including zero and absent optional fields. Preserve the generic
			// parser's progressive-usage fallback for all other responses.
			result.Usage = result.codexEngineReceiptUsage
		}
	}()
	if result.CodexEngineUsageFinalized {
		return
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		if result.Stream && result.codexEngineUsageChatObserved {
			result.CodexEngineUsageFinalized = true
		}
		return
	}
	if event == "message_delta" {
		// Only the last complete delta is authoritative. Do not fill missing
		// counters from message_start or an earlier delta.
		result.codexEngineUsageMessageObserved = false
	}
	if result.Stream && event != "message_stop" && !bytes.Contains(data, []byte(`"usage"`)) {
		return
	}
	if parsed == nil && !gjson.ValidBytes(data) {
		// Do not let a malformed final usage frame settle the previous partial
		// Chat/Messages candidate when its closing marker arrives.
		result.codexEngineUsageChatObserved = false
		result.codexEngineUsageMessageObserved = false
		return
	}
	complete := false
	var completeUsage OpenAIUsage
	var root gjson.Result
	if parsed != nil {
		root = *parsed
	} else {
		root = gjson.ParseBytes(data)
	}
	if event == "response.failed" || root.Get("type").String() == "response.failed" || root.Get("status").String() == "failed" || root.Get("response.status").String() == "failed" {
		return
	}
	chatFinished := false
	root.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		reason := choice.Get("finish_reason")
		chatFinished = reason.Type == gjson.String && strings.TrimSpace(reason.String()) != ""
		return !chatFinished
	})
	for _, path := range []string{"response.usage", "usage", "message.usage"} {
		usage := root.Get(path)
		if !usage.IsObject() {
			continue
		}
		hasInput := codexEngineUsageCounterPresent(usage, "input_tokens", "prompt_tokens")
		hasOutput := codexEngineUsageCounterPresent(usage, "output_tokens", "completion_tokens")
		parsed, _ := openAIUsageFromGJSON(usage)
		if result.UpstreamEndpoint == "/v1/messages" {
			// Engine's Messages adapter reports uncached input separately. The
			// shared OpenAI meter expects total input before cache subtraction.
			parsed.InputTokens += parsed.CacheReadInputTokens + parsed.CacheCreationInputTokens
		}
		if hasInput && hasOutput && !complete {
			complete = true
			completeUsage = parsed
		}
		if path == "usage" {
			result.codexEngineUsageChatObserved = false
		}
		if path == "usage" && chatFinished && codexEngineUsageCounterPresent(usage, "prompt_tokens") && codexEngineUsageCounterPresent(usage, "completion_tokens") {
			result.codexEngineUsageChatObserved = true
			result.codexEngineReceiptUsage = parsed
		}
		if event == "message_delta" && path == "usage" && hasInput && hasOutput {
			result.codexEngineUsageMessageObserved = true
			result.codexEngineReceiptUsage = parsed
		}
	}
	if !result.Stream {
		result.CodexEngineUsageFinalized = complete
		result.codexEngineReceiptUsage = completeUsage
		return
	}
	switch event {
	case "response.completed", "response.done", "response.incomplete":
		result.CodexEngineUsageFinalized = complete
		result.codexEngineReceiptUsage = completeUsage
	case "message_stop":
		result.CodexEngineUsageFinalized = result.codexEngineUsageMessageObserved
	}
}

func codexEngineUsageCounterPresent(usage gjson.Result, paths ...string) bool {
	for _, path := range paths {
		value := usage.Get(path)
		if value.Type != gjson.Number {
			continue
		}
		if count, err := strconv.ParseInt(value.Raw, 10, 64); err == nil && count >= 0 {
			return true
		}
	}
	return false
}
