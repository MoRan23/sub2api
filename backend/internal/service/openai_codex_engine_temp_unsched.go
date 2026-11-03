package service

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"
)

// Apply only administrator-enabled rules. Engine business errors otherwise
// retain their transparent behavior, including pool mode and status 529.
func (s *OpenAIGatewayService) observeCodexEngineTempUnschedulable(ctx context.Context, account *Account, resp *http.Response, failure *codexEngineResponseError, requestedModel string) {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.accountRepo == nil ||
		account == nil || !account.IsCodexEngine() || resp == nil || failure == nil ||
		isOpenAICandyTest(ctx) || isOpenAICandyTestAccount(account) ||
		!account.IsTempUnschedulableEnabled() || account.IsCustomErrorCodesEnabled() {
		return
	}
	status := resp.StatusCode
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		// A failed HTTP 200 stream can report a real upstream error status. The
		// diagnostic 502 fallback is not evidence for a status-specific rule.
		status = failure.reportedStatus
	}
	if status < http.StatusBadRequest || status > 599 || len(failure.tempUnschedBody) == 0 {
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	// Reuse rule ordering, configured scope, persistence and cache notification.
	// The public generic entry points also impose pool/default health policies.
	s.rateLimitService.tryTempUnschedulable(stateCtx, account, status, failure.tempUnschedBody, requestedModel)
}

// Retain only actual error fields, independently of diagnostic truncation and
// synthesized messages. Response output/history and unrelated metadata never
// participate in keyword matching, even when they precede response.error.
func codexEngineTempUnschedBody(payload []byte, failure gjson.Result, event string) []byte {
	unauthorizedDetail := gjson.GetBytes(payload, "detail").String() == "Unauthorized"
	if failure.IsObject() {
		return codexEngineTempUnschedFields(failure, gjson.Result{}, unauthorizedDetail)
	}
	if event != "" || len(payload) == 0 {
		return nil
	}
	if gjson.ValidBytes(payload) {
		return codexEngineTempUnschedFields(gjson.ParseBytes(payload), gjson.Result{}, unauthorizedDetail)
	}
	// Non-JSON HTTP rejections can be plain text or a proxy's HTML error page.
	return append([]byte(nil), payload[:min(len(payload), tempUnschedBodyMaxBytes)]...)
}

func mergeCodexEngineTempUnschedBodies(current, previous []byte) []byte {
	if len(current) == 0 {
		return previous
	}
	if len(previous) == 0 {
		return current
	}
	unauthorizedDetail := gjson.GetBytes(current, "detail").String() == "Unauthorized" || gjson.GetBytes(previous, "detail").String() == "Unauthorized"
	return codexEngineTempUnschedFields(gjson.GetBytes(current, "error"), gjson.GetBytes(previous, "error"), unauthorizedDetail)
}

// Limit both retained data and temporary encoding allocations. A field may be
// much longer than the ops log excerpt, but the existing 64 KiB rule-matching
// limit still applies. Keep the envelope valid for shared error-code parsing.
func codexEngineTempUnschedFields(current, previous gjson.Result, unauthorizedDetail bool) []byte {
	body := []byte(`{"error":{`)
	fields := 0
	if unauthorizedDetail {
		// Shared 401 handling recognizes this exact root field as a credential
		// failure. A nested error.detail must not acquire that meaning.
		body = []byte(`{"detail":"Unauthorized","error":{`)
	}
	for _, key := range []string{"code", "type", "message", "detail", "error"} {
		value := current.Get(key)
		if key == "message" && current.Type == gjson.String {
			// An HTTP error can be a JSON string containing only the rejection.
			value = current
		}
		if !value.Exists() || value.String() == "" {
			value = previous.Get(key)
		}
		if !value.Exists() || value.IsObject() || value.IsArray() || value.String() == "" {
			continue
		}
		prefix := `"` + key + `":`
		if fields > 0 {
			prefix = "," + prefix
		}
		budget := tempUnschedBodyMaxBytes - len(body) - len(prefix) - len("}}")
		if budget < 2 {
			break
		}
		text := value.String()
		text = text[:min(len(text), budget-2)]
		encoded, _ := json.Marshal(text)
		if len(encoded) > budget {
			// Escaping can expand a byte up to sixfold. Keep the longest fitting
			// prefix rather than subtracting encoded excess from the raw length,
			// which could erase an otherwise matching prefix. Rune boundaries
			// keep encoded lengths monotonic for multibyte error text.
			runes := []rune(text)
			low, high := 0, len(runes)
			encoded = []byte(`""`)
			for low < high {
				mid := low + (high-low+1)/2
				candidate, _ := json.Marshal(string(runes[:mid]))
				if len(candidate) <= budget {
					low, encoded = mid, candidate
				} else {
					high = mid - 1
				}
			}
		}
		body = append(body, prefix...)
		body = append(body, encoded...)
		fields++
	}
	if fields == 0 && !unauthorizedDetail {
		return nil
	}
	return append(body, '}', '}')
}
