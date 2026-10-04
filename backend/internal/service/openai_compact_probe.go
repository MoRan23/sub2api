package service

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	// AccountTestModeDefault drives the standard /responses connection test.
	AccountTestModeDefault = "default"
	// AccountTestModeCompact drives the remote-compaction probe test
	// (native v2: streaming /responses with a compaction_trigger input item).
	AccountTestModeCompact = "compact"
)

func normalizeAccountTestMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AccountTestModeCompact:
		return AccountTestModeCompact
	default:
		return AccountTestModeDefault
	}
}

// createOpenAICompactProbePayload 构造原生 remote compaction v2 探测载荷：
// 流式 /responses + input 末尾 {"type":"compaction_trigger"}。上游已下线
// legacy unary /responses/compact（v1 形态恒 404，#5598/#5624），现行 codex
// 默认协议即 v2（RemoteCompactionV2 Stable + default_enabled）。
func createOpenAICompactProbePayload(model string, isOAuth bool) map[string]any {
	payload := map[string]any{
		"model":        strings.TrimSpace(model),
		"instructions": openAITestInstructions(model),
		"input": []any{
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": randomOpenAICompactTestPrompt(),
			},
			map[string]any{"type": "compaction_trigger"},
		},
		"stream": true,
	}
	// ChatGPT internal API 要求 store: false，与真实转发一致。
	if isOAuth {
		payload["store"] = false
	}
	return payload
}

// openAICompactProbeFoundCompactionItem 判定探测响应是否产出了 compaction
// 输出 item——v2 契约的核心（codex 缺它即 fatal "got 0 items"）。三种形态都
// 认：① SSE 的 output_item.done/added（原生 v2 主形态，codex 只从这里收集
// item）；② SSE 终态 response.completed 的 response.output[]（部分上游只在
// 终态给出 item）；③ 整体 JSON 的 output[]（老网关链把请求降级成 unary）。
func openAICompactProbeFoundCompactionItem(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	bodyText := string(body)
	if _, found := findRawCompactionItemFromSSE(bodyText); found {
		return true
	}
	if finalResponse, ok := extractCodexFinalResponse(bodyText); ok &&
		responsesOutputHasCompactionItem(finalResponse) {
		return true
	}
	return responsesOutputHasCompactionItem(body)
}

// HTTP 200 only acknowledges an SSE stream. A failed or unfinished execution
// cannot establish whether the upstream supports compaction, even if it emitted
// an output item before failing. Keep the public error code and message intact.
func openAICompactProbeResponseError(body []byte) error {
	var failure error
	completed, invalid := false, false
	inspect := func(eventType string, payload []byte) {
		if !gjson.ValidBytes(payload) {
			invalid = true
			return
		}
		root := gjson.ParseBytes(payload)
		if eventType == "" {
			switch root.Get("type").String() {
			case "error", "response.failed", "response.incomplete", "response.completed", "response.done":
				eventType = root.Get("type").String()
			}
		}
		response := root.Get("response")
		if !response.IsObject() {
			response = root
		}
		status := response.Get("status").String()
		hasError := response.Get("error").Exists() && response.Get("error").Type != gjson.Null
		if eventType == "error" || eventType == "response.failed" || eventType == "response.incomplete" ||
			status == "failed" || status == "incomplete" || status == "cancelled" || status == "canceled" || hasError {
			if failure == nil {
				failure = openAICompactProbeExecutionError(eventType, status, payload)
			}
			return
		}
		if eventType == "response.completed" || eventType == "response.done" || eventType == "" {
			if !response.Get("output").IsArray() || (status != "" && status != "completed") {
				invalid = true
				return
			}
			completed = true
		}
	}
	if gjson.ValidBytes(body) {
		inspect("", body)
	} else {
		forEachOpenAISSEFrame(string(body), inspect)
	}
	if failure != nil {
		return failure
	}
	if invalid {
		return fmt.Errorf("Upstream compaction returned an invalid response")
	}
	if !completed {
		return fmt.Errorf("Upstream compaction ended without a completed response")
	}
	return nil
}

func openAICompactProbeExecutionError(eventType, status string, payload []byte) error {
	if eventType == "" {
		eventType = status
	}
	if eventType == "" {
		eventType = "error"
	}
	label := "Upstream compaction failed (" + eventType + ")"
	for _, path := range []string{"response.error.code", "error.code", "code", "response.error.type", "error.type"} {
		if code := strings.TrimSpace(gjson.GetBytes(payload, path).String()); code != "" {
			label += " [" + code + "]"
			break
		}
	}
	message := extractOpenAISSEErrorMessage(payload)
	if message == "" {
		for _, path := range []string{"response.incomplete_details.reason", "incomplete_details.reason"} {
			if reason := strings.TrimSpace(gjson.GetBytes(payload, path).String()); reason != "" {
				message = reason
				break
			}
		}
	}
	if message != "" {
		label += ": " + message
	}
	return fmt.Errorf("%s", truncateString(sanitizeUpstreamErrorMessage(label), 2048))
}

// buildOpenAIRemoteCompactionV2ProbeExtraUpdates computes the independent
// native-v2 capability observation. Legacy /responses/compact state is never
// read or written here.
// probeErr includes transport, read and response execution failures; these do
// not change capability. Only a completed 2xx response without a compaction item
// establishes that the native-v2 contract was not fulfilled.
func buildOpenAIRemoteCompactionV2ProbeExtraUpdates(resp *http.Response, body []byte, probeErr error, compactionFound bool, now time.Time) map[string]any {
	updates := map[string]any{
		OpenAIRemoteCompactionV2CheckedAtExtraKey:  now.Format(time.RFC3339),
		OpenAIRemoteCompactionV2LastStatusExtraKey: nil,
	}

	if resp != nil {
		updates[OpenAIRemoteCompactionV2LastStatusExtraKey] = resp.StatusCode
	}

	switch {
	case probeErr != nil:
		updates[OpenAIRemoteCompactionV2LastErrorExtraKey] = truncateString(sanitizeUpstreamErrorMessage(probeErr.Error()), 2048)
	case resp == nil:
		updates[OpenAIRemoteCompactionV2LastErrorExtraKey] = "remote compaction v2 probe failed"
	default:
		errMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
		if errMsg == "" && len(body) > 0 {
			errMsg = strings.TrimSpace(string(body))
		}
		if errMsg == "" && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			errMsg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		errMsg = truncateString(sanitizeUpstreamErrorMessage(errMsg), 2048)
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300 && compactionFound:
			updates[OpenAIRemoteCompactionV2SupportedExtraKey] = true
			updates[OpenAIRemoteCompactionV2LastErrorExtraKey] = ""
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			updates[OpenAIRemoteCompactionV2SupportedExtraKey] = false
			updates[OpenAIRemoteCompactionV2LastErrorExtraKey] = "upstream returned 2xx without a compaction output item (native remote compaction v2 unsupported)"
		default:
			updates[OpenAIRemoteCompactionV2LastErrorExtraKey] = errMsg
		}
	}

	return updates
}

func mergeExtraUpdates(base map[string]any, more map[string]any) map[string]any {
	if len(base) == 0 && len(more) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(more))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range more {
		out[key] = value
	}
	return out
}
