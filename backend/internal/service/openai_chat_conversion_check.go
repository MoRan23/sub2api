package service

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIChatConversionCheckContextKey = "openai_chat_conversion_check"
const openAIChatConversionCheckActiveContextKey = "openai_chat_conversion_check_active"

// The source check is frozen before typed decoding or account adaptation. It
// retains diagnostics only, never the caller's body or semantic field values.
type openAIChatConversionCheckState struct {
	check      *apicompat.ChatConversionCheck
	err        error
	applicable bool
	logged     map[int64]bool
}

// PrepareOpenAIChatConversionCheck computes diagnostics without choosing a route
// or rejecting a request. Mixed provider groups can therefore prepare it before
// waiting for a slot, and enforce it only after selecting an OAuth account.
func PrepareOpenAIChatConversionCheck(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	if _, exists := c.Get(openAIChatConversionCheckContextKey); exists {
		return
	}
	state := &openAIChatConversionCheckState{logged: make(map[int64]bool)}
	state.applicable = gjson.GetBytes(body, "messages").Exists() || !gjson.GetBytes(body, "input").Exists()
	if state.applicable {
		state.check, state.err = apicompat.CheckOpenAIOAuthChatConversion(body)
		state.check = cloneOpenAIChatConversionCheck(state.check)
	}
	c.Set(openAIChatConversionCheckContextKey, state)
}

// ValidateOpenAIChatConversionForAccount does not send a response, retry, or
// penalize an account. Its typed error must be handled as a client error before
// the gateway's upstream failure accounting.
func ValidateOpenAIChatConversionForAccount(c *gin.Context, account *Account) error {
	if c == nil {
		return nil
	}
	c.Set(openAIChatConversionCheckActiveContextKey, false)
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	value, _ := c.Get(openAIChatConversionCheckContextKey)
	state, _ := value.(*openAIChatConversionCheckState)
	if state == nil || !state.applicable {
		return nil
	}
	c.Set(openAIChatConversionCheckActiveContextKey, true)
	if !state.logged[account.ID] {
		state.logged[account.ID] = true
		var conversionErr *apicompat.ChatConversionError
		if errors.As(state.err, &conversionErr) {
			logOpenAIChatConversionIssue(account.ID, "rejected", apicompat.ChatConversionIssue{
				Path: conversionErr.Path, Reason: conversionErr.Reason,
			})
		} else if state.check != nil {
			for _, issue := range state.check.Issues {
				logOpenAIChatConversionIssue(account.ID, state.check.Status, issue)
			}
		}
	}
	return state.err
}

// GetOpenAIChatConversionCheck returns an owned summary only for a validated
// OAuth Chat attempt. It does not manufacture an outbound observation.
func GetOpenAIChatConversionCheck(c *gin.Context) *apicompat.ChatConversionCheck {
	if c == nil || !c.GetBool(openAIChatConversionCheckActiveContextKey) {
		return nil
	}
	value, _ := c.Get(openAIChatConversionCheckContextKey)
	state, _ := value.(*openAIChatConversionCheckState)
	if state == nil || state.err != nil {
		return nil
	}
	return cloneOpenAIChatConversionCheck(state.check)
}

func cloneOpenAIChatConversionCheck(value *apicompat.ChatConversionCheck) *apicompat.ChatConversionCheck {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Issues = append([]apicompat.ChatConversionIssue(nil), value.Issues...)
	return &copy
}

var openAIChatConversionLogState = struct {
	sync.Mutex
	entries map[requestIntegrityLogKey]requestIntegrityLogEntry
}{entries: make(map[requestIntegrityLogKey]requestIntegrityLogEntry)}

func logOpenAIChatConversionIssue(accountID int64, status string, issue apicompat.ChatConversionIssue) {
	key := requestIntegrityLogKey{accountID: accountID, reason: issue.Reason}
	now := time.Now()
	openAIChatConversionLogState.Lock()
	entry, exists := openAIChatConversionLogState.entries[key]
	if exists && now.Sub(entry.emitted) < time.Minute {
		entry.suppressed++
		entry.touched = now
		openAIChatConversionLogState.entries[key] = entry
		openAIChatConversionLogState.Unlock()
		return
	}
	if !exists && len(openAIChatConversionLogState.entries) >= 4096 {
		var oldestKey requestIntegrityLogKey
		var oldest time.Time
		for candidate, value := range openAIChatConversionLogState.entries {
			if now.Sub(value.touched) > 10*time.Minute {
				delete(openAIChatConversionLogState.entries, candidate)
				continue
			}
			if oldest.IsZero() || value.touched.Before(oldest) {
				oldestKey, oldest = candidate, value.touched
			}
		}
		if len(openAIChatConversionLogState.entries) >= 4096 {
			delete(openAIChatConversionLogState.entries, oldestKey)
		}
	}
	openAIChatConversionLogState.entries[key] = requestIntegrityLogEntry{emitted: now, touched: now}
	openAIChatConversionLogState.Unlock()
	slog.Warn("openai.chat_conversion", "account_id", accountID, "status", status,
		"reason", issue.Reason, "field", issue.Path, "suppressed", entry.suppressed)
}

// Only the leading system prefix can be promoted to top-level instructions.
// Later system messages keep their conversation position as developer messages.
// This is scoped to the Chat OAuth adapter; native Responses keeps its policy.
func preserveOpenAIChatSystemMessageOrder(req *apicompat.ChatCompletionsRequest) {
	leading := true
	for i := range req.Messages {
		if req.Messages[i].Role != "system" {
			leading = false
			continue
		}
		if !leading {
			req.Messages[i].Role = "developer"
		}
	}
}
